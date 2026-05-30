package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/ruaan-deysel/vault/internal/safepath"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SFTPConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	Password       string `json:"password"`
	KeyFile        string `json:"key_file"`
	BasePath       string `json:"base_path"`
	Path           string `json:"path"`             // Deprecated alias for BasePath; kept for backward compatibility.
	HostKey        string `json:"host_key"`         // SHA-256 fingerprint of host public key
	KnownHostsFile string `json:"known_hosts_file"` // Path to OpenSSH known_hosts file
}

type SFTPAdapter struct {
	config SFTPConfig
}

func NewSFTPAdapter(config SFTPConfig) (*SFTPAdapter, error) {
	if config.Port == 0 {
		config.Port = 22
	}
	// Backward compatibility: accept "path" as alias for "base_path".
	if config.BasePath == "" && config.Path != "" {
		config.BasePath = config.Path
	}
	return &SFTPAdapter{config: config}, nil
}

func (s *SFTPAdapter) connect() (*sftp.Client, error) {
	var authMethods []ssh.AuthMethod
	if s.config.Password != "" {
		authMethods = append(authMethods, ssh.Password(s.config.Password))
	}
	if s.config.KeyFile != "" {
		key, err := os.ReadFile(s.config.KeyFile) // #nosec G304 — KeyFile is admin-configured storage config
		if err != nil {
			return nil, fmt.Errorf("read key file: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	sshConfig := &ssh.ClientConfig{
		User:            s.config.User,
		Auth:            authMethods,
		HostKeyCallback: s.hostKeyCallback(),
	}

	addr := net.JoinHostPort(s.config.Host, fmt.Sprintf("%d", s.config.Port))
	conn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}
	return client, nil
}

func (s *SFTPAdapter) fullPath(path string, allowRoot bool) (string, error) {
	fullPath, err := safepath.JoinUnderBase(s.config.BasePath, path, allowRoot)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", path, err)
	}
	return fullPath, nil
}

// hostKeyCallback returns the appropriate SSH host key callback based on config.
// Priority: known_hosts_file → host_key fingerprint → insecure (backward compat).
func (s *SFTPAdapter) hostKeyCallback() ssh.HostKeyCallback {
	// Option 1: Use a known_hosts file (OpenSSH format).
	if s.config.KnownHostsFile != "" {
		cb, err := knownhosts.New(s.config.KnownHostsFile)
		if err == nil {
			return cb
		}
		// Log the load error and fall through to the next option.
		log.Printf("Warning: SFTP could not load known_hosts_file %q for host %s: %v. "+
			"Falling back to insecure host key acceptance — configure a valid known_hosts_file or host_key to prevent MITM attacks.",
			s.config.KnownHostsFile, s.config.Host, err)
	}

	// Option 2: Verify against a SHA-256 fingerprint of the host key.
	if s.config.HostKey != "" {
		expected := strings.TrimPrefix(s.config.HostKey, "SHA256:")
		return func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hash := sha256.Sum256(key.Marshal())
			actual := hex.EncodeToString(hash[:])
			if !strings.EqualFold(actual, expected) {
				return fmt.Errorf("host key mismatch: expected %s, got %s", expected, actual)
			}
			return nil
		}
	}

	// Fallback: accept any key (backward compatibility with existing configs).
	log.Printf("Warning: SFTP connection to %s has no host key verification configured. "+
		"Set host_key or known_hosts_file in storage config to prevent MITM attacks.", s.config.Host)
	return ssh.InsecureIgnoreHostKey() // #nosec G106 //nolint:gosec // user chose not to configure host key
}

func (s *SFTPAdapter) Write(path string, reader io.Reader) error {
	client, err := s.connect()
	if err != nil {
		return err
	}
	defer client.Close()

	full, err := s.fullPath(path, false)
	if err != nil {
		return err
	}
	if err := client.MkdirAll(filepath.Dir(full)); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	f, err := client.Create(full)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	if _, err := io.Copy(f, reader); err != nil {
		_ = f.Close()
		_ = client.Remove(full)
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = client.Remove(full)
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

func (s *SFTPAdapter) Read(path string) (io.ReadCloser, error) {
	client, err := s.connect()
	if err != nil {
		return nil, err
	}
	// Note: caller must close the returned ReadCloser. We wrap to also close the sftp client.
	fullPath, err := s.fullPath(path, false)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	f, err := client.Open(fullPath)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &sftpReadCloser{file: f, client: client}, nil
}

type sftpReadCloser struct {
	file   *sftp.File
	client *sftp.Client
}

func (r *sftpReadCloser) Read(p []byte) (int, error) { return r.file.Read(p) }
func (r *sftpReadCloser) Close() error {
	fileErr := r.file.Close()
	clientErr := r.client.Close()
	if fileErr != nil && clientErr != nil {
		return errors.Join(fileErr, clientErr)
	}
	if fileErr != nil {
		return fileErr
	}
	return clientErr
}

func (s *SFTPAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range offset=%d length=%d", offset, length)
	}
	client, err := s.connect()
	if err != nil {
		return nil, err
	}
	fullPath, err := s.fullPath(p, false)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	info, err := client.Stat(fullPath)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if offset >= info.Size() {
		_ = client.Close()
		return nil, fmt.Errorf("offset %d at or past EOF (size=%d)", offset, info.Size())
	}
	f, err := client.Open(fullPath)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		_ = client.Close()
		return nil, err
	}
	rc := &sftpReadCloser{file: f, client: client}
	return &rangeReader{Reader: io.LimitReader(rc, length), closer: rc}, nil
}

func (s *SFTPAdapter) Delete(path string) error {
	client, err := s.connect()
	if err != nil {
		return err
	}
	defer client.Close()
	fullPath, err := s.fullPath(path, false)
	if err != nil {
		return err
	}
	return client.Remove(fullPath)
}

func (s *SFTPAdapter) List(prefix string) ([]FileInfo, error) {
	client, err := s.connect()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fullPath, err := s.fullPath(prefix, true)
	if err != nil {
		return nil, err
	}
	entries, err := client.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	var files []FileInfo
	for _, e := range entries {
		files = append(files, FileInfo{
			Path:    filepath.Join(prefix, e.Name()),
			Size:    e.Size(),
			ModTime: e.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	return files, nil
}

func (s *SFTPAdapter) Stat(path string) (FileInfo, error) {
	client, err := s.connect()
	if err != nil {
		return FileInfo{}, err
	}
	defer client.Close()

	fullPath, err := s.fullPath(path, false)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := client.Stat(fullPath)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (s *SFTPAdapter) TestConnection() error {
	client, err := s.connect()
	if err != nil {
		return err
	}
	defer client.Close()

	_, err = client.ReadDir(s.config.BasePath)
	return err
}

// GetCapacity issues an SFTP statvfs@openssh.com call. Servers that
// don't advertise the extension (some Cisco / SonicWall / hardened
// SSH deployments) return an error; we surface that as a zero-Total
// Capacity with Source="sftp-statvfs" rather than failing the whole
// probe — capacity is informational, not a liveness signal.
//
// Context cancellation is checked before dialling so a cancelled
// caller (e.g. a 60s scheduler probe that has timed out) does not
// pay for the SSH handshake. pkg/sftp's StatVFS does not take a
// context; the deadline lives at the dial layer.
func (s *SFTPAdapter) GetCapacity(ctx context.Context) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	client, err := s.connect()
	if err != nil {
		return Capacity{}, fmt.Errorf("sftp: dial: %w", err)
	}
	defer client.Close()

	probedAt := time.Now().UTC()
	st, err := client.StatVFS(s.basePathOrRoot())
	if err != nil {
		// statvfs@openssh.com unsupported by this server. Log once at
		// the daemon and return a zero-Total Capacity so the UI shows
		// "no quota reported" rather than a hard failure.
		log.Printf("sftp: statvfs unsupported on %s: %v", s.config.Host, err)
		return Capacity{ProbedAt: probedAt, Source: "sftp-statvfs"}, nil
	}
	cap, err := sftpStatVFSToCapacity(st, probedAt)
	if err != nil {
		return Capacity{}, fmt.Errorf("sftp: convert statvfs: %w", err)
	}
	return cap, nil
}

// basePathOrRoot returns BasePath as an absolute SFTP path, or "/" when
// BasePath is empty. StatVFS must be called against a real directory
// the user can stat.
func (s *SFTPAdapter) basePathOrRoot() string {
	if p := strings.Trim(s.config.BasePath, "/"); p != "" {
		return "/" + p
	}
	return "/"
}

// sftpStatVFSToCapacity converts a pkg/sftp StatVFS struct into a
// Capacity using Frsize (the fundamental block size — matches what df
// and statvfs(2) report). Extracted from GetCapacity so the conversion
// can be unit-tested without a live SFTP server. Returns an error if
// Frsize is 0 (genuinely malformed response — every real server reports
// a non-zero block size).
func sftpStatVFSToCapacity(st *sftp.StatVFS, probedAt time.Time) (Capacity, error) {
	if st == nil {
		return Capacity{}, fmt.Errorf("nil StatVFS")
	}
	if st.Frsize == 0 {
		return Capacity{}, fmt.Errorf("frsize is 0 — malformed StatVFS response")
	}
	bsize := int64(st.Frsize)         //nolint:gosec,unconvert // Frsize varies by platform; cast is required on Darwin, redundant on Linux
	total := int64(st.Blocks) * bsize //nolint:gosec,unconvert
	free := int64(st.Bavail) * bsize  //nolint:gosec,unconvert
	used := total - free
	if used < 0 {
		used = 0
	}
	return Capacity{
		TotalBytes: total,
		UsedBytes:  used,
		FreeBytes:  free,
		ProbedAt:   probedAt,
		Source:     "sftp-statvfs",
	}, nil
}

// Usage attempts the SFTP statvfs@openssh.com extension. When the server
// supports it, free and total are computed from the Bavail and Blocks
// fields using Frsize as the block size. When the server doesn't advertise
// the extension, or if dialling fails, ErrUsageNotSupported is returned so
// callers can degrade gracefully.
func (s *SFTPAdapter) Usage() (free, total int64, err error) {
	client, err := s.connect()
	if err != nil {
		return 0, 0, ErrUsageNotSupported
	}
	defer client.Close()

	st, err := client.StatVFS(s.basePathOrRoot())
	if err != nil {
		// Server does not support the statvfs@openssh.com extension.
		return 0, 0, ErrUsageNotSupported
	}
	if st.Frsize == 0 {
		return 0, 0, ErrUsageNotSupported
	}
	bsize := int64(st.Frsize)        //nolint:gosec,unconvert
	total = int64(st.Blocks) * bsize //nolint:gosec,unconvert
	free = int64(st.Bavail) * bsize  //nolint:gosec,unconvert
	if free > total {
		free = total
	}
	return free, total, nil
}

var _ Adapter = (*SFTPAdapter)(nil)
