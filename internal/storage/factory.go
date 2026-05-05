package storage

import (
	"encoding/json"
	"fmt"
)

func NewAdapter(storageType, configJSON string) (Adapter, error) {
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
