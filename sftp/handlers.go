package taildrive_sftp

import (
	"io"
	"os"
	"time"

	"github.com/pkg/sftp"

	ts_vfs "a3l6/m/vfs"
)

// handlers maps the sftp request handlers onto a vfs.FS.
type handlers struct {
	fs ts_vfs.FS
}

var (
	_ sftp.FileReader = handlers{}
	_ sftp.FileWriter = handlers{}
	_ sftp.FileCmder  = handlers{}
	_ sftp.FileLister = handlers{}
)

func (h handlers) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	return h.fs.Open(r.Context(), r.Filepath, os.O_RDONLY, 0)
}

func (h handlers) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := os.O_WRONLY | os.O_CREATE
	if r.Pflags().Trunc {
		flags |= os.O_TRUNC
	}

	return h.fs.Open(r.Context(), r.Filepath, flags, 0o644)
}

func (h handlers) Filecmd(r *sftp.Request) error {
	ctx := r.Context()

	switch r.Method {
	case "Rename":
		return h.fs.Rename(ctx, r.Filepath, r.Target)
	case "Rmdir", "Remove":
		return h.fs.Remove(ctx, r.Filepath)
	case "Mkdir":
		return h.fs.Mkdir(ctx, r.Filepath, 0o755)
	case "Setstat":
		if attrs := r.Attributes(); attrs != nil && r.AttrFlags().Size {
			return h.fs.Truncate(ctx, r.Filepath, int64(attrs.Size))
		}
		return nil
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

func (h handlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	ctx := r.Context()

	switch r.Method {
	case "List":
		entries, err := h.fs.ReadDir(ctx, r.Filepath)
		if err != nil {
			return nil, err
		}

		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			infos = append(infos, entryInfo{e})
		}

		return listerat(infos), nil
	case "Stat":
		e, err := h.fs.Stat(ctx, r.Filepath)
		if err != nil {
			return nil, err
		}

		return listerat{entryInfo{e}}, nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

type entryInfo struct {
	e ts_vfs.Entry
}

func (i entryInfo) Name() string       { return i.e.Name }
func (i entryInfo) Size() int64        { return i.e.Size }
func (i entryInfo) Mode() os.FileMode  { return i.e.Mode }
func (i entryInfo) ModTime() time.Time { return i.e.ModTime }
func (i entryInfo) IsDir() bool        { return i.e.Mode.IsDir() }
func (i entryInfo) Sys() any           { return nil }

type listerat []os.FileInfo

func (l listerat) ListAt(ls []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}

	n := copy(ls, l[off:])
	if n < len(ls) {
		return n, io.EOF
	}

	return n, nil
}
