package main

import (
	"context"
	"fmt"

	"a3l6/m/config"
	"a3l6/m/fuse"
	sftpclient "a3l6/m/sftp/client"
	"a3l6/m/vfs"
	webdavclient "a3l6/m/webdav/client"
)

// startFuse connects to cfg.Fuse.Peer over the configured backend and mounts
// its share at cfg.Fuse.Mountpoint. It blocks until ctx is cancelled or the
// mount fails; nothing calls it from main() yet.
func startFuse(ctx context.Context, cfg *config.Config) error {
	if !cfg.Fuse.Enabled {
		return nil
	}

	var backend vfs.FS

	switch cfg.Fuse.Backend {
	case "sftp":
		fs, err := sftpclient.Dial(cfg.Fuse.Peer + ":22")
		if err != nil {
			return fmt.Errorf("fuse: dial %s over sftp: %w", cfg.Fuse.Peer, err)
		}
		backend = fs
	case "webdav":
		backend = webdavclient.Dial("http://" + cfg.Fuse.Peer + ":8081")
	default:
		return fmt.Errorf("fuse: unknown backend %q (want \"sftp\" or \"webdav\")", cfg.Fuse.Backend)
	}

	return fuse.Mount(ctx, cfg.Fuse.Mountpoint, backend, fuse.Options{
		Name:     cfg.Fuse.Share,
		ReadOnly: cfg.Fuse.ReadOnly,
	})
}
