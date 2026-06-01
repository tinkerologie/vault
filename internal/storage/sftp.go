package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// sftpPoolSize is the maximum number of concurrent SSH+SFTP connections
// the pool holds open per adapter instance.
const sftpPoolSize = 4

// sftpConnection holds both halves of a live SSH+SFTP session so both are
// closed together when the connection is discarded from the pool. The
// existing connect() path leaked the *ssh.Client because callers only closed
// the *sftp.Client; this struct fixes that.
type sftpConnection struct {
	ssh  *ssh.Client
	sftp *sftp.Client
}

// Close closes the SFTP session and the underlying SSH transport.
func (c *sftpConnection) Close() error {
	sErr := c.sftp.Close()
	if c.ssh != nil {
		_ = c.ssh.Close()
	}
	return sErr
}

type SFTPAdapter struct {
	config    SFTPConfig
	pool      *sftpPool
	baseCtx   context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

func NewSFTPAdapter(config SFTPConfig) (*SFTPAdapter, error) {
	if config.Port == 0 {
		config.Port = 22
	}
	// Backward compatibility: accept "path" as alias for "base_path".
	if config.BasePath == "" && config.Path != "" {
		config.BasePath = config.Path
	}
	a := &SFTPAdapter{config: config}
	a.baseCtx, a.cancel = context.WithCancel(context.Background())
	a.pool = newSFTPPool(sftpPoolSize, func() (sftpConn, error) {
		return a.dialConnection()
	})
	return a, nil
}

// Close drains the idle connection pool, closing every pooled ssh+sftp pair.
// CloseAdapter calls this when the adapter is no longer needed.
func (s *SFTPAdapter) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.pool.closeAll()
	})
	return nil
}

// dialConnection opens a fresh SSH transport and SFTP session, returning a
// combined sftpConnection that owns both. Both halves are closed together when
// the connection is discarded from the pool, preventing the ssh.Client TCP
// leak that occurred when callers only closed the *sftp.Client.
func (s *SFTPAdapter) dialConnection() (*sftpConnection, error) {
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
	sshConn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}

	sftpClient, err := sftp.NewClient(sshConn)
	if err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}
	return &sftpConnection{ssh: sshConn, sftp: sftpClient}, nil
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

func (s *SFTPAdapter) Write(path string, reader io.Reader) (retErr error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return err
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp

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

func (s *SFTPAdapter) WriteFrom(path string, open func() (io.ReadCloser, error)) error {
	return streamWriteFrom(s, path, open)
}

// Read opens path for reading. The returned ReadCloser's lifetime extends past
// this function's return (the caller streams from it), so we cannot use a
// simple defer-put pattern here. Instead the connection is returned to the pool
// when the caller closes the ReadCloser. A nil op-error is passed on close
// because a successful read-to-EOF is not a reason to discard the connection.
func (s *SFTPAdapter) Read(path string) (io.ReadCloser, error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return nil, err
	}
	client := conn.(*sftpConnection).sftp
	fullPath, err := s.fullPath(path, false)
	if err != nil {
		s.pool.put(conn, err)
		return nil, err
	}
	f, err := client.Open(fullPath)
	if err != nil {
		s.pool.put(conn, err)
		return nil, err
	}
	return &sftpReadCloser{file: f, pool: s.pool, conn: conn}, nil
}

// sftpReadCloser wraps an open remote file and returns the pooled connection
// when the caller closes the reader. Using the pool field (rather than storing
// the *sftp.Client directly) lets the close correctly signal op-success so the
// connection is reused.
type sftpReadCloser struct {
	file *sftp.File
	pool *sftpPool
	conn sftpConn
}

func (r *sftpReadCloser) Read(p []byte) (int, error) { return r.file.Read(p) }
func (r *sftpReadCloser) Close() error {
	fileErr := r.file.Close()
	// Return the connection to the pool. We treat a file-close error as a
	// connection-level error to be safe (the remote state is uncertain).
	r.pool.put(r.conn, fileErr)
	return fileErr
}

// ReadRange opens a length-limited slice of path starting at offset. Like
// Read, the returned ReadCloser's lifetime extends past this function's return,
// so the pool connection is returned only when the caller closes the reader.
func (s *SFTPAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range offset=%d length=%d", offset, length)
	}
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return nil, err
	}
	client := conn.(*sftpConnection).sftp
	fullPath, err := s.fullPath(p, false)
	if err != nil {
		s.pool.put(conn, err)
		return nil, err
	}
	info, err := client.Stat(fullPath)
	if err != nil {
		s.pool.put(conn, err)
		return nil, err
	}
	if offset >= info.Size() {
		s.pool.put(conn, nil) // not an op error — caller asked for out-of-range
		return nil, fmt.Errorf("offset %d at or past EOF (size=%d)", offset, info.Size())
	}
	f, err := client.Open(fullPath)
	if err != nil {
		s.pool.put(conn, err)
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		s.pool.put(conn, err)
		return nil, err
	}
	rc := &sftpReadCloser{file: f, pool: s.pool, conn: conn}
	return &rangeReader{Reader: io.LimitReader(rc, length), closer: rc}, nil
}

func (s *SFTPAdapter) Delete(path string) (retErr error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return err
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp
	fullPath, err := s.fullPath(path, false)
	if err != nil {
		return err
	}
	return client.Remove(fullPath)
}

func (s *SFTPAdapter) List(prefix string) (_ []FileInfo, retErr error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return nil, err
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp

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

func (s *SFTPAdapter) Stat(path string) (_ FileInfo, retErr error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return FileInfo{}, err
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp

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

func (s *SFTPAdapter) TestConnection() (retErr error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return err
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp

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
func (s *SFTPAdapter) GetCapacity(ctx context.Context) (_ Capacity, retErr error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return Capacity{}, fmt.Errorf("sftp: dial: %w", err)
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp

	probedAt := time.Now().UTC()
	st, err := client.StatVFS(s.basePathOrRoot())
	if err != nil {
		// statvfs@openssh.com unsupported by this server. Log once at
		// the daemon and return a zero-Total Capacity so the UI shows
		// "no quota reported" rather than a hard failure.
		log.Printf("sftp: statvfs unsupported on %s: %v", s.config.Host, err)
		// Not a connection-level error; return the conn healthy.
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
func (s *SFTPAdapter) Usage() (free, total int64, retErr error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return 0, 0, ErrUsageNotSupported
	}
	// statvfs errors are not connection-level faults; always return the conn
	// healthy so it can be reused for the next probe.
	defer func() { s.pool.put(conn, nil) }()
	client := conn.(*sftpConnection).sftp

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

// RemoveEmptyDir removes dir if it is empty. sftp.Client.RemoveDirectory wraps
// the SSH_FXP_RMDIR request, which the SFTP server rejects when the directory
// is not empty — that rejection is the desired guard for the cleanup sweep.
func (s *SFTPAdapter) RemoveEmptyDir(dir string) (retErr error) {
	conn, err := s.pool.get(s.baseCtx)
	if err != nil {
		return err
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp
	fullPath, err := s.fullPath(dir, false)
	if err != nil {
		return err
	}
	return client.RemoveDirectory(fullPath)
}

var _ Adapter = (*SFTPAdapter)(nil)
