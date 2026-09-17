//go:build linux

package mount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/safepath"
)

type fuseRoot struct {
	fs.Inode
	onActivity func()
}

var _ fs.NodeGetattrer = (*fuseRoot)(nil)
var _ fs.NodeAccesser = (*fuseRoot)(nil)
var _ fs.NodeOpendirer = (*fuseRoot)(nil)

func (r *fuseRoot) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if r.onActivity != nil {
		r.onActivity()
	}
	out.Mode = 0555 | syscall.S_IFDIR
	return fs.OK
}

func (r *fuseRoot) Access(ctx context.Context, mask uint32) syscall.Errno {
	if mask&2 != 0 {
		return syscall.EROFS
	}
	return fs.OK
}

func (r *fuseRoot) Opendir(ctx context.Context) syscall.Errno {
	if r.onActivity != nil {
		r.onActivity()
	}
	return fs.OK
}

type fuseDir struct {
	fs.Inode
	modTime    time.Time
	onActivity func()
}

var _ fs.NodeGetattrer = (*fuseDir)(nil)
var _ fs.NodeAccesser = (*fuseDir)(nil)
var _ fs.NodeOpendirer = (*fuseDir)(nil)

func (d *fuseDir) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if d.onActivity != nil {
		d.onActivity()
	}
	out.Mode = 0555 | syscall.S_IFDIR
	out.Mtime = uint64(d.modTime.Unix())
	out.Atime = out.Mtime
	out.Ctime = out.Mtime
	return fs.OK
}

func (d *fuseDir) Access(ctx context.Context, mask uint32) syscall.Errno {
	if mask&2 != 0 {
		return syscall.EROFS
	}
	return fs.OK
}

func (d *fuseDir) Opendir(ctx context.Context) syscall.Errno {
	if d.onActivity != nil {
		d.onActivity()
	}
	return fs.OK
}

type fuseFile struct {
	fs.Inode
	reader     *FileReader
	modTime    time.Time
	mode       uint32
	onActivity func()
}

var _ fs.NodeGetattrer = (*fuseFile)(nil)
var _ fs.NodeOpener = (*fuseFile)(nil)
var _ fs.NodeReader = (*fuseFile)(nil)
var _ fs.NodeAccesser = (*fuseFile)(nil)

func (f *fuseFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if f.onActivity != nil {
		f.onActivity()
	}
	m := (f.mode & 0777) | syscall.S_IFREG
	m &= ^uint32(0222) // read-only: clear write bits
	if m&0777 == 0 {
		m |= 0444
	}
	out.Mode = m
	out.Size = uint64(f.reader.Size())
	out.Mtime = uint64(f.modTime.Unix())
	out.Atime = out.Mtime
	out.Ctime = out.Mtime
	return fs.OK
}

func (f *fuseFile) Access(ctx context.Context, mask uint32) syscall.Errno {
	if mask&2 != 0 {
		return syscall.EROFS
	}
	return fs.OK
}

func (f *fuseFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if f.onActivity != nil {
		f.onActivity()
	}
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_APPEND|syscall.O_TRUNC) != 0 {
		return nil, 0, syscall.EROFS
	}
	return nil, 0, fs.OK
}

func (f *fuseFile) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if f.onActivity != nil {
		f.onActivity()
	}
	n, err := f.reader.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), fs.OK
}

func getOrCreateDir(ctx context.Context, parent *fs.Inode, dirName string, modTime time.Time, onActivity func()) *fs.Inode {
	if ch := parent.GetChild(dirName); ch != nil {
		return ch
	}
	dirNode := &fuseDir{modTime: modTime, onActivity: onActivity}
	ch := parent.NewPersistentInode(ctx, dirNode, fs.StableAttr{Mode: syscall.S_IFDIR})
	parent.AddChild(dirName, ch, true)
	return ch
}

func addDirToTree(ctx context.Context, parent *fs.Inode, relPath string, modTime time.Time, onActivity func()) *fs.Inode {
	cleaned, err := safepath.NormalizeRelative(relPath, false)
	if err != nil || cleaned == "" || cleaned == "." {
		return parent
	}
	parts := strings.Split(cleaned, "/")
	curr := parent
	for _, part := range parts {
		curr = getOrCreateDir(ctx, curr, part, modTime, onActivity)
	}
	return curr
}

func addFileToTree(ctx context.Context, parent *fs.Inode, relPath string, reader *FileReader, modTime time.Time, mode uint32, onActivity func()) {
	cleaned, err := safepath.NormalizeRelative(relPath, false)
	if err != nil || cleaned == "" || cleaned == "." {
		return
	}
	parts := strings.Split(cleaned, "/")
	curr := parent
	for i := 0; i < len(parts)-1; i++ {
		curr = getOrCreateDir(ctx, curr, parts[i], modTime, onActivity)
	}
	fileName := parts[len(parts)-1]
	fileNode := &fuseFile{
		reader:     reader,
		modTime:    modTime,
		mode:       mode,
		onActivity: onActivity,
	}
	ch := curr.NewPersistentInode(ctx, fileNode, fs.StableAttr{Mode: syscall.S_IFREG})
	curr.AddChild(fileName, ch, true)
}

// populateManifestTree maps manifest files and sub-manifests under rootDir.
func populateManifestTree(ctx context.Context, repo *dedup.Repo, cache *ChunkCache, rootDir *fs.Inode, m dedup.Manifest, onActivity func()) error {
	for key, entry := range m.Files {
		var modTime time.Time
		if entry.ModTime != "" {
			if t, err := time.Parse(time.RFC3339, entry.ModTime); err == nil {
				modTime = t
			}
		}
		if modTime.IsZero() {
			modTime = time.Now()
		}

		// Check for container volume mount points: __vol__<dest>
		if strings.HasPrefix(key, "__vol__") {
			dest := strings.TrimPrefix(key, "__vol__")
			dest = strings.TrimPrefix(dest, "/")
			volDir := addDirToTree(ctx, rootDir, dest, modTime, onActivity)
			if len(entry.Chunks) > 0 {
				subM, err := repo.GetManifest(entry.Chunks[0])
				if err != nil {
					return fmt.Errorf("mount: get sub-manifest for %s: %w", dest, err)
				}
				if err := populateManifestTree(ctx, repo, cache, volDir, subM, onActivity); err != nil {
					return err
				}
			}
			continue
		}

		// Check for container single-file bind mounts: __volfile__<dest>
		if strings.HasPrefix(key, "__volfile__") {
			dest := strings.TrimPrefix(key, "__volfile__")
			dest = strings.TrimPrefix(dest, "/")
			reader, err := NewFileReader(repo, entry, cache)
			if err != nil {
				return err
			}
			addFileToTree(ctx, rootDir, dest, reader, modTime, entry.Mode, onActivity)
			continue
		}

		// Ordinary directory or file
		if entry.IsDir {
			addDirToTree(ctx, rootDir, key, modTime, onActivity)
		} else {
			reader, err := NewFileReader(repo, entry, cache)
			if err != nil {
				return err
			}
			addFileToTree(ctx, rootDir, key, reader, modTime, entry.Mode, onActivity)
		}
	}
	return nil
}

type linuxMountHandle struct {
	server    *fuse.Server
	mountPath string
	done      chan struct{}
	once      sync.Once
}

func (h *linuxMountHandle) MountPath() string {
	return h.mountPath
}

func (h *linuxMountHandle) Unmount() error {
	var err error
	h.once.Do(func() {
		if h.server != nil {
			err = h.server.Unmount()
			if err != nil {
				// Best effort fallback via fusermount
				if uerr := exec.Command("fusermount3", "-u", h.mountPath).Run(); uerr == nil {
					err = nil
				} else if uerr := exec.Command("fusermount", "-u", h.mountPath).Run(); uerr == nil {
					err = nil
				}
			}
		}
		select {
		case <-h.done:
		case <-time.After(3 * time.Second):
		}
	})
	return err
}

func buildTree(ctx context.Context, repo *dedup.Repo, cache *ChunkCache, rootInode *fs.Inode, manifests map[string]dedup.Manifest, onActivity func()) error {
	if len(manifests) == 1 {
		for key, m := range manifests {
			if key == "" || key == "." {
				if err := populateManifestTree(ctx, repo, cache, rootInode, m, onActivity); err != nil {
					return err
				}
				break
			}
			// Single named item (e.g. container name)
			itemDir := getOrCreateDir(ctx, rootInode, key, time.Now(), onActivity)
			if err := populateManifestTree(ctx, repo, cache, itemDir, m, onActivity); err != nil {
				return err
			}
		}
	} else {
		// Multi-item backup (multiple containers / folders)
		for itemName, m := range manifests {
			itemDir := getOrCreateDir(ctx, rootInode, itemName, time.Now(), onActivity)
			if err := populateManifestTree(ctx, repo, cache, itemDir, m, onActivity); err != nil {
				return err
			}
		}
	}
	return nil
}

// MountManifests constructs a FUSE read-only tree and mounts it at mountPath.
func MountManifests(ctx context.Context, repo *dedup.Repo, manifests map[string]dedup.Manifest, mountPath string, onActivity func()) (MountHandle, error) {
	if err := os.MkdirAll(mountPath, 0755); err != nil {
		return nil, fmt.Errorf("mount: create mountpoint %s: %w", mountPath, err)
	}

	rootNode := &fuseRoot{onActivity: onActivity}
	cache := NewChunkCache(DefaultCacheSizeBytes)

	type buildState struct {
		once sync.Once
		err  error
		done chan struct{}
	}
	state := &buildState{done: make(chan struct{})}

	mountOpts := fuse.MountOptions{
		FsName:     "vault-backup",
		Name:       "vault",
		Options:    []string{"ro", "default_permissions"},
		AllowOther: true,
	}

	fsOpts := &fs.Options{
		MountOptions: mountOpts,
		OnAdd: func(ctx context.Context) {
			state.once.Do(func() {
				state.err = buildTree(ctx, repo, cache, &rootNode.Inode, manifests, onActivity)
				close(state.done)
			})
		},
	}

	server, err := fs.Mount(mountPath, rootNode, fsOpts)
	if err != nil {
		// If AllowOther fails (e.g. non-root without user_allow_other in /etc/fuse.conf), retry without AllowOther
		mountOpts.AllowOther = false
		fsOpts.MountOptions = mountOpts
		server, err = fs.Mount(mountPath, rootNode, fsOpts)
		if err != nil {
			return nil, fmt.Errorf("mount: fs.Mount on %s failed: %w", mountPath, err)
		}
	}

	if err := server.WaitMount(); err != nil {
		_ = server.Unmount()
		return nil, fmt.Errorf("mount: server.WaitMount on %s: %w", mountPath, err)
	}

	select {
	case <-state.done:
	case <-ctx.Done():
		_ = server.Unmount()
		return nil, fmt.Errorf("mount: context cancelled while building tree: %w", ctx.Err())
	}

	if state.err != nil {
		_ = server.Unmount()
		return nil, fmt.Errorf("mount: build tree: %w", state.err)
	}

	handle := &linuxMountHandle{
		server:    server,
		mountPath: mountPath,
		done:      make(chan struct{}),
	}

	go func() {
		server.Wait()
		close(handle.done)
	}()

	return handle, nil
}

func unmountPlatform(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	if uerr := exec.Command("fusermount3", "-u", path).Run(); uerr == nil {
		return nil
	}
	if uerr := exec.Command("fusermount", "-u", path).Run(); uerr == nil {
		return nil
	}
	err := syscall.Unmount(path, 0)
	if err == nil || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOENT) {
		return nil
	}
	return err
}
