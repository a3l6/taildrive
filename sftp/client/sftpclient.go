package client

import (
	"context"
	"net"
	"os"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"a3l6/m/vfs"
)

type FS struct {
	client *sftp.Client
	conn   *ssh.Client
}

var _ vfs.FS = (*FS)(nil)

// Dial connects to a taildrive SFTP server at addr (host:port). The server
// runs with NoClientAuth, so no credentials go here — the tailnet itself is
// the trust boundary.
func Dial(addr string) (*FS, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:            "taildrive",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, err
	}

	return &FS{client: client, conn: sshClient}, nil
}

func (f *FS) Close() error {
	f.client.Close()
	return f.conn.Close()
}

func (f *FS) Stat(ctx context.Context, p string) (vfs.Entry, error) {
	info, err := f.client.Stat(p)
	if err != nil {
		return vfs.Entry{}, err
	}
	return entryFrom(info), nil
}

func (f *FS) ReadDir(ctx context.Context, p string) ([]vfs.Entry, error) {
	infos, err := f.client.ReadDir(p)
	if err != nil {
		return nil, err
	}

	entries := make([]vfs.Entry, 0, len(infos))
	for _, info := range infos {
		entries = append(entries, entryFrom(info))
	}
	return entries, nil
}

// *sftp.File implements ReadAt, WriteAt and Close, so it satisfies
// vfs.File without any wrapping.
func (f *FS) Open(ctx context.Context, p string, flags int, perm os.FileMode) (vfs.File, error) {
	file, err := f.client.OpenFile(p, flags)
	if err != nil {
		return nil, err
	}

	if flags&os.O_CREATE != 0 && perm != 0 {
		f.client.Chmod(p, perm) // best-effort; SFTP OPEN carries no mode of its own
	}

	return file, nil
}

func (f *FS) Mkdir(ctx context.Context, p string, perm os.FileMode) error {
	if err := f.client.Mkdir(p); err != nil {
		return err
	}
	if perm != 0 {
		f.client.Chmod(p, perm)
	}
	return nil
}

// Remove tries file-removal first, then falls back to directory-removal —
// SFTP has no single verb for "remove whatever this is" the way os.Remove
// does.
func (f *FS) Remove(ctx context.Context, p string) error {
	if err := f.client.Remove(p); err != nil {
		if info, statErr := f.client.Stat(p); statErr == nil && info.IsDir() {
			return f.client.RemoveDirectory(p)
		}
		return err
	}
	return nil
}

func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	return f.client.Rename(oldPath, newPath)
}

func (f *FS) Truncate(ctx context.Context, p string, size int64) error {
	return f.client.Truncate(p, size)
}

func entryFrom(info os.FileInfo) vfs.Entry {
	return vfs.Entry{Name: info.Name(), Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()}
}
