package storage

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/cloudsoda/go-smb2"
	"github.com/ruaan-deysel/vault/internal/safepath"
)

type SMBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Share    string `json:"share"`
	BasePath string `json:"base_path"`
}

type SMBAdapter struct {
	config SMBConfig
}

func NewSMBAdapter(config SMBConfig) (*SMBAdapter, error) {
	if config.Port == 0 {
		config.Port = 445
	}
	return &SMBAdapter{config: config}, nil
}

func (s *SMBAdapter) connect() (*smb2.Share, *smb2.Session, error) {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     s.config.User,
			Password: s.config.Password,
		},
	}

	session, err := d.Dial(context.Background(), addr)
	if err != nil {
		return nil, nil, fmt.Errorf("smb dial: %w", err)
	}

	share, err := session.Mount(s.config.Share)
	if err != nil {
		session.Logoff()
		return nil, nil, fmt.Errorf("mount share: %w", err)
	}

	return share, session, nil
}

func (s *SMBAdapter) fullPath(path string, allowRoot bool) (string, error) {
	fullPath, err := safepath.JoinUnderBase(s.config.BasePath, path, allowRoot)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", path, err)
	}
	return fullPath, nil
}

func (s *SMBAdapter) Write(path string, reader io.Reader) error {
	share, session, err := s.connect()
	if err != nil {
		return err
	}
	defer session.Logoff()
	defer share.Umount()

	full, err := s.fullPath(path, false)
	if err != nil {
		return err
	}
	if err := share.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	f, err := share.Create(full)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(f, reader)
	return err
}

func (s *SMBAdapter) Read(path string) (io.ReadCloser, error) {
	share, session, err := s.connect()
	if err != nil {
		return nil, err
	}

	fullPath, err := s.fullPath(path, false)
	if err != nil {
		share.Umount()
		session.Logoff()
		return nil, err
	}
	f, err := share.Open(fullPath)
	if err != nil {
		share.Umount()
		session.Logoff()
		return nil, err
	}
	return &smbReadCloser{file: f, share: share, session: session}, nil
}

type smbReadCloser struct {
	file    *smb2.File
	share   *smb2.Share
	session *smb2.Session
}

func (r *smbReadCloser) Read(p []byte) (int, error) { return r.file.Read(p) }
func (r *smbReadCloser) Close() error {
	r.file.Close()
	r.share.Umount()
	return r.session.Logoff()
}

func (s *SMBAdapter) Delete(path string) error {
	share, session, err := s.connect()
	if err != nil {
		return err
	}
	defer session.Logoff()
	defer share.Umount()
	fullPath, err := s.fullPath(path, false)
	if err != nil {
		return err
	}
	return share.Remove(fullPath)
}

func (s *SMBAdapter) List(prefix string) ([]FileInfo, error) {
	share, session, err := s.connect()
	if err != nil {
		return nil, err
	}
	defer session.Logoff()
	defer share.Umount()

	fullPath, err := s.fullPath(prefix, true)
	if err != nil {
		return nil, err
	}
	entries, err := share.ReadDir(fullPath)
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

func (s *SMBAdapter) Stat(path string) (FileInfo, error) {
	share, session, err := s.connect()
	if err != nil {
		return FileInfo{}, err
	}
	defer session.Logoff()
	defer share.Umount()

	fullPath, err := s.fullPath(path, false)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := share.Stat(fullPath)
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

func (s *SMBAdapter) TestConnection() error {
	share, session, err := s.connect()
	if err != nil {
		return err
	}
	defer session.Logoff()
	defer share.Umount()

	_, err = share.ReadDir(s.config.BasePath)
	return err
}

var _ Adapter = (*SMBAdapter)(nil)
