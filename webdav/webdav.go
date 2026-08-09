package taildrive_webdav

import (
	"log"
	"net/http"

	"golang.org/x/net/webdav"
)

func Run() {
	handler := &webdav.Handler{
		Prefix:     "/",
		FileSystem: webdav.Dir("./data"),
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
