package taildrive_webdav

import (
	"log"
	"net/http"

	tdvfs "a3l6/m/vfs"
	"golang.org/x/net/webdav"
)

func Run(fs tdvfs.FS) {
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
	log.Println("serving on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}
