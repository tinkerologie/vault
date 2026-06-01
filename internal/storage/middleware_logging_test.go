package storage

import (
	"bytes"
	"io"
	"log"
	"strings"
	"testing"
)

type okDeleter struct{ mockAdapter }

func (okDeleter) Delete(string) error { return nil }

func TestLoggingEmitsWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)

	a := withLogging(okDeleter{}, "dest-1", true)
	_ = a.Delete("run3/vol0")

	if !strings.Contains(buf.String(), "delete") || !strings.Contains(buf.String(), "run3/vol0") {
		t.Errorf("expected delete trace, got %q", buf.String())
	}
}

func TestLoggingSilentWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)

	a := withLogging(okDeleter{}, "dest-1", false)
	_ = a.Delete("run3/vol0")

	if buf.Len() != 0 {
		t.Errorf("expected no output when disabled, got %q", buf.String())
	}
}
