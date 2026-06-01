package storage

import (
	"encoding/json"
	"fmt"
)

// Options tunes the middleware chain. Zero value = production defaults with
// verbose logging off.
type Options struct {
	VerboseLogging bool
	DestLabel      string // human destination name for logs/metrics
}

// NewAdapter constructs a storage adapter of the requested type from the JSON
// config blob stored on the storage_destinations row. The adapter is wrapped
// in the full middleware chain: throttle (network backends only) → retry →
// metrics → logging. Signature is unchanged from prior versions.
func NewAdapter(storageType, configJSON string) (Adapter, error) {
	return NewAdapterWithOptions(storageType, configJSON, Options{})
}

// NewAdapterWithOptions is like NewAdapter but accepts an Options struct to
// control the middleware chain. Use it when you need verbose logging or a
// custom destination label for metrics/logs.
//
// The chain order from innermost to outermost is:
//
//	provider → [throttle (network only)] → retry → metrics → logging
//
// Throttle wraps the raw provider so that a throttled-then-failed write is
// re-issued cleanly from the start of the stream on retry. Metrics and logging
// sit outermost so they record the logical outcome (including all retry
// attempts) rather than each individual attempt.
func NewAdapterWithOptions(storageType, configJSON string, opts Options) (Adapter, error) {
	// Universal optional field present on every storage type. Parsed
	// once up-front so the per-type config structs don't have to
	// duplicate the same field.
	var common struct {
		BandwidthLimitMbps int `json:"bandwidth_limit_mbps"`
	}
	_ = json.Unmarshal([]byte(configJSON), &common)

	provider, err := newRawAdapter(storageType, configJSON)
	if err != nil {
		return nil, err
	}

	a := provider
	// throttle (innermost wrapper): skipped for local; no-op when limit <= 0.
	// Local storage talks directly to the host filesystem — there is no network
	// link to protect, so throttling would only slow backups for no benefit.
	if storageType != "local" {
		a = WrapThrottled(a, common.BandwidthLimitMbps)
	}
	// retry wraps throttle so a throttled-then-failed attempt is re-issued.
	a = withRetry(a, DefaultRetryPolicy)
	// metrics + logging outermost record the logical op and final outcome.
	label := opts.DestLabel
	if label == "" {
		label = storageType
	}
	a = withMetrics(a, label)
	a = withLogging(a, label, opts.VerboseLogging)
	return a, nil
}

// newRawAdapter is the original type-dispatched factory, kept here so the
// throttled-wrap step in NewAdapter is the only public entry point.
func newRawAdapter(storageType, configJSON string) (Adapter, error) {
	switch storageType {
	case "local":
		var cfg struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
			return nil, fmt.Errorf("parse local config: %w", err)
		}
		return NewLocalAdapter(cfg.Path), nil
	case "sftp":
		var cfg SFTPConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
			return nil, fmt.Errorf("parse sftp config: %w", err)
		}
		return NewSFTPAdapter(cfg)
	case "smb":
		var cfg SMBConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
			return nil, fmt.Errorf("parse smb config: %w", err)
		}
		return NewSMBAdapter(cfg)
	case "nfs":
		var cfg NFSConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
			return nil, fmt.Errorf("parse nfs config: %w", err)
		}
		return NewNFSAdapter(cfg)
	case "webdav":
		var cfg WebDAVConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
			return nil, fmt.Errorf("parse webdav config: %w", err)
		}
		return NewWebDAVAdapter(cfg)
	case "s3":
		var cfg S3Config
		if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
			return nil, fmt.Errorf("parse s3 config: %w", err)
		}
		return NewS3Adapter(cfg)
	default:
		return nil, fmt.Errorf("unknown storage type: %s", storageType)
	}
}
