package webdav

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"a3l6/m/config"
	"a3l6/m/vfs"

	"golang.org/x/net/webdav"
)

func Run(ctx context.Context, fs vfs.FS) error {
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

	cfg, err := config.LoadConfig("./config.toml")
	if err != nil {
		cfg.ApplyDefaults()
	}

	srv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Webdav.Port), Handler: handler}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-runCtx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Println("webdav: listening on port ", cfg.Webdav.Port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}

	<-drained
	log.Println("webdav: stopped")
	return ctx.Err()
}
