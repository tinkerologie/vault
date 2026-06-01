package storage

import (
	"encoding/json"
	"testing"
)

func TestFactoryChainWrapsWithLoggingOutermost(t *testing.T) {
	cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	a, err := NewAdapter("local", string(cfg))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if _, ok := a.(*loggingAdapter); !ok {
		t.Fatalf("expected *loggingAdapter at the top of the chain, got %T", a)
	}
}

func TestFactoryWithOptionsVerbose(t *testing.T) {
	cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	a, err := NewAdapterWithOptions("local", string(cfg), Options{VerboseLogging: true, DestLabel: "my-dest"})
	if err != nil {
		t.Fatalf("NewAdapterWithOptions: %v", err)
	}
	if _, ok := a.(*loggingAdapter); !ok {
		t.Fatalf("expected *loggingAdapter, got %T", a)
	}
}
