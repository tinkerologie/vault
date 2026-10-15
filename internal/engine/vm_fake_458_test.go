//go:build linux

package engine

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// fakeLibvirt is an in-memory libvirtClient for the VM engine tests (#458).
// Every method records its name and delegates to the matching *Fn field when
// set; an unset field yields a benign success (zero values, nil error, and a
// running domain for DomainGetState). Nothing here talks to a hypervisor.
type fakeLibvirt struct {
	mu    sync.Mutex
	calls []string

	DisconnectFn                   func() error
	ConnectListAllDomainsFn        func() ([]libvirt.Domain, uint32, error)
	DomainLookupByNameFn           func(name string) (libvirt.Domain, error)
	DomainGetStateFn               func(dom libvirt.Domain) (int32, int32, error)
	DomainGetXMLDescFn             func(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error)
	DomainGetInfoFn                func(dom libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error)
	DomainDefineXMLFlagsFn         func(xml string) (libvirt.Domain, error)
	DomainCreateFn                 func(dom libvirt.Domain) error
	DomainCreateWithFlagsFn        func(dom libvirt.Domain, flags uint32) (libvirt.Domain, error)
	DomainResumeFn                 func(dom libvirt.Domain) error
	DomainShutdownFlagsFn          func(dom libvirt.Domain) error
	DomainDestroyFn                func(dom libvirt.Domain) error
	DomainUndefineFn               func(dom libvirt.Domain) error
	DomainUndefineFlagsFn          func(dom libvirt.Domain, flags libvirt.DomainUndefineFlagsValues) error
	DomainHasManagedSaveImageFn    func(dom libvirt.Domain) (int32, error)
	DomainManagedSaveRemoveFn      func(dom libvirt.Domain) error
	DomainCheckpointLookupByNameFn func(dom libvirt.Domain, name string) (libvirt.DomainCheckpoint, error)
	DomainCheckpointCreateXMLFn    func(dom libvirt.Domain, xml string) (libvirt.DomainCheckpoint, error)
	DomainCheckpointDeleteFn       func(cp libvirt.DomainCheckpoint) error
	DomainBackupBeginFn            func(dom libvirt.Domain, backupXML string) error
	DomainGetJobStatsFn            func(dom libvirt.Domain, flags libvirt.DomainGetJobStatsFlags) (int32, []libvirt.TypedParam, error)
	DomainAbortJobFn               func(dom libvirt.Domain) error
	DomainGetBlockJobInfoFn        func(dom libvirt.Domain, path string) (int32, int32, uint64, uint64, uint64, error)
	DomainBlockJobAbortFn          func(dom libvirt.Domain, path string, flags libvirt.DomainBlockJobAbortFlags) error
	DomainBlockCommitFn            func(dom libvirt.Domain, disk string) error
	QEMUDomainAgentCommandFn       func(dom libvirt.Domain, cmd string) (libvirt.OptString, error)
	DomainInterfaceAddressesFn     func(dom libvirt.Domain, source uint32) ([]libvirt.DomainInterface, error)
}

var _ libvirtClient = (*fakeLibvirt)(nil)

func (f *fakeLibvirt) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

// count reports how many times the named method was called.
func (f *fakeLibvirt) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (f *fakeLibvirt) Disconnect() error {
	f.record("Disconnect")
	if f.DisconnectFn != nil {
		return f.DisconnectFn()
	}
	return nil
}

func (f *fakeLibvirt) ConnectListAllDomains(int32, libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error) {
	f.record("ConnectListAllDomains")
	if f.ConnectListAllDomainsFn != nil {
		return f.ConnectListAllDomainsFn()
	}
	return nil, 0, nil
}

func (f *fakeLibvirt) DomainLookupByName(name string) (libvirt.Domain, error) {
	f.record("DomainLookupByName")
	if f.DomainLookupByNameFn != nil {
		return f.DomainLookupByNameFn(name)
	}
	return libvirt.Domain{Name: name}, nil
}

func (f *fakeLibvirt) DomainGetState(dom libvirt.Domain, _ uint32) (int32, int32, error) {
	f.record("DomainGetState")
	if f.DomainGetStateFn != nil {
		return f.DomainGetStateFn(dom)
	}
	return int32(libvirt.DomainRunning), 0, nil
}

func (f *fakeLibvirt) DomainGetXMLDesc(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
	f.record("DomainGetXMLDesc")
	if f.DomainGetXMLDescFn != nil {
		return f.DomainGetXMLDescFn(dom, flags)
	}
	return "", nil
}

func (f *fakeLibvirt) DomainGetInfo(dom libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error) {
	f.record("DomainGetInfo")
	if f.DomainGetInfoFn != nil {
		return f.DomainGetInfoFn(dom)
	}
	return 0, 0, 0, 0, 0, nil
}

func (f *fakeLibvirt) DomainDefineXMLFlags(xml string, _ libvirt.DomainDefineFlags) (libvirt.Domain, error) {
	f.record("DomainDefineXMLFlags")
	if f.DomainDefineXMLFlagsFn != nil {
		return f.DomainDefineXMLFlagsFn(xml)
	}
	return libvirt.Domain{}, nil
}

func (f *fakeLibvirt) DomainCreate(dom libvirt.Domain) error {
	f.record("DomainCreate")
	if f.DomainCreateFn != nil {
		return f.DomainCreateFn(dom)
	}
	return nil
}

func (f *fakeLibvirt) DomainCreateWithFlags(dom libvirt.Domain, flags uint32) (libvirt.Domain, error) {
	f.record("DomainCreateWithFlags")
	if f.DomainCreateWithFlagsFn != nil {
		return f.DomainCreateWithFlagsFn(dom, flags)
	}
	return dom, nil
}

func (f *fakeLibvirt) DomainResume(dom libvirt.Domain) error {
	f.record("DomainResume")
	if f.DomainResumeFn != nil {
		return f.DomainResumeFn(dom)
	}
	return nil
}

func (f *fakeLibvirt) DomainShutdownFlags(dom libvirt.Domain, _ libvirt.DomainShutdownFlagValues) error {
	f.record("DomainShutdownFlags")
	if f.DomainShutdownFlagsFn != nil {
		return f.DomainShutdownFlagsFn(dom)
	}
	return nil
}

func (f *fakeLibvirt) DomainDestroy(dom libvirt.Domain) error {
	f.record("DomainDestroy")
	if f.DomainDestroyFn != nil {
		return f.DomainDestroyFn(dom)
	}
	return nil
}

func (f *fakeLibvirt) DomainUndefine(dom libvirt.Domain) error {
	f.record("DomainUndefine")
	if f.DomainUndefineFn != nil {
		return f.DomainUndefineFn(dom)
	}
	return nil
}

func (f *fakeLibvirt) DomainUndefineFlags(dom libvirt.Domain, flags libvirt.DomainUndefineFlagsValues) error {
	f.record("DomainUndefineFlags")
	if f.DomainUndefineFlagsFn != nil {
		return f.DomainUndefineFlagsFn(dom, flags)
	}
	return nil
}

func (f *fakeLibvirt) DomainHasManagedSaveImage(dom libvirt.Domain, _ uint32) (int32, error) {
	f.record("DomainHasManagedSaveImage")
	if f.DomainHasManagedSaveImageFn != nil {
		return f.DomainHasManagedSaveImageFn(dom)
	}
	return 0, nil
}

func (f *fakeLibvirt) DomainManagedSaveRemove(dom libvirt.Domain, _ uint32) error {
	f.record("DomainManagedSaveRemove")
	if f.DomainManagedSaveRemoveFn != nil {
		return f.DomainManagedSaveRemoveFn(dom)
	}
	return nil
}

func (f *fakeLibvirt) DomainCheckpointLookupByName(dom libvirt.Domain, name string, _ uint32) (libvirt.DomainCheckpoint, error) {
	f.record("DomainCheckpointLookupByName")
	if f.DomainCheckpointLookupByNameFn != nil {
		return f.DomainCheckpointLookupByNameFn(dom, name)
	}
	return libvirt.DomainCheckpoint{Name: name, Dom: dom}, nil
}

func (f *fakeLibvirt) DomainCheckpointCreateXML(dom libvirt.Domain, xml string, _ uint32) (libvirt.DomainCheckpoint, error) {
	f.record("DomainCheckpointCreateXML")
	if f.DomainCheckpointCreateXMLFn != nil {
		return f.DomainCheckpointCreateXMLFn(dom, xml)
	}
	return libvirt.DomainCheckpoint{Dom: dom}, nil
}

func (f *fakeLibvirt) DomainCheckpointDelete(cp libvirt.DomainCheckpoint, _ libvirt.DomainCheckpointDeleteFlags) error {
	f.record("DomainCheckpointDelete")
	if f.DomainCheckpointDeleteFn != nil {
		return f.DomainCheckpointDeleteFn(cp)
	}
	return nil
}

func (f *fakeLibvirt) DomainBackupBegin(dom libvirt.Domain, backupXML string, _ libvirt.OptString, _ libvirt.DomainBackupBeginFlags) error {
	f.record("DomainBackupBegin")
	if f.DomainBackupBeginFn != nil {
		return f.DomainBackupBeginFn(dom, backupXML)
	}
	return nil
}

func (f *fakeLibvirt) DomainGetJobStats(dom libvirt.Domain, flags libvirt.DomainGetJobStatsFlags) (int32, []libvirt.TypedParam, error) {
	f.record("DomainGetJobStats")
	if f.DomainGetJobStatsFn != nil {
		return f.DomainGetJobStatsFn(dom, flags)
	}
	return int32(libvirt.DomainJobCompleted), nil, nil
}

func (f *fakeLibvirt) DomainAbortJob(dom libvirt.Domain) error {
	f.record("DomainAbortJob")
	if f.DomainAbortJobFn != nil {
		return f.DomainAbortJobFn(dom)
	}
	return nil
}

func (f *fakeLibvirt) DomainGetBlockJobInfo(dom libvirt.Domain, path string, _ uint32) (int32, int32, uint64, uint64, uint64, error) {
	f.record("DomainGetBlockJobInfo")
	if f.DomainGetBlockJobInfoFn != nil {
		return f.DomainGetBlockJobInfoFn(dom, path)
	}
	return 0, 0, 0, 0, 0, nil
}

func (f *fakeLibvirt) DomainBlockJobAbort(dom libvirt.Domain, path string, flags libvirt.DomainBlockJobAbortFlags) error {
	f.record("DomainBlockJobAbort")
	if f.DomainBlockJobAbortFn != nil {
		return f.DomainBlockJobAbortFn(dom, path, flags)
	}
	return nil
}

func (f *fakeLibvirt) DomainBlockCommit(dom libvirt.Domain, disk string, _, _ libvirt.OptString, _ uint64, _ libvirt.DomainBlockCommitFlags) error {
	f.record("DomainBlockCommit")
	if f.DomainBlockCommitFn != nil {
		return f.DomainBlockCommitFn(dom, disk)
	}
	return nil
}

func (f *fakeLibvirt) QEMUDomainAgentCommand(dom libvirt.Domain, cmd string, _ int32, _ uint32) (libvirt.OptString, error) {
	f.record("QEMUDomainAgentCommand")
	if f.QEMUDomainAgentCommandFn != nil {
		return f.QEMUDomainAgentCommandFn(dom, cmd)
	}
	return nil, nil
}

func (f *fakeLibvirt) DomainInterfaceAddresses(dom libvirt.Domain, source uint32, _ uint32) ([]libvirt.DomainInterface, error) {
	f.record("DomainInterfaceAddresses")
	if f.DomainInterfaceAddressesFn != nil {
		return f.DomainInterfaceAddressesFn(dom, source)
	}
	return nil, nil
}

// fakeSock stands in for the libvirt socket. Only Close is ever used by the
// VM engine; the embedded nil net.Conn makes any other call panic loudly.
type fakeSock struct {
	net.Conn
	closes   atomic.Int32
	closeErr error
}

func (s *fakeSock) Close() error {
	s.closes.Add(1)
	return s.closeErr
}

// stateSeq returns a DomainGetStateFn that yields the given states in order
// and then keeps repeating the last one.
func stateSeq(states ...libvirt.DomainState) func(libvirt.Domain) (int32, int32, error) {
	var mu sync.Mutex
	i := 0
	return func(libvirt.Domain) (int32, int32, error) {
		mu.Lock()
		defer mu.Unlock()
		s := states[i]
		if i < len(states)-1 {
			i++
		}
		return int32(s), 0, nil
	}
}

// noDomainErr is the libvirt error the engine treats as "domain is gone".
var noDomainErr = libvirt.Error{Code: uint32(libvirt.ErrNoDomain), Message: "domain not found"}

// fastVMPolling shrinks the VM poll/timeout knobs for the duration of a test.
// Tests that call it must not run in parallel.
func fastVMPolling(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldTimeout, oldPoll, oldAbort, oldSample := vmShutdownTimeout, vmShutdownPollInterval, vmAbortJobRetryDelay, vmActivitySampleInterval
	vmShutdownTimeout = timeout
	vmShutdownPollInterval = time.Millisecond
	vmAbortJobRetryDelay = time.Millisecond
	vmActivitySampleInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		vmShutdownTimeout, vmShutdownPollInterval, vmAbortJobRetryDelay, vmActivitySampleInterval = oldTimeout, oldPoll, oldAbort, oldSample
	})
}

// progressLog records progress callbacks so tests can assert on milestones.
type progressLog struct {
	mu      sync.Mutex
	entries []progressEntry
}

type progressEntry struct {
	pct int
	msg string
}

func (p *progressLog) fn(_ string, pct int, msg string) {
	p.mu.Lock()
	p.entries = append(p.entries, progressEntry{pct: pct, msg: msg})
	p.mu.Unlock()
}

func (p *progressLog) has(msg string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.msg == msg {
			return true
		}
	}
	return false
}

func (p *progressLog) hasPct(pct int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.pct == pct {
			return true
		}
	}
	return false
}

func noProgress(string, int, string) {}
