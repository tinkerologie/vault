package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestDaemonHeartbeatJoin(t *testing.T) {
	for _, lateError := range []bool{false, true} {
		name := "graceful"
		if lateError {
			name = "late_tls_error"
		}
		t.Run(name, func(t *testing.T) {
			env := newDaemonEnv458(t)
			previous := daemonStartHeartbeat
			t.Cleanup(func() { daemonStartHeartbeat = previous })
			started, stopEntered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var heartbeatPath string
			daemonStartHeartbeat = func(_ context.Context, path, _ string) func() {
				heartbeatPath = path
				close(started)
				return func() {
					close(stopEntered)
					<-release
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			if !lateError {
				cancel()
			}
			shutdownNotifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
				return ctx, cancel
			}
			cmd := &cobra.Command{Use: "daemon"}
			cmd.SetContext(t.Context())
			cmd.Flags().String("db", env.dbPath, "")
			cmd.Flags().String("addr", "127.0.0.1:0", "")
			cert, key := "", ""
			if lateError {
				cert, key = filepath.Join(env.dir, "missing.crt"), filepath.Join(env.dir, "missing.key")
			}
			cmd.Flags().String("tls-cert", cert, "")
			cmd.Flags().String("tls-key", key, "")
			result, finished := make(chan error, 1), make(chan struct{})
			t.Cleanup(func() {
				cancel()
				unblock()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("daemon did not finish during cleanup")
				}
			})
			go func() {
				defer close(finished)
				result <- daemonCmd.RunE(cmd, nil)
			}()
			select {
			case <-stopEntered:
			case err := <-result:
				t.Fatalf("daemon returned without joining heartbeat: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("daemon never reached heartbeat join")
			}
			select {
			case <-started:
			default:
				t.Fatal("heartbeat stop called without start")
			}
			select {
			case err := <-result:
				t.Fatalf("daemon returned before heartbeat stop finished: %v", err)
			default:
			}
			unblock()
			select {
			case err := <-result:
				if lateError {
					if err == nil || !strings.Contains(err.Error(), "missing.crt") {
						t.Fatalf("expected late TLS error, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("graceful shutdown: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("daemon did not return after heartbeat stopped")
			}
			if want := filepath.Join(filepath.Dir(env.dbPath), "heartbeat"); heartbeatPath != want {
				t.Errorf("heartbeat path = %q, want %q", heartbeatPath, want)
			}
		})
	}
}
