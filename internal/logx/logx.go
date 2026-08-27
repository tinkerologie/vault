// Package logx provides centralized, runtime-adjustable leveled logging using
// log/slog. It bridges Go's standard log package output into a shared slog
// handler so that existing log.Print* calls respect the runtime log level
// while preserving warning and error message visibility.
//
// This package is intentionally kept free of database or settings dependencies.
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// levelVar is the single source of truth for the active log level across the process.
// It initializes to slog.LevelInfo (zero value).
var levelVar = new(slog.LevelVar)

// Setup initializes the global default logger with a text handler writing to w
// at the level governed by levelVar. Standard library log output is routed
// through this handler, with severity inference for conventional prefixes
// ("warning:", "warn", "error:", "err:", "debug:", etc.).
func Setup(w io.Writer) {
	opts := &slog.HandlerOptions{
		Level:       levelVar,
		ReplaceAttr: replaceAttr,
	}
	textHandler := slog.NewTextHandler(w, opts)
	handler := &bridgedHandler{inner: textHandler}
	slog.SetDefault(slog.New(handler))
}

// bridgedHandler wraps an slog.Handler to ensure standard library log messages
// (which enter slog at LevelInfo by default) have their severity inferred from
// message prefixes, allowing Warnings and Errors from legacy log.Printf sites
// to remain visible even when the log level is set to Warn or Error.
type bridgedHandler struct {
	inner slog.Handler
}

func (b *bridgedHandler) Enabled(ctx context.Context, level slog.Level) bool {
	// If active level is above Info (e.g. Warn, Error, or custom levels in-between),
	// stdlib log messages (entering at LevelInfo) must pass Enabled check so Handle
	// can inspect message prefixes and promote warnings/errors appropriately.
	if level == slog.LevelInfo && levelVar.Level() > slog.LevelInfo && levelVar.Level() <= slog.LevelError {
		return true
	}
	return level >= levelVar.Level()
}

func (b *bridgedHandler) Handle(ctx context.Context, r slog.Record) error {
	effectiveLevel := r.Level
	if r.Level == slog.LevelInfo {
		msg := strings.TrimSpace(r.Message)
		lower := strings.ToLower(msg)
		if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "err:") || strings.HasPrefix(lower, "fatal") || strings.HasPrefix(lower, "panic") {
			effectiveLevel = slog.LevelError
		} else if strings.HasPrefix(lower, "warning") || strings.HasPrefix(lower, "warn") {
			effectiveLevel = slog.LevelWarn
		} else if strings.HasPrefix(lower, "debug") || strings.HasPrefix(lower, "[debug]") {
			effectiveLevel = slog.LevelDebug
		}
	}

	if effectiveLevel < levelVar.Level() {
		return nil
	}

	if effectiveLevel != r.Level {
		r.Level = effectiveLevel
	}

	return b.inner.Handle(ctx, r)
}

func (b *bridgedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &bridgedHandler{inner: b.inner.WithAttrs(attrs)}
}

func (b *bridgedHandler) WithGroup(name string) slog.Handler {
	return &bridgedHandler{inner: b.inner.WithGroup(name)}
}

// replaceAttr formats record attributes to ensure log lines stay clean,
// readable, and compatible with diagnostics collection.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		if t := a.Value.Time(); !t.IsZero() {
			return slog.String(slog.TimeKey, t.Format(time.RFC3339))
		}
	}
	return a
}

// SetLevelString updates the active log level from a case-insensitive string name.
// Allowed values are "debug", "info", "warn" (or "warning"), and "error".
// Any unknown or empty value safely falls back to Info.
func SetLevelString(s string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		levelVar.Set(slog.LevelDebug)
	case "info":
		levelVar.Set(slog.LevelInfo)
	case "warn", "warning":
		levelVar.Set(slog.LevelWarn)
	case "error":
		levelVar.Set(slog.LevelError)
	default:
		levelVar.Set(slog.LevelInfo)
	}
}

// SetLevel sets the active log level directly.
func SetLevel(l slog.Level) {
	levelVar.Set(l)
}

// Level returns the current active log level.
func Level() slog.Level {
	return levelVar.Level()
}

// LevelString returns the current active log level as a lowercase string
// ("debug", "info", "warn", "error").
func LevelString() string {
	switch levelVar.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelInfo:
		return "info"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

// Debug logs at LevelDebug.
func Debug(msg string, args ...any) {
	slog.Default().Debug(msg, args...)
}

// Info logs at LevelInfo.
func Info(msg string, args ...any) {
	slog.Default().Info(msg, args...)
}

// Warn logs at LevelWarn.
func Warn(msg string, args ...any) {
	slog.Default().Warn(msg, args...)
}

// Error logs at LevelError.
func Error(msg string, args ...any) {
	slog.Default().Error(msg, args...)
}

// Debugf formats and logs at LevelDebug.
func Debugf(format string, args ...any) {
	slog.Default().Debug(fmt.Sprintf(format, args...))
}

// Infof formats and logs at LevelInfo.
func Infof(format string, args ...any) {
	slog.Default().Info(fmt.Sprintf(format, args...))
}

// Warnf formats and logs at LevelWarn.
func Warnf(format string, args ...any) {
	slog.Default().Warn(fmt.Sprintf(format, args...))
}

// Errorf formats and logs at LevelError.
func Errorf(format string, args ...any) {
	slog.Default().Error(fmt.Sprintf(format, args...))
}
