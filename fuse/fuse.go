package fuse

import (
	"context"
	"io"
	"log"
	"os"
	"path"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"a3l6/m/vfs"
)

type Options struct {
	Name         string `json:"name"`
	ReadOnly     bool   `json:"readonly"`
	Debug        bool
	AttrTimeout  time.Duration
	EntryTimeout time.Duration
}

func (o *Options) applyDefaults() {
	if o.Name == "" {
		o.Name = "taildrive"
	}

	if o.AttrTimeout == 0 {
		o.AttrTimeout = time.Second
	}

	if o.EntryTimeout == 0 {
		o.EntryTimeout = time.Second
	}
}

type mount struct {
	fs       vfs.FS
	uid, gid uint32
}

type node struct {
	fs.Inode
	mount *mount
}

func Mount(ctx context.Context, mountpoint string, fsys vfs.FS, opts Options) error {
	opts.applyDefaults()

	if err := os.Mkdir(mountpoint, 0o755); err != nil {
		return err
	}

	m := &mount{fs: fsys, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}

	mountOpts := fuse.MountOptions{
		FsName:        opts.Name,
		Name:          "taildrive",
		Debug:         opts.Debug,
		DisableXAttrs: true,
	}

	if opts.ReadOnly {
		mountOpts.Options = append(mountOpts.Options, "ro")
	}

	server, err := fs.Mount(mountpoint, &node{mount: m}, &fs.Options{
		MountOptions: mountOpts,
		AttrTimeout:  &opts.AttrTimeout,
		EntryTimeout: &opts.EntryTimeout,
	})

	if err != nil {
		return err
	}

	log.Printf("fuse: mounted %s at %s", opts.Name, mountpoint)

	served := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			if err := server.Unmount(); err != nil {
				log.Printf("fuse: unmount %s: %s (still mounted, try fusermount -u %s)", mountpoint, err, mountpoint)
			}
		case <-served:
		}
	}()

	server.Wait()
	close(served)

	log.Printf("fuse: unmounted %s", mountpoint)
	return ctx.Err()
}

var (
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeSetattrer = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeCreater   = (*node)(nil)
	_ fs.NodeMkdirer   = (*node)(nil)
	_ fs.NodeUnlinker  = (*node)(nil)
	_ fs.NodeRmdirer   = (*node)(nil)
	_ fs.NodeRenamer   = (*node)(nil)
)

func (n *node) path() string                 { return "/" + n.Path(n.Root()) }
func (n *node) childPath(name string) string { return path.Join(n.path(), name) }

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	e, err := n.mount.fs.Stat(ctx, n.childPath(name))
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	child := n.NewInode(ctx, &node{mount: n.mount}, fs.StableAttr{Mode: fileType(e.Mode)})

	n.mount.setAttr(e, &out.Attr)
	return child, 0
}

func (n *node) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	e, err := n.mount.fs.Stat(ctx, n.path())
	if err != nil {
		return fs.ToErrno(err)
	}
	n.mount.setAttr(e, &out.Attr)
	return 0
}

// TODO: Look into implementing this properly with all proper attrs handled
func (n *node) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if size, ok := in.GetSize(); ok {
		if err := n.mount.fs.Truncate(ctx, n.path(), int64(size)); err != nil {
			return fs.ToErrno(err)
		}
	}

	return n.Getattr(ctx, fh, out)
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.mount.fs.ReadDir(ctx, n.path())
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	out := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, fuse.DirEntry{Name: e.Name, Mode: fileType(e.Mode)})
	}

	return fs.NewListDirStream(out), 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	f, err := n.mount.fs.Open(ctx, n.path(), openFlags(flags), 0)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	return &handle{file: f}, 0, 0
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	perm := os.FileMode(mode) & os.ModePerm

	f, err := n.mount.fs.Open(ctx, n.childPath(name), openFlags(flags)|os.O_CREATE, perm)
	if err != nil {
		return nil, nil, 0, fs.ToErrno(err)
	}

	child := n.NewInode(ctx, &node{mount: n.mount}, fs.StableAttr{Mode: syscall.S_IFREG})

	n.mount.setAttr(vfs.Entry{Name: name, Mode: perm, ModTime: time.Now()}, &out.Attr)

	return child, &handle{file: f}, 0, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	perm := os.FileMode(mode) & os.ModePerm

	if err := n.mount.fs.Mkdir(ctx, n.childPath(name), perm); err != nil {
		return nil, fs.ToErrno(err)
	}

	child := n.NewInode(ctx, &node{mount: n.mount}, fs.StableAttr{Mode: syscall.S_IFDIR})

	n.mount.setAttr(vfs.Entry{Name: name, Mode: os.ModeDir | perm, ModTime: time.Now()}, &out.Attr)

	return child, 0
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	if err := n.mount.fs.Remove(ctx, n.childPath(name)); err != nil {
		return fs.ToErrno(err)
	}
	return 0
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if err := n.mount.fs.Remove(ctx, n.childPath(name)); err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if flags != 0 {
		return syscall.EINVAL
	}

	target, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}

	if err := n.mount.fs.Rename(ctx, n.childPath(name), target.childPath(newName)); err != nil {
		return fs.ToErrno(err)
	}
	return 0
}

type handle struct {
	mu   sync.Mutex
	file vfs.File
}

var (
	_ fs.FileReader   = (*handle)(nil)
	_ fs.FileWriter   = (*handle)(nil)
	_ fs.FileReleaser = (*handle)(nil)
)

func (h *handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	n, err := h.file.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, fs.ToErrno(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *handle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	n, err := h.file.WriteAt(data, off)
	if err != nil {
		return 0, fs.ToErrno(err)
	}

	return uint32(n), 0
}

func (h *handle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.file == nil {
		return 0
	}

	err := h.file.Close()
	h.file = nil
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

func (m *mount) setAttr(e vfs.Entry, out *fuse.Attr) {
	out.Mode = fileType(e.Mode) | uint32(e.Mode.Perm())
	out.Size = uint64(e.Size)
	out.Blocks = uint64((e.Size + 511) / 512)
	out.Owner.Uid, out.Owner.Gid = m.uid, m.gid
	out.Nlink = 1

	if e.Mode.IsDir() {
		out.Nlink = 2
	}

	t := e.ModTime
	out.SetTimes(&t, &t, &t)
}

func fileType(mode os.FileMode) uint32 {
	if mode.IsDir() {
		return syscall.S_IFDIR
	}
	return syscall.S_IFREG
}

func openFlags(flags uint32) int {
	return int(flags) &^ syscall.O_APPEND
}
