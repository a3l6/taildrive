package taildrive_sftp

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

type vfs struct {
	mounts map[string]string // cleaned virtual path -> absolute dir
}

func newVFS(m map[string]string) *vfs {
	clean := make(map[string]string, len(m))
	for v, r := range m {
		vp := path.Clean("/" + strings.Trim(v, "/"))
		rp, _ := filepath.Abs(r)
		clean[vp] = rp
	}

	return &vfs{mounts: clean}
}

type resolved struct {
	clean     string
	real      string
	mountRoot string
	isVirtual bool
}

func (fs *vfs) resolve(virtual string) (resolved, error) {
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
			return resolved{}, os.ErrPermission
		}

		return resolved{clean: clean, real: real, mountRoot: root}, nil
	}

	if clean == "/" || fs.isAncestor(clean) {
		return resolved{clean: clean, isVirtual: true}, nil
	}

	return resolved{}, os.ErrNotExist
}

func (fs *vfs) isAncestor(clean string) bool {
	prefix := clean + "/"

	for vp := range fs.mounts {
		if strings.HasPrefix(vp, prefix) {
			return true
		}
	}

	return false
}

func (fs *vfs) children(clean string) []os.FileInfo {
	prefix := "/"
	if clean != "/" {
		prefix = clean + "/"
	}

	seen := map[string]bool{}
	var out []os.FileInfo

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
		out = append(out, virtualDir{name: child, mod: time.Now()})
	}

	return out
}

func (fs *vfs) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	res, err := fs.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	if res.isVirtual {
		return nil, os.ErrPermission
	}

	return os.OpenFile(res.real, os.O_RDONLY, 0)
}

func (fs *vfs) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	res, err := fs.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	if res.isVirtual {
		return nil, os.ErrPermission
	}

	flags := os.O_WRONLY | os.O_CREATE
	if r.Pflags().Trunc {
		flags |= os.O_TRUNC
	}

	return os.OpenFile(res.real, flags, 0o644)
}

func (fs *vfs) Filecmd(r *sftp.Request) error {
	res, err := fs.resolve(r.Filepath)
	if err != nil {
		return err
	}

	if res.isVirtual || res.real == res.mountRoot {
		return sftp.ErrSSHFxPermissionDenied
	}

	switch r.Method {
	case "Rename":
		t, err := fs.resolve(r.Target)
		if err != nil {
			return err
		}

		if t.isVirtual {
			return sftp.ErrSSHFxPermissionDenied
		}
		return os.Rename(res.real, t.real)
	case "Rmdir", "Remove":
		return os.Remove(res.real)
	case "Mkdir":
		return os.Mkdir(res.real, 0o755)
	case "Setstat":
		return nil
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

func (fs *vfs) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	res, err := fs.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	switch r.Method {
	case "List":
		if res.isVirtual {
			return listerat(fs.children(res.clean)), nil
		}
		entries, err := os.ReadDir(res.real)
		if err != nil {
			return nil, err
		}

		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				infos = append(infos, info)
			}
		}

		return listerat(infos), nil
	case "Stat":
		if res.isVirtual {
			name := path.Base(res.clean)
			return listerat{virtualDir{name: name, mod: time.Now()}}, nil
		}
		info, err := os.Stat(res.real)
		if err != nil {
			return nil, err
		}
		return listerat{info}, nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

type virtualDir struct {
	name string
	mod  time.Time
}

func (v virtualDir) Name() string       { return v.name }
func (v virtualDir) Size() int64        { return 0 }
func (v virtualDir) Mode() os.FileMode  { return os.ModeDir | 0o555 }
func (v virtualDir) ModTime() time.Time { return v.mod }
func (v virtualDir) IsDir() bool        { return true }
func (v virtualDir) Sys() any           { return nil }

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
