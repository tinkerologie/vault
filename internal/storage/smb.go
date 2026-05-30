package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"

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
	Path     string `json:"path"` // Deprecated alias for BasePath; kept for backward compatibility.
}

type SMBAdapter struct {
	config SMBConfig
}

func NewSMBAdapter(config SMBConfig) (*SMBAdapter, error) {
	if config.Port == 0 {
		config.Port = 445
	}
	// Backward compatibility: accept "path" as alias for "base_path".
	if config.BasePath == "" && config.Path != "" {
		config.BasePath = config.Path
	}
	return &SMBAdapter{config: config}, nil
}

// smbDialTimeout is the maximum time allowed for dialling the SMB session.
const smbDialTimeout = 30 * time.Second

func (s *SMBAdapter) connect() (*smb2.Share, *smb2.Session, error) {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     s.config.User,
			Password: s.config.Password,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), smbDialTimeout)
	defer cancel()

	session, err := d.Dial(ctx, addr)
	if err != nil {
		return nil, nil, fmt.Errorf("smb dial: %w", err)
	}

	share, err := session.Mount(s.config.Share)
	if err != nil {
		_ = session.Logoff()
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

	if _, err := io.Copy(f, reader); err != nil {
		_ = f.Close()
		_ = share.Remove(full)
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = share.Remove(full)
		return fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		if removeErr := share.Remove(full); removeErr != nil {
			return fmt.Errorf("close: %w (cleanup remove failed: %v)", err, removeErr)
		}
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

func (s *SMBAdapter) Read(path string) (io.ReadCloser, error) {
	share, session, err := s.connect()
	if err != nil {
		return nil, err
	}

	fullPath, err := s.fullPath(path, false)
	if err != nil {
		_ = share.Umount()
		_ = session.Logoff()
		return nil, err
	}
	f, err := share.Open(fullPath)
	if err != nil {
		_ = share.Umount()
		_ = session.Logoff()
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
	var errs []error
	if err := r.file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close smb file: %w", err))
	}
	if err := r.share.Umount(); err != nil {
		errs = append(errs, fmt.Errorf("umount smb share: %w", err))
	}
	if err := r.session.Logoff(); err != nil {
		errs = append(errs, fmt.Errorf("logoff smb session: %w", err))
	}
	return errors.Join(errs...)
}

func (s *SMBAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range offset=%d length=%d", offset, length)
	}
	share, session, err := s.connect()
	if err != nil {
		return nil, err
	}

	fullPath, err := s.fullPath(p, false)
	if err != nil {
		_ = share.Umount()
		_ = session.Logoff()
		return nil, err
	}
	info, err := share.Stat(fullPath)
	if err != nil {
		_ = share.Umount()
		_ = session.Logoff()
		return nil, err
	}
	if offset >= info.Size() {
		_ = share.Umount()
		_ = session.Logoff()
		return nil, fmt.Errorf("offset %d at or past EOF (size=%d)", offset, info.Size())
	}
	f, err := share.Open(fullPath)
	if err != nil {
		_ = share.Umount()
		_ = session.Logoff()
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		_ = share.Umount()
		_ = session.Logoff()
		return nil, err
	}
	rc := &smbReadCloser{file: f, share: share, session: session}
	return &rangeReader{Reader: io.LimitReader(rc, length), closer: rc}, nil
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

// GetCapacity queries the SMB server's filesystem info via the
// FILE_FS_FULL_SIZE_INFORMATION request (FSCTL_QUERY_INFO with
// FileFsFullSizeInformation class per [MS-FSCC]). go-smb2's
// Share.Statfs wraps this. Servers that refuse the query (some
// Samba ACL configurations) cause Statfs to return an error;
// we surface that as a zero-Total Capacity with Source="smb-fsctl"
// rather than failing the whole probe — capacity is informational,
// not a liveness signal.
//
// Context cancellation is checked before connecting so a cancelled
// caller (e.g. a 60s scheduler probe that has timed out) doesn't
// pay for the SMB session negotiation.
func (s *SMBAdapter) GetCapacity(ctx context.Context) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	share, _, err := s.connect()
	if err != nil {
		return Capacity{}, fmt.Errorf("smb: connect: %w", err)
	}
	defer share.Umount()

	probedAt := time.Now().UTC()
	probePath := s.basePathOrShareRoot()
	info, err := share.Statfs(probePath)
	if err != nil {
		log.Printf("smb: statfs on %s unsupported or refused: %v", probePath, err)
		return Capacity{ProbedAt: probedAt, Source: "smb-fsctl"}, nil
	}
	cap, err := smbFileFsInfoToCapacity(info, probedAt)
	if err != nil {
		return Capacity{}, fmt.Errorf("smb: convert statfs: %w", err)
	}
	return cap, nil
}

// basePathOrShareRoot returns the BasePath as-is when configured, or
// "." (the share root) when empty. go-smb2 expects paths relative to
// the mounted share, with "." denoting the share root itself.
func (s *SMBAdapter) basePathOrShareRoot() string {
	if p := strings.Trim(s.config.BasePath, "/\\"); p != "" {
		return p
	}
	return "."
}

// smbFileFsInfoToCapacity converts a go-smb2 FileFsInfo into a Capacity
// using BlockSize × TotalBlockCount for total and BlockSize ×
// AvailableBlockCount for free (matches df semantics — Available is
// the non-root reserved fraction; FreeBlockCount includes root-only
// space). Extracted from GetCapacity for unit testability without a
// live SMB server.
func smbFileFsInfoToCapacity(info smb2.FileFsInfo, probedAt time.Time) (Capacity, error) {
	if info == nil {
		return Capacity{}, fmt.Errorf("nil FileFsInfo")
	}
	bsize := info.BlockSize()
	if bsize == 0 {
		return Capacity{}, fmt.Errorf("BlockSize is 0 — malformed FileFsInfo")
	}
	total := int64(info.TotalBlockCount() * bsize)    //nolint:gosec // SMB FsInfo values fit int64 in practice
	free := int64(info.AvailableBlockCount() * bsize) //nolint:gosec,unconvert
	used := total - free
	if used < 0 {
		used = 0
	}
	return Capacity{
		TotalBytes: total,
		UsedBytes:  used,
		FreeBytes:  free,
		ProbedAt:   probedAt,
		Source:     "smb-fsctl",
	}, nil
}

// Usage queries the SMB server's filesystem info via share.Statfs. When the
// server supports the FILE_FS_FULL_SIZE_INFORMATION request, free and total
// are computed from AvailableBlockCount and TotalBlockCount × BlockSize. When
// the server refuses the query (some Samba ACL configurations), or if
// connecting fails, ErrUsageNotSupported is returned.
func (s *SMBAdapter) Usage() (free, total int64, err error) {
	share, session, err := s.connect()
	if err != nil {
		return 0, 0, ErrUsageNotSupported
	}
	defer session.Logoff()
	defer share.Umount()

	info, err := share.Statfs(s.basePathOrShareRoot())
	if err != nil {
		return 0, 0, ErrUsageNotSupported
	}
	bsize := info.BlockSize()
	if bsize == 0 {
		return 0, 0, ErrUsageNotSupported
	}
	total = int64(info.TotalBlockCount() * bsize)    //nolint:gosec
	free = int64(info.AvailableBlockCount() * bsize) //nolint:gosec,unconvert
	return free, total, nil
}

var _ Adapter = (*SMBAdapter)(nil)
