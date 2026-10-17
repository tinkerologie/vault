package dnssd

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/miekg/dns"
)

// recordingConn is an MDNSConn that records every response synchronously,
// so a test can drive handleRequest directly and inspect what was sent.
type recordingConn struct {
	mu   sync.Mutex
	sent []*Response
}

func (c *recordingConn) SendQuery(q *Query) error { return nil }

func (c *recordingConn) SendResponse(resp *Response) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, resp)
	return nil
}

func (c *recordingConn) Read(ctx context.Context) <-chan *Request { return nil }
func (c *recordingConn) Drain(ctx context.Context)                {}
func (c *recordingConn) Close()                                   {}

func (c *recordingConn) take() []*Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.sent
	c.sent = nil
	return out
}

func newScopedService(t *testing.T, ifaces []string) Service {
	t.Helper()
	sv, err := NewService(Config{
		Host:   "Computer",
		Name:   "Test",
		Type:   "_asdf._tcp",
		Domain: "local",
		Port:   12345,
		Ifaces: ifaces,
	})
	if err != nil {
		t.Fatal(err)
	}
	ip := net.IP{192, 0, 2, 10}
	sv.ifaceIPs = map[string][]net.IP{"included": {ip}, "excluded": {ip}}
	return sv
}

func queryOn(iface, name string, qtype uint16) *Request {
	msg := new(dns.Msg)
	msg.SetQuestion(name, qtype)
	msg.RecursionDesired = false
	return &Request{
		msg:   msg,
		from:  &net.UDPAddr{IP: net.IP{192, 0, 2, 99}, Port: 5353},
		iface: &net.Interface{Name: iface, Flags: net.FlagUp | net.FlagMulticast},
	}
}

func countRecords(resps []*Response) (answers, extras int) {
	for _, r := range resps {
		answers += len(r.msg.Answer)
		extras += len(r.msg.Extra)
	}
	return answers, extras
}

// TestQueryOnExcludedInterfaceIsNotAnswered covers tinkerologie/vault#498:
// a service published only on "included" must not leak its PTR/SRV/TXT or
// address records in reply to a query received on "excluded".
func TestQueryOnExcludedInterfaceIsNotAnswered(t *testing.T) {
	scoped := newScopedService(t, []string{"included"})

	questions := []struct {
		name  string
		qname string
		qtype uint16
	}{
		{"service type PTR", scoped.ServiceName(), dns.TypePTR},
		{"instance SRV", scoped.EscapedServiceInstanceName(), dns.TypeSRV},
		{"instance TXT", scoped.EscapedServiceInstanceName(), dns.TypeTXT},
		{"hostname A", scoped.Hostname(), dns.TypeA},
		{"meta query", scoped.ServicesMetaQueryName(), dns.TypePTR},
	}

	for _, q := range questions {
		t.Run(q.name, func(t *testing.T) {
			conn := &recordingConn{}
			r := newResponder(conn)
			r.addManaged(scoped)

			r.handleRequest(queryOn("excluded", q.qname, q.qtype))
			if got := conn.take(); len(got) != 0 {
				t.Fatalf("excluded interface got %d responses, want none: %v", len(got), got[0].msg)
			}

			r.handleRequest(queryOn("included", q.qname, q.qtype))
			got := conn.take()
			if len(got) != 1 {
				t.Fatalf("included interface got %d responses, want 1", len(got))
			}
			if got[0].iface == nil || got[0].iface.Name != "included" {
				t.Fatalf("response sent on %v, want included", got[0].iface)
			}
			if answers, _ := countRecords(got); answers == 0 {
				t.Fatal("included interface response has no answers")
			}
		})
	}

	t.Run("service type PTR carries address on included", func(t *testing.T) {
		conn := &recordingConn{}
		r := newResponder(conn)
		r.addManaged(scoped)

		r.handleRequest(queryOn("included", scoped.ServiceName(), dns.TypePTR))
		got := conn.take()
		if len(got) != 1 {
			t.Fatalf("got %d responses, want 1", len(got))
		}
		var haveSRV, haveTXT, haveA bool
		for _, rr := range got[0].msg.Extra {
			switch rr := rr.(type) {
			case *dns.SRV:
				haveSRV = true
			case *dns.TXT:
				haveTXT = true
			case *dns.A:
				haveA = rr.A.Equal(net.IP{192, 0, 2, 10})
			}
		}
		if !haveSRV || !haveTXT || !haveA {
			t.Fatalf("extras SRV=%v TXT=%v A=%v, want all present", haveSRV, haveTXT, haveA)
		}
	})
}

// TestQueryForUnscopedServiceIsAnsweredEverywhere guards the unchanged
// default: a service with no Ifaces list stays visible on every interface.
func TestQueryForUnscopedServiceIsAnsweredEverywhere(t *testing.T) {
	unscoped := newScopedService(t, nil)

	for _, iface := range []string{"included", "excluded"} {
		conn := &recordingConn{}
		r := newResponder(conn)
		r.addManaged(unscoped)

		r.handleRequest(queryOn(iface, unscoped.ServiceName(), dns.TypePTR))
		if got := conn.take(); len(got) != 1 {
			t.Fatalf("%s: got %d responses, want 1", iface, len(got))
		}
	}
}

// TestScopedAndUnscopedServicesOnExcludedInterface checks the guard is per
// service: on an excluded interface the unscoped service still answers the
// meta query while the scoped one stays silent.
func TestScopedAndUnscopedServicesOnExcludedInterface(t *testing.T) {
	scoped := newScopedService(t, []string{"included"})
	other, err := NewService(Config{Name: "Other", Type: "_other._tcp", Domain: "local", Port: 1, Host: "Computer"})
	if err != nil {
		t.Fatal(err)
	}

	conn := &recordingConn{}
	r := newResponder(conn)
	r.addManaged(scoped)
	r.addManaged(other)

	r.handleRequest(queryOn("excluded", scoped.ServicesMetaQueryName(), dns.TypePTR))
	got := conn.take()
	if len(got) != 1 {
		t.Fatalf("got %d responses, want 1", len(got))
	}
	for _, rr := range got[0].msg.Answer {
		ptr, ok := rr.(*dns.PTR)
		if !ok {
			continue
		}
		if ptr.Ptr == scoped.ServiceName() {
			t.Fatalf("scoped service type %s leaked on excluded interface", ptr.Ptr)
		}
	}
}
