package webdav

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"

	"a3l6/m/common/ts"
	"a3l6/m/common/types"
	"a3l6/m/config"
	"a3l6/m/vfs"

	"golang.org/x/net/webdav"
)

func Run(ctx context.Context, fs vfs.FS, cfg *config.Config, ready types.ReadyFunc) error {
	handler := &webdav.Handler{
		Prefix:     "/",
		FileSystem: davFS{fs},
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				log.Printf("webdav: %s %s: %v", r.Method, r.URL.Path, err)
			}
		},
	}

	// Bind to the tailnet address only, like the API, so the address we
	// advertise is the one we actually listen on.
	ip, err := ts.GetTailscaleIP(ctx, cfg)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(cfg.Webdav.Port)))
	if err != nil {
		return err
	}

	srv := &http.Server{Handler: handler}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-runCtx.Done()
		srv.Shutdown(context.Background())
	}()

	addr := ln.Addr().(*net.TCPAddr)
	log.Println("webdav: listening on", addr)
	ready(ip, addr.Port)

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}

	<-drained
	log.Println("webdav: stopped")
	return ctx.Err()
}
