package storage

import (
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
	defer f.Close()

	_, err = io.Copy(f, reader)
	return err
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

var _ Adapter = (*SFTPAdapter)(nil)
