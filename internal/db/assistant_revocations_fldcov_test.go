package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistantRevocationsFailClosedWithoutLedger(t *testing.T) {
	digest := strings.Repeat("a", 64)
	var missing *AssistantRevocations
	if missing.Permits(digest) {
		t.Fatal("nil ledger permitted a key")
	}

	unconfigured := LoadAssistantRevocations("")
	if unconfigured.Ready() || unconfigured.Permits(digest) {
		t.Fatal("unconfigured ledger permitted a key")
	}
	if err := unconfigured.Revoke(digest); err == nil {
		t.Fatal("unconfigured ledger accepted a revocation")
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assistant-keys.revoked"), 0o700); err != nil {
		t.Fatal(err)
	}
	unreadable := LoadAssistantRevocations(dir)
	if unreadable.Ready() || unreadable.Permits(digest) {
		t.Fatal("ledger that is not a regular file permitted a key")
	}

	ready := LoadAssistantRevocations(t.TempDir())
	if err := ready.Revoke("NOT-A-DIGEST"); err == nil {
		t.Fatal("ledger accepted an invalid digest")
	}
}
