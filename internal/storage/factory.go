package storage

import (
	"encoding/json"
	"fmt"
)

// NewAdapter constructs a storage adapter of the requested type from the JSON
// config blob stored on the storage_destinations row. When the config
// includes `bandwidth_limit_mbps > 0` AND the destination is a remote type
// (SFTP / SMB / NFS / WebDAV / S3), the returned adapter is wrapped in a
// rate-limited shell that throttles every Read/Write body to the requested
// megabits per second. Metadata operations (List/Stat/Delete/TestConnection)
// are never throttled. Local destinations never honour the limit — there is
// no upstream link to protect, throttling local I/O just slows backups for
// no operational benefit.
func NewAdapter(storageType, configJSON string) (Adapter, error) {
	// Universal optional field present on every storage type. Parsed
	// once up-front so the per-type config structs don't have to
	// duplicate the same field.
	var common struct {
		BandwidthLimitMbps int `json:"bandwidth_limit_mbps"`
	}
	_ = json.Unmarshal([]byte(configJSON), &common)

	adapter, err := newRawAdapter(storageType, configJSON)
	if err != nil {
		return nil, err
	}
	if storageType == "local" {
		// Local storage talks directly to the host's filesystem; no
		// network link to throttle. Skip the wrapper even if a stale
		// bandwidth_limit_mbps value lingers in the config from an
		// earlier release that exposed the field for local destinations.
		return adapter, nil
	}
	return WrapThrottled(adapter, common.BandwidthLimitMbps), nil
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
