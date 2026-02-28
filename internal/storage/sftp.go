package storage

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	KeyFile  string `json:"key_file"`
	BasePath string `json:"base_path"`
}

type SFTPAdapter struct {
	config SFTPConfig
}

func NewSFTPAdapter(config SFTPConfig) (*SFTPAdapter, error) {
	if config.Port == 0 {
		config.Port = 22
	}
	return &SFTPAdapter{config: config}, nil
}

func (s *SFTPAdapter) connect() (*sftp.Client, error) {
	var authMethods []ssh.AuthMethod
	if s.config.Password != "" {
		authMethods = append(authMethods, ssh.Password(s.config.Password))
	}
	if s.config.KeyFile != "" {
		key, err := os.ReadFile(s.config.KeyFile)
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
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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

func (s *SFTPAdapter) fullPath(path string) string {
	return filepath.Join(s.config.BasePath, filepath.Clean(path))
}

func (s *SFTPAdapter) Write(path string, reader io.Reader) error {
	client, err := s.connect()
	if err != nil {
		return err
	}
	defer client.Close()

	full := s.fullPath(path)
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
	f, err := client.Open(s.fullPath(path))
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
	r.file.Close()
	return r.client.Close()
}

func (s *SFTPAdapter) Delete(path string) error {
	client, err := s.connect()
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Remove(s.fullPath(path))
}

func (s *SFTPAdapter) List(prefix string) ([]FileInfo, error) {
	client, err := s.connect()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	entries, err := client.ReadDir(s.fullPath(prefix))
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

	info, err := client.Stat(s.fullPath(path))
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
