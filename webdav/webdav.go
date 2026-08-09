package taildrive_webdav

import (
	"context"
	"log"
	"net/http"

	tdvfs "a3l6/m/vfs"

	"golang.org/x/net/webdav"
)

func Run(ctx context.Context, fs tdvfs.FS) error {
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
	// TODO: change this port to be from config
	srv := &http.Server{Addr: ":8081", Handler: handler}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-runCtx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Println("serving on :8080")
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}

	<-drained
	log.Println("webdav: stopped")
	return ctx.Err()
}
