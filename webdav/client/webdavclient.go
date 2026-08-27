package client

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"a3l6/m/vfs"
)

type FS struct {
	base   string
	client *http.Client
}

var _ vfs.FS = (*FS)(nil)

// taildrive's WebDAV server has no auth middleware, so this is
// unauthenticated over the tailnet — same trust model as the SFTP side.
func Dial(base string) *FS {
	return &FS{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 30 * time.Second}}
}

type propResponse struct {
	Href     string `xml:"href"`
	Propstat struct {
		Prop struct {
			ResourceType struct {
				Collection *struct{} `xml:"collection"`
			} `xml:"resourcetype"`
			ContentLength int64  `xml:"getcontentlength"`
			LastModified  string `xml:"getlastmodified"`
		} `xml:"prop"`
	} `xml:"propstat"`
}

func (f *FS) propfind(ctx context.Context, p, depth string) ([]propResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", f.base+p, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", depth)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("webdav: propfind %s: %s", p, resp.Status)
	}

	var ms struct {
		Responses []propResponse `xml:"response"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, err
	}
	return ms.Responses, nil
}

func (f *FS) Stat(ctx context.Context, p string) (vfs.Entry, error) {
	rs, err := f.propfind(ctx, p, "0")
	if err != nil {
		return vfs.Entry{}, err
	}
	if len(rs) == 0 {
		return vfs.Entry{}, os.ErrNotExist
	}
	return entryFromProp(path.Base(p), rs[0]), nil
}

func (f *FS) ReadDir(ctx context.Context, p string) ([]vfs.Entry, error) {
	rs, err := f.propfind(ctx, p, "1")
	if err != nil {
		return nil, err
	}

	entries := make([]vfs.Entry, 0, len(rs))
	for _, r := range rs {
		name := path.Base(strings.TrimSuffix(r.Href, "/"))
		if name == "" || name == path.Base(p) {
			continue // self, included at Depth: 1
		}
		entries = append(entries, entryFromProp(name, r))
	}
	return entries, nil
}

func (f *FS) get(ctx context.Context, p string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.base+p, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webdav: get %s: %s", p, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (f *FS) put(ctx context.Context, p string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, f.base+p, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	default:
		return fmt.Errorf("webdav: put %s: %s", p, resp.Status)
	}
}

// Open buffers the whole file locally and flushes it with one PUT on Close,
// the same cache-and-sync approach tools like davfs2 use, since WebDAV has
// no byte-range write verb to stream partial writes through.
func (f *FS) Open(ctx context.Context, p string, flags int, perm os.FileMode) (vfs.File, error) {
	var buf []byte

	if flags&os.O_TRUNC == 0 {
		body, err := f.get(ctx, p)
		switch {
		case err == nil:
			buf = body
		case flags&os.O_CREATE == 0:
			return nil, err
		}
	}

	return &file{fs: f, path: p, buf: buf}, nil
}

type file struct {
	fs    *FS
	path  string
	mu    sync.Mutex
	buf   []byte
	dirty bool
}

func (h *file) ReadAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if off >= int64(len(h.buf)) {
		return 0, io.EOF
	}
	n := copy(p, h.buf[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (h *file) WriteAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if end := off + int64(len(p)); end > int64(len(h.buf)) {
		grown := make([]byte, end)
		copy(grown, h.buf)
		h.buf = grown
	}
	copy(h.buf[off:], p)
	h.dirty = true
	return len(p), nil
}

func (h *file) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.dirty {
		return nil
	}
	return h.fs.put(context.Background(), h.path, h.buf)
}

func (f *FS) Mkdir(ctx context.Context, p string, perm os.FileMode) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", f.base+p, nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("webdav: mkcol %s: %s", p, resp.Status)
	}
	return nil
}

func (f *FS) Remove(ctx context.Context, p string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, f.base+p, nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("webdav: delete %s: %s", p, resp.Status)
	}
	return nil
}

func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	req, err := http.NewRequestWithContext(ctx, "MOVE", f.base+oldPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Destination", f.base+newPath)
	req.Header.Set("Overwrite", "T")

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("webdav: move %s -> %s: %s", oldPath, newPath, resp.Status)
	}
	return nil
}

// Truncate has no WebDAV verb, so it round-trips: fetch, resize, put back.
func (f *FS) Truncate(ctx context.Context, p string, size int64) error {
	body, err := f.get(ctx, p)
	if err != nil {
		return err
	}

	if int64(len(body)) < size {
		grown := make([]byte, size)
		copy(grown, body)
		body = grown
	} else {
		body = body[:size]
	}
	return f.put(ctx, p, body)
}

func entryFromProp(name string, r propResponse) vfs.Entry {
	prop := r.Propstat.Prop
	mode := os.FileMode(0o644)
	if prop.ResourceType.Collection != nil {
		mode = os.ModeDir | 0o755
	}
	t, _ := time.Parse(time.RFC1123, prop.LastModified)
	return vfs.Entry{Name: name, Size: prop.ContentLength, Mode: mode, ModTime: t}
}
