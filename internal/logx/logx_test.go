package logx_test

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/logx"
)

func TestSetLevelString(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
		expStr   string
	}{
		{"debug", slog.LevelDebug, "debug"},
		{"DEBUG", slog.LevelDebug, "debug"},
		{"  debug  ", slog.LevelDebug, "debug"},
		{"info", slog.LevelInfo, "info"},
		{"INFO", slog.LevelInfo, "info"},
		{"warn", slog.LevelWarn, "warn"},
		{"warning", slog.LevelWarn, "warn"},
		{"WARN", slog.LevelWarn, "warn"},
		{"error", slog.LevelError, "error"},
		{"ERROR", slog.LevelError, "error"},
		{"unknown", slog.LevelInfo, "info"},
		{"", slog.LevelInfo, "info"},
		{"invalid_level", slog.LevelInfo, "info"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			logx.SetLevelString(tt.input)
			if got := logx.Level(); got != tt.expected {
				t.Errorf("SetLevelString(%q): Level() = %v, want %v", tt.input, got, tt.expected)
			}
			if got := logx.LevelString(); got != tt.expStr {
				t.Errorf("SetLevelString(%q): LevelString() = %q, want %q", tt.input, got, tt.expStr)
			}
		})
	}
}

func TestSetupAndLeveledFiltering(t *testing.T) {
	buf := new(bytes.Buffer)
	logx.Setup(buf)

	// Set to INFO
	logx.SetLevelString("info")
	buf.Reset()

	slog.Debug("debug message should be hidden")
	if buf.Len() > 0 {
		t.Errorf("expected debug to be filtered at INFO level, got: %s", buf.String())
	}

	buf.Reset()
	slog.Info("info message should appear", "key", "val")
	if !strings.Contains(buf.String(), "info message should appear") {
		t.Errorf("expected info message to appear, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "level=INFO") {
		t.Errorf("expected level=INFO in output, got: %s", buf.String())
	}

	// Dynamic switch to DEBUG at runtime
	logx.SetLevelString("debug")
	buf.Reset()
	slog.Debug("debug message should now appear", "num", 42)
	if !strings.Contains(buf.String(), "debug message should now appear") {
		t.Errorf("expected debug message after level change to debug, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "level=DEBUG") {
		t.Errorf("expected level=DEBUG in output, got: %s", buf.String())
	}

	// Test standard log integration (log.Print routes at INFO level)
	logx.SetLevelString("info")
	buf.Reset()
	log.Printf("standard log message via print %d", 123)
	if !strings.Contains(buf.String(), "standard log message via print 123") {
		t.Errorf("expected standard log to route through slog, got: %s", buf.String())
	}

	// Switch to WARN: standard log (INFO) without warning/error prefix should be filtered out
	logx.SetLevelString("warn")
	buf.Reset()
	log.Printf("another standard log message")
	if buf.Len() > 0 {
		t.Errorf("expected standard log (INFO) to be filtered at WARN level, got: %s", buf.String())
	}

	// But log.Printf with "Warning: ..." prefix should be promoted to WARN and printed
	buf.Reset()
	log.Printf("Warning: failed to validate database configuration")
	if !strings.Contains(buf.String(), "Warning: failed to validate database configuration") {
		t.Errorf("expected warning message to be preserved at WARN level, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("expected level=WARN in output, got: %s", buf.String())
	}

	// log.Printf with "Error: ..." prefix should be promoted to ERROR and printed at WARN level
	buf.Reset()
	log.Printf("Error: disk is full")
	if !strings.Contains(buf.String(), "Error: disk is full") || !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("expected error message to be preserved at WARN level, got: %s", buf.String())
	}

	// Switch to ERROR: warning prefix should be filtered, error prefix should be printed
	logx.SetLevelString("error")
	buf.Reset()
	log.Printf("Warning: something degraded")
	if buf.Len() > 0 {
		t.Errorf("expected warning to be filtered at ERROR level, got: %s", buf.String())
	}

	buf.Reset()
	log.Printf("Error: fatal startup failure")
	if !strings.Contains(buf.String(), "Error: fatal startup failure") || !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("expected error to appear at ERROR level, got: %s", buf.String())
	}

	// Reset level to info for clean state
	logx.SetLevelString("info")
}

func TestLeveledHelpers(t *testing.T) {
	buf := new(bytes.Buffer)
	logx.Setup(buf)

	logx.SetLevelString("debug")
	buf.Reset()
	logx.Debug("debug msg", "k", "v")
	if !strings.Contains(buf.String(), "debug msg") || !strings.Contains(buf.String(), "level=DEBUG") {
		t.Errorf("Debug failed: %s", buf.String())
	}

	buf.Reset()
	logx.Debugf("debug formatted %d", 42)
	if !strings.Contains(buf.String(), "debug formatted 42") || !strings.Contains(buf.String(), "level=DEBUG") {
		t.Errorf("Debugf failed: %s", buf.String())
	}

	buf.Reset()
	logx.Info("info msg")
	if !strings.Contains(buf.String(), "info msg") || !strings.Contains(buf.String(), "level=INFO") {
		t.Errorf("Info failed: %s", buf.String())
	}

	buf.Reset()
	logx.Infof("info formatted %s", "abc")
	if !strings.Contains(buf.String(), "info formatted abc") || !strings.Contains(buf.String(), "level=INFO") {
		t.Errorf("Infof failed: %s", buf.String())
	}

	buf.Reset()
	logx.Warn("warn msg")
	if !strings.Contains(buf.String(), "warn msg") || !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("Warn failed: %s", buf.String())
	}

	buf.Reset()
	logx.Warnf("warn formatted %d", 100)
	if !strings.Contains(buf.String(), "warn formatted 100") || !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("Warnf failed: %s", buf.String())
	}

	buf.Reset()
	logx.Error("error msg")
	if !strings.Contains(buf.String(), "error msg") || !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("Error failed: %s", buf.String())
	}

	buf.Reset()
	logx.Errorf("error formatted %v", true)
	if !strings.Contains(buf.String(), "error formatted true") || !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("Errorf failed: %s", buf.String())
	}

	// Test WithAttrs & WithGroup
	logger := slog.Default().With("component", "test").WithGroup("sub")
	buf.Reset()
	logger.Info("grouped message")
	if !strings.Contains(buf.String(), "grouped message") || !strings.Contains(buf.String(), "component=test") {
		t.Errorf("WithAttrs/WithGroup failed: %s", buf.String())
	}

	logx.SetLevelString("info")
}

func TestReplaceAttrTimeFormatting(t *testing.T) {
	buf := new(bytes.Buffer)
	logx.Setup(buf)
	logx.SetLevelString("info")

	slog.Info("testing timestamp formatting")
	out := buf.String()

	// Check time format conforms to RFC3339 prefix like time=202...
	if !strings.Contains(out, "time=") {
		t.Fatalf("expected time attribute in output, got: %s", out)
	}

	// Extract time value: time=...
	idx := strings.Index(out, "time=")
	timePart := out[idx+5:]
	if spaceIdx := strings.Index(timePart, " "); spaceIdx != -1 {
		timePart = timePart[:spaceIdx]
	}
	timePart = strings.Trim(timePart, "\"\n\r")

	if _, err := time.Parse(time.RFC3339, timePart); err != nil {
		t.Errorf("time attribute %q is not valid RFC3339: %v", timePart, err)
	}

	// Test zero time attribute and non-time / grouped attributes
	buf.Reset()
	slog.Info("msg with other attrs",
		slog.Time("zero_time", time.Time{}),
		slog.String("str", "hello"),
		slog.Group("grp", slog.Time(slog.TimeKey, time.Now())),
	)
	if !strings.Contains(buf.String(), "msg with other attrs") {
		t.Errorf("expected message to appear, got: %s", buf.String())
	}
}

func TestSetLevelAndLevelStringUnknown(t *testing.T) {
	logx.SetLevel(slog.Level(42))
	if got := logx.Level(); got != slog.Level(42) {
		t.Errorf("Level() = %v, want 42", got)
	}
	if got := logx.LevelString(); got != "info" {
		t.Errorf("LevelString() for custom level = %q, want info", got)
	}
	logx.SetLevel(slog.LevelInfo)
}

func TestCustomLevelBetweenWarnAndError(t *testing.T) {
	buf := new(bytes.Buffer)
	logx.Setup(buf)

	customLevel := slog.LevelWarn + 1
	logx.SetLevel(customLevel)

	// A normal standard log message (unprefixed) should be filtered out
	buf.Reset()
	log.Printf("normal status message")
	if buf.Len() > 0 {
		t.Errorf("expected normal info message to be filtered at custom level %v, got: %s", customLevel, buf.String())
	}

	// A warning message should be filtered out since customLevel > LevelWarn
	buf.Reset()
	log.Printf("Warning: minor warning")
	if buf.Len() > 0 {
		t.Errorf("expected warning to be filtered at custom level %v, got: %s", customLevel, buf.String())
	}

	// An error message should be promoted to LevelError (> customLevel) and printed
	buf.Reset()
	log.Printf("Error: critical failure")
	if !strings.Contains(buf.String(), "Error: critical failure") || !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("expected error message to be printed at custom level %v, got: %s", customLevel, buf.String())
	}

	logx.SetLevel(slog.LevelInfo)
}
