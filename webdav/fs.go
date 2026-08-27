package webdav

import (
	"context"
	"io"
	"os"
	"path"
	"syscall"
	"time"

	"golang.org/x/net/webdav"

	"a3l6/m/vfs"
)

// davFS maps the webdav filesystem calls onto a vfs.FS.
type davFS struct {
	fs vfs.FS
}

var _ webdav.FileSystem = davFS{}

func (d davFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return d.fs.Mkdir(ctx, name, perm)
}

// OpenFile stats first so that directories, including the synthesised ones that
// have nothing on disk behind them, come back as a handle that can only be
// listed.
func (d davFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if e, err := d.fs.Stat(ctx, name); err == nil && e.Mode.IsDir() {
		return &davFile{fs: d.fs, ctx: ctx, name: name}, nil
	}

	f, err := d.fs.Open(ctx, name, flag, perm)
	if err != nil {
		return nil, err
	}

	return &davFile{fs: d.fs, ctx: ctx, name: name, f: f}, nil
}

// RemoveAll walks the tree itself because vfs.Remove is not recursive, and RFC
// 4918 makes DELETE on a collection depth infinity.
func (d davFS) RemoveAll(ctx context.Context, name string) error {
	if entries, err := d.fs.ReadDir(ctx, name); err == nil {
		for _, e := range entries {
			if err := d.RemoveAll(ctx, path.Join(name, e.Name)); err != nil {
				return err
			}
		}
	}

	return d.fs.Remove(ctx, name)
}

func (d davFS) Rename(ctx context.Context, oldName, newName string) error {
	return d.fs.Rename(ctx, oldName, newName)
}

func (d davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	e, err := d.fs.Stat(ctx, name)
	if err != nil {
		return nil, err
	}

	return entryInfo{e}, nil
}

// davFile carries the cursor webdav expects on top of the absolute offsets vfs
// exposes. A nil f means the path is a directory, which is list only.
type davFile struct {
	fs   vfs.FS
	ctx  context.Context
	name string
	f    vfs.File

	off     int64
	entries []os.FileInfo
	listed  bool
}

var _ webdav.File = (*davFile)(nil)

func (f *davFile) Read(p []byte) (int, error) {
	if f.f == nil {
		return 0, syscall.EISDIR
	}

	n, err := f.f.ReadAt(p, f.off)
	f.off += int64(n)

	return n, err
}

func (f *davFile) Write(p []byte) (int, error) {
	if f.f == nil {
		return 0, syscall.EISDIR
	}

	n, err := f.f.WriteAt(p, f.off)
	f.off += int64(n)

	return n, err
}

func (f *davFile) Seek(offset int64, whence int) (int64, error) {
	if f.f == nil {
		return 0, syscall.EISDIR
	}

	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		e, err := f.fs.Stat(f.ctx, f.name)
		if err != nil {
			return 0, err
		}
		f.off = e.Size + offset
	}

	if f.off < 0 {
		return 0, syscall.EINVAL
	}

	return f.off, nil
}

// Readdir consumes from entries so repeated calls advance without a second
// cursor, and listed keeps an empty directory from being read twice.
func (f *davFile) Readdir(count int) ([]os.FileInfo, error) {
	if f.f != nil {
		return nil, syscall.ENOTDIR
	}

	if !f.listed {
		entries, err := f.fs.ReadDir(f.ctx, f.name)
		if err != nil {
			return nil, err
		}

		for _, e := range entries {
			f.entries = append(f.entries, entryInfo{e})
		}
		f.listed = true
	}

	rest := f.entries
	if count > 0 && count < len(rest) {
		rest = rest[:count]
	}
	f.entries = f.entries[len(rest):]

	if count > 0 && len(rest) == 0 {
		return nil, io.EOF
	}

	return rest, nil
}

func (f *davFile) Stat() (os.FileInfo, error) {
	e, err := f.fs.Stat(f.ctx, f.name)
	if err != nil {
		return nil, err
	}

	return entryInfo{e}, nil
}

func (f *davFile) Close() error {
	if f.f == nil {
		return nil
	}

	return f.f.Close()
}

type entryInfo struct {
	e vfs.Entry
}

func (i entryInfo) Name() string       { return i.e.Name }
func (i entryInfo) Size() int64        { return i.e.Size }
func (i entryInfo) Mode() os.FileMode  { return i.e.Mode }
func (i entryInfo) ModTime() time.Time { return i.e.ModTime }
func (i entryInfo) IsDir() bool        { return i.e.Mode.IsDir() }
func (i entryInfo) Sys() any           { return nil }
