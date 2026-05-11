package storage

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNewWebDAVAdapter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  WebDAVConfig
		wantErr bool
	}{
		{
			name:    "valid https with auth",
			config:  WebDAVConfig{URL: "https://webdav.example.com/", Username: "u", Password: "p", BasePath: "vault-backups"},
			wantErr: false,
		},
		{
			name:    "valid http no auth",
			config:  WebDAVConfig{URL: "http://nas.local:8080/dav"},
			wantErr: false,
		},
		{
			name:    "valid with insecure tls",
			config:  WebDAVConfig{URL: "https://self-signed.local/", InsecureSkipVerify: true},
			wantErr: false,
		},
		{
			name:    "missing url",
			config:  WebDAVConfig{Username: "u", Password: "p"},
			wantErr: true,
		},
		{
			name:    "url without scheme",
			config:  WebDAVConfig{URL: "webdav.example.com"},
			wantErr: true,
		},
		{
			name:    "url with ftp scheme",
			config:  WebDAVConfig{URL: "ftp://example.com/"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewWebDAVAdapter(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewWebDAVAdapter() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if a == nil {
					t.Fatal("adapter is nil")
				}
				if strings.HasSuffix(a.config.URL, "/") {
					t.Errorf("trailing slash should be trimmed: %q", a.config.URL)
				}
			}
		})
	}
}

func TestWebDAVRejectsTraversal(t *testing.T) {
	t.Parallel()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.fullPath("../escape", false); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := a.fullPath("ok/relative", false); err != nil {
		t.Fatalf("legit path rejected: %v", err)
	}
}

// TestWebDAVNoOverallTimeoutByDefault asserts the regression behind the
// #83 follow-up report: previously the WebDAV client had a hard 60-second
// http.Client.Timeout that killed multi-GB PUTs ("context deadline
// exceeded ... Client.Timeout exceeded while awaiting headers"). Default
// configs must carry TimeoutSeconds=0 (no overall lifetime cap) so that
// client() leaves http.Client.Timeout at its zero value. Only an explicit
// TimeoutSeconds opts the operator back into a hard deadline.
func TestWebDAVNoOverallTimeoutByDefault(t *testing.T) {
	t.Parallel()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if a.config.TimeoutSeconds != 0 {
		t.Fatalf("default TimeoutSeconds = %d, want 0 (unlimited)", a.config.TimeoutSeconds)
	}
	// client() must succeed without panic when timeout is unset.
	if c := a.client(); c == nil {
		t.Fatal("client() returned nil")
	}

	a2, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/", TimeoutSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if a2.config.TimeoutSeconds != 600 {
		t.Fatalf("explicit TimeoutSeconds=600 -> %d, want 600", a2.config.TimeoutSeconds)
	}
}

// TestWebDAVStallTimeoutDefaults locks in the 5-minute default watchdog
// window and respects negative-value opt-out.
func TestWebDAVStallTimeoutDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  int
		want time.Duration
	}{
		{"zero defaults to 5m", 0, 5 * time.Minute},
		{"explicit override", 30, 30 * time.Second},
		{"negative disables", -1, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/", StallTimeoutSeconds: tc.cfg})
			if err != nil {
				t.Fatal(err)
			}
			if got := a.stallTimeout(); got != tc.want {
				t.Fatalf("stallTimeout() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestStallReaderFiresOnNoProgress ensures the upload watchdog aborts a
// reader that delivers zero bytes for longer than the configured window.
func TestStallReaderFiresOnNoProgress(t *testing.T) {
	t.Parallel()
	// blockingReader returns 0,nil forever until released — simulates a
	// stuck upload where the upstream goroutine is alive but no data is
	// flowing.
	br := &blockingReader{}
	sr := newStallReader(br, 200*time.Millisecond)
	defer sr.cancel()

	deadline := time.After(2 * time.Second)
	for {
		buf := make([]byte, 16)
		_, err := sr.Read(buf)
		if errors.Is(err, ErrUploadStalled) {
			return // success
		}
		if err != nil && err != io.EOF {
			t.Fatalf("unexpected error: %v", err)
		}
		select {
		case <-deadline:
			t.Fatal("stallReader did not fire ErrUploadStalled in time")
		default:
		}
	}
}

// TestStallReaderAllowsSlowProgress confirms a slow-but-steady reader is
// not falsely aborted: as long as some bytes flow within the window, the
// watchdog stays armed but never fires.
func TestStallReaderAllowsSlowProgress(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader(bytes.Repeat([]byte("a"), 1024))
	sr := newStallReader(src, 500*time.Millisecond)
	defer sr.cancel()

	read := 0
	buf := make([]byte, 32)
	for {
		n, err := sr.Read(buf)
		read += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // well under 500ms window
	}
	if read != 1024 {
		t.Fatalf("read %d bytes, want 1024", read)
	}
}

// blockingReader returns (0, nil) indefinitely. Used to simulate a stuck
// upload pipe for the stall watchdog tests.
type blockingReader struct{}

func (b *blockingReader) Read(_ []byte) (int, error) {
	time.Sleep(50 * time.Millisecond)
	return 0, nil
}
