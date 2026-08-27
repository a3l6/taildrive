package vfs

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// FS is the filesystem every protocol maps onto. Paths are slash separated and
// rooted at "/". Errors should be a syscall.Errno or wrap one, since that is
// the only form every protocol can translate.
type FS interface {
	Stat(ctx context.Context, p string) (Entry, error)
	ReadDir(ctx context.Context, p string) ([]Entry, error)
	Open(ctx context.Context, p string, flags int, perm os.FileMode) (File, error)
	Mkdir(ctx context.Context, p string, perm os.FileMode) error
	Remove(ctx context.Context, p string) error
	Rename(ctx context.Context, oldPath, newPath string) error
	Truncate(ctx context.Context, p string, size int64) error
}

// Offsets are absolute, so implementations never track a cursor of their own.
type File interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
}

type Entry struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
}

type Defaultvfs struct {
	mounts  map[string]string // cleaned virtual path -> absolute dir
	started time.Time
}

var _ FS = (*Defaultvfs)(nil)

func NewVFS(m map[string]string) *Defaultvfs {
	clean := make(map[string]string, len(m))
	for v, r := range m {
		vp := path.Clean("/" + strings.Trim(v, "/"))
		rp, _ := filepath.Abs(r)
		clean[vp] = rp
	}

	return &Defaultvfs{mounts: clean, started: time.Now()}
}

type resolved struct {
	clean     string
	real      string
	mountRoot string
	isVirtual bool
}

func (fs *Defaultvfs) resolve(virtual string) (resolved, error) {
	clean := path.Clean("/" + strings.TrimPrefix(virtual, "/"))

	best := ""
	for vp := range fs.mounts {
		if clean == vp || strings.HasPrefix(clean, vp+"/") {
			if len(vp) > len(best) {
				best = vp
			}
		}
	}

	if best != "" {
		sub := strings.TrimPrefix(strings.TrimPrefix(clean, best), "/")
		root := fs.mounts[best]
		real := filepath.Join(root, filepath.FromSlash(sub))

		if real != root && !strings.HasPrefix(real, root+string(os.PathSeparator)) {
			return resolved{}, syscall.EPERM
		}

		return resolved{clean: clean, real: real, mountRoot: root}, nil
	}

	if clean == "/" || fs.isAncestor(clean) {
		return resolved{clean: clean, isVirtual: true}, nil
	}

	return resolved{}, syscall.ENOENT
}

// writable resolves p and refuses the synthesised tree and the mount roots
// themselves, which no protocol may rename or delete.
func (fs *Defaultvfs) writable(p string) (resolved, error) {
	res, err := fs.resolve(p)
	if err != nil {
		return resolved{}, err
	}

	if res.isVirtual || res.real == res.mountRoot {
		return resolved{}, syscall.EPERM
	}

	return res, nil
}

func (fs *Defaultvfs) isAncestor(clean string) bool {
	prefix := clean + "/"

	for vp := range fs.mounts {
		if strings.HasPrefix(vp, prefix) {
			return true
		}
	}

	return false
}

func (fs *Defaultvfs) children(clean string) []Entry {
	prefix := "/"
	if clean != "/" {
		prefix = clean + "/"
	}

	seen := map[string]bool{}
	var out []Entry

	for vp := range fs.mounts {
		if !strings.HasPrefix(vp, prefix) {
			continue
		}

		child := strings.TrimPrefix(vp, prefix)
		if i := strings.IndexByte(child, '/'); i >= 0 {
			child = child[:i]
		}

		if child == "" || seen[child] {
			continue
		}

		seen[child] = true
		out = append(out, fs.virtualDir(child))
	}

	return out
}

func (fs *Defaultvfs) virtualDir(name string) Entry {
	return Entry{Name: name, Mode: os.ModeDir | 0o555, ModTime: fs.started}
}

func (fs *Defaultvfs) Stat(ctx context.Context, p string) (Entry, error) {
	res, err := fs.resolve(p)
	if err != nil {
		return Entry{}, err
	}

	if res.isVirtual {
		return fs.virtualDir(path.Base(res.clean)), nil
	}

	info, err := os.Stat(res.real)
	if err != nil {
		return Entry{}, err
	}

	return entryFrom(info), nil
}

func (fs *Defaultvfs) ReadDir(ctx context.Context, p string) ([]Entry, error) {
	res, err := fs.resolve(p)
	if err != nil {
		return nil, err
	}

	if res.isVirtual {
		return fs.children(res.clean), nil
	}

	dirents, err := os.ReadDir(res.real)
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

func (fs *Defaultvfs) Open(ctx context.Context, p string, flags int, perm os.FileMode) (File, error) {
	res, err := fs.resolve(p)
	if err != nil {
		return nil, err
	}

	if res.isVirtual {
		return nil, syscall.EPERM
	}

	if perm == 0 {
		perm = 0o644
	}

	return os.OpenFile(res.real, flags, perm)
}

func (fs *Defaultvfs) Mkdir(ctx context.Context, p string, perm os.FileMode) error {
	res, err := fs.writable(p)
	if err != nil {
		return err
	}

	if perm == 0 {
		perm = 0o755
	}

	return os.Mkdir(res.real, perm)
}

func (fs *Defaultvfs) Remove(ctx context.Context, p string) error {
	res, err := fs.writable(p)
	if err != nil {
		return err
	}

	return os.Remove(res.real)
}

func (fs *Defaultvfs) Rename(ctx context.Context, oldPath, newPath string) error {
	from, err := fs.writable(oldPath)
	if err != nil {
		return err
	}

	to, err := fs.writable(newPath)
	if err != nil {
		return err
	}

	return os.Rename(from.real, to.real)
}

func (fs *Defaultvfs) Truncate(ctx context.Context, p string, size int64) error {
	res, err := fs.writable(p)
	if err != nil {
		return err
	}

	return os.Truncate(res.real, size)
}

func entryFrom(info os.FileInfo) Entry {
	return Entry{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
	}
}
