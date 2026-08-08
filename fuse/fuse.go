// Package taildrive_fuse exposes a remote filesystem as a local POSIX mount.
//
// Nothing in the FUSE layer knows how bytes reach the peer. Every operation
// goes through Backend, a small path-based interface, so each transfer
// protocol implements Backend once and inherits the whole mount. An SFTP
// backend wraps *sftp.Client; a WebDAV backend wraps an HTTP client; OSBackend
// at the bottom of this file wraps the local disk and doubles as the reference
// for what an adapter has to provide.
//
// Paths handed to a Backend are always slash-separated and rooted at "/", the
// same convention vfs.resolve uses on the serving side.
//
// Presenting several peers under one mountpoint is a Backend that dispatches
// on the first path segment, not something this file does.
//
// Deliberately absent: symlinks, hardlinks, xattrs, statfs, and persistent
// chmod/chown. Setattr accepts and drops mode and time changes because
// refusing them breaks ordinary tools, but truncation is honored.
package taildrive_fuse

import (
	"context"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Entry is one stat result. Only the directory bit and the permission bits of
// Mode are read; a protocol that cannot report a mode should still set
// os.ModeDir correctly and leave the permissions at 0.
type Entry struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
}

// Backend is the protocol-agnostic filesystem every protocol maps onto.
//
// Implementations must be safe for concurrent use: the kernel issues FUSE
// requests in parallel. Errors should be os.ErrNotExist, os.ErrPermission,
// os.ErrExist or a syscall.Errno where possible, since fs.ToErrno translates
// those into the right errno and anything else becomes EIO.
type Backend interface {
	Stat(ctx context.Context, p string) (Entry, error)
	ReadDir(ctx context.Context, p string) ([]Entry, error)

	// Open opens p with os.O_* flags, creating it with perm when the flags
	// ask for it. Protocols without a create mode may ignore perm.
	Open(ctx context.Context, p string, flags int, perm os.FileMode) (File, error)

	Mkdir(ctx context.Context, p string, perm os.FileMode) error

	// Remove deletes a file or an empty directory; FUSE unlink and rmdir both
	// land here.
	Remove(ctx context.Context, p string) error

	Rename(ctx context.Context, oldPath, newPath string) error
	Truncate(ctx context.Context, p string, size int64) error

	// Close releases the underlying connection. Called once, after unmount.
	Close() error
}

// File is an open remote file. The kernel always supplies absolute offsets,
// so a backend never has to track a cursor.
type File interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
}

// Options tunes a single mount.
type Options struct {
	// Name identifies the mount in /proc/mounts and in `df` output.
	Name string

	// ReadOnly mounts with "ro" so the kernel rejects writes before they
	// reach the backend. Mirrors Share.ReadOnly.
	ReadOnly bool

	// Debug logs the raw FUSE traffic.
	Debug bool

	// AttrTimeout and EntryTimeout are how long the kernel may cache stat
	// results and name lookups. Both default to a second: long enough that
	// `ls -l` is not a round trip per file, short enough that a peer's
	// changes show up on their own.
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

// mount is the state every node in one mount shares.
type mount struct {
	backend  Backend
	uid, gid uint32
}

// Mount mounts backend at mountpoint and blocks until ctx is cancelled or the
// filesystem is unmounted from outside. The backend is closed on the way out.
func Mount(ctx context.Context, mountpoint string, backend Backend, opts Options) error {
	opts.applyDefaults()

	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return err
	}

	m := &mount{
		backend: backend,
		uid:     uint32(os.Getuid()),
		gid:     uint32(os.Getgid()),
	}

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

	// Unblocks the Wait below when the registry cancels us. A busy mountpoint
	// makes Unmount fail, and the mount then outlives the process.
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

	if err := backend.Close(); err != nil {
		log.Printf("fuse: closing backend: %s", err)
	}

	log.Printf("fuse: unmounted %s", mountpoint)
	return ctx.Err()
}

// Run mounts the default backend. The signature matches the runner the api
// registry expects, so FUSE starts and stops like any other protocol.
func Run(ctx context.Context) error {
	mountpoint, backend := defaultMount()
	return Mount(ctx, mountpoint, backend, Options{})
}

// defaultMount is placeholder wiring, the counterpart to sftp.mountsFor:
// swap the OS backend for the negotiated protocol client once peer selection
// picks one.
func defaultMount() (string, Backend) {
	return filepath.Join(os.Getenv("HOME"), "taildrive"), NewOSBackend("/tmp")
}

// node is one file or directory. It stores no path of its own; go-fuse tracks
// the tree, so a rename moves every descendant for free.
type node struct {
	fs.Inode
	mount *mount
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

func (n *node) path() string { return "/" + n.Path(n.Root()) }

func (n *node) childPath(name string) string { return path.Join(n.path(), name) }

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	e, err := n.mount.backend.Stat(ctx, n.childPath(name))
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	child := n.NewInode(ctx, &node{mount: n.mount}, fs.StableAttr{Mode: fileType(e.Mode)})
	n.mount.setAttr(e, &out.Attr)
	return child, 0
}

func (n *node) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	e, err := n.mount.backend.Stat(ctx, n.path())
	if err != nil {
		return fs.ToErrno(err)
	}

	n.mount.setAttr(e, &out.Attr)
	return 0
}

func (n *node) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if size, ok := in.GetSize(); ok {
		if err := n.mount.backend.Truncate(ctx, n.path(), int64(size)); err != nil {
			return fs.ToErrno(err)
		}
	}

	// Mode, owner and timestamps are accepted and dropped: not every protocol
	// can express them, and reporting EPERM makes cp and editors fail.
	return n.Getattr(ctx, fh, out)
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.mount.backend.ReadDir(ctx, n.path())
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
	f, err := n.mount.backend.Open(ctx, n.path(), openFlags(flags), 0)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}

	return &handle{file: f}, 0, 0
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	perm := os.FileMode(mode) & os.ModePerm

	f, err := n.mount.backend.Open(ctx, n.childPath(name), openFlags(flags)|os.O_CREATE, perm)
	if err != nil {
		return nil, nil, 0, fs.ToErrno(err)
	}

	// Synthesised rather than stat'ed: the file is empty and the attribute
	// timeout corrects anything the backend disagreed about.
	child := n.NewInode(ctx, &node{mount: n.mount}, fs.StableAttr{Mode: syscall.S_IFREG})
	n.mount.setAttr(Entry{Name: name, Mode: perm, ModTime: time.Now()}, &out.Attr)

	return child, &handle{file: f}, 0, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	perm := os.FileMode(mode) & os.ModePerm

	if err := n.mount.backend.Mkdir(ctx, n.childPath(name), perm); err != nil {
		return nil, fs.ToErrno(err)
	}

	child := n.NewInode(ctx, &node{mount: n.mount}, fs.StableAttr{Mode: syscall.S_IFDIR})
	n.mount.setAttr(Entry{Name: name, Mode: os.ModeDir | perm, ModTime: time.Now()}, &out.Attr)

	return child, 0
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	if err := n.mount.backend.Remove(ctx, n.childPath(name)); err != nil {
		return fs.ToErrno(err)
	}
	return 0
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if err := n.mount.backend.Remove(ctx, n.childPath(name)); err != nil {
		return fs.ToErrno(err)
	}
	return 0
}

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	// RENAME_EXCHANGE and RENAME_NOREPLACE have no portable equivalent.
	if flags != 0 {
		return syscall.EINVAL
	}

	target, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}

	if err := n.mount.backend.Rename(ctx, n.childPath(name), target.childPath(newName)); err != nil {
		return fs.ToErrno(err)
	}
	return 0
}

// handle is one open file. The mutex is here because the kernel dispatches
// reads and writes on the same handle concurrently and few protocol clients
// promise that is safe.
type handle struct {
	mu   sync.Mutex
	file File
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
		return uint32(n), fs.ToErrno(err)
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

func (m *mount) setAttr(e Entry, out *fuse.Attr) {
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

// fileType reduces a Go mode to the S_IF* bits FUSE indexes nodes by. Anything
// that is not a directory is presented as a regular file.
func fileType(mode os.FileMode) uint32 {
	if mode.IsDir() {
		return syscall.S_IFDIR
	}
	return syscall.S_IFREG
}

// openFlags converts kernel open flags for the backend. O_APPEND is stripped
// because the kernel already resolves appends into absolute offsets, and
// honouring it a second time would write past the end.
func openFlags(flags uint32) int {
	return int(flags) &^ syscall.O_APPEND
}

// OSBackend serves a local directory. It exists so the mount can be exercised
// end to end before any protocol client is wired up, and as the shortest
// example of what an adapter owes the interface.
type OSBackend struct {
	root string
}

var _ Backend = (*OSBackend)(nil)

func NewOSBackend(root string) *OSBackend {
	abs, _ := filepath.Abs(root)
	return &OSBackend{root: abs}
}

// real maps a virtual path onto disk, refusing anything that escapes the root.
func (b *OSBackend) real(p string) (string, error) {
	clean := path.Clean("/" + strings.TrimPrefix(p, "/"))
	real := filepath.Join(b.root, filepath.FromSlash(clean))

	if real != b.root && !strings.HasPrefix(real, b.root+string(os.PathSeparator)) {
		return "", os.ErrPermission
	}

	return real, nil
}

func (b *OSBackend) Stat(ctx context.Context, p string) (Entry, error) {
	real, err := b.real(p)
	if err != nil {
		return Entry{}, err
	}

	info, err := os.Stat(real)
	if err != nil {
		return Entry{}, err
	}

	return entryFrom(info), nil
}

func (b *OSBackend) ReadDir(ctx context.Context, p string) ([]Entry, error) {
	real, err := b.real(p)
	if err != nil {
		return nil, err
	}

	dirents, err := os.ReadDir(real)
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		info, err := d.Info()
		if err != nil {
			continue
		}
		entries = append(entries, entryFrom(info))
	}

	return entries, nil
}

func (b *OSBackend) Open(ctx context.Context, p string, flags int, perm os.FileMode) (File, error) {
	real, err := b.real(p)
	if err != nil {
		return nil, err
	}

	if perm == 0 {
		perm = 0o644
	}

	return os.OpenFile(real, flags, perm)
}

func (b *OSBackend) Mkdir(ctx context.Context, p string, perm os.FileMode) error {
	real, err := b.real(p)
	if err != nil {
		return err
	}

	if perm == 0 {
		perm = 0o755
	}

	return os.Mkdir(real, perm)
}

func (b *OSBackend) Remove(ctx context.Context, p string) error {
	real, err := b.real(p)
	if err != nil {
		return err
	}

	return os.Remove(real)
}

func (b *OSBackend) Rename(ctx context.Context, oldPath, newPath string) error {
	oldReal, err := b.real(oldPath)
	if err != nil {
		return err
	}

	newReal, err := b.real(newPath)
	if err != nil {
		return err
	}

	return os.Rename(oldReal, newReal)
}

func (b *OSBackend) Truncate(ctx context.Context, p string, size int64) error {
	real, err := b.real(p)
	if err != nil {
		return err
	}

	return os.Truncate(real, size)
}

func (b *OSBackend) Close() error { return nil }

func entryFrom(info os.FileInfo) Entry {
	return Entry{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
	}
}
