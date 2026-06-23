package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func safePath(shareRoot, requestedPath string) (string, error) {
	cleaned := filepath.Clean("/" + requestedPath)

	joined := filepath.Join(shareRoot, cleaned)

	root := filepath.Clean(shareRoot)

	if joined != root && !strings.HasPrefix(joined, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path not in share")
	}
	return joined, nil
}

func handleListShares(shares map[string]Share) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type ShareResponse struct {
			Name     string `json:"name"`
			ReadOnly bool   `json:"read_only"`
		}

		var result []ShareResponse
		for _, share := range shares {
			result = append(result, ShareResponse{
				Name:     share.Name,
				ReadOnly: share.ReadOnly,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func handleBrowseShare(shares map[string]Share) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		share, ok := shares[r.PathValue("share")]
		if !ok {
			http.NotFound(w, r)
			return
		}

		requestedPath := r.URL.Query().Get("path")
		absPath, err := safePath(share.Path, requestedPath)
		if err != nil {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		type Entry struct {
			Name     string    `json:"name"`
			Path     string    `json:"path"`
			Size     int64     `json:"size"`
			Modified time.Time `json:"modified"`
			IsDir    bool      `json:"is_dir"`
		}
		type Response struct {
			Path    string  `json:"path"`
			Entries []Entry `json:"entries"`
		}

		var result Response
		result.Path = requestedPath

		err = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// skip the root itself
			if path == absPath {
				return nil
			}
			info, _ := d.Info()
			// path relative to the share root, not the server filesystem
			rel, _ := filepath.Rel(share.Path, path)
			result.Entries = append(result.Entries, Entry{
				Name:     d.Name(),
				Path:     rel,
				Size:     info.Size(),
				Modified: info.ModTime(),
				IsDir:    d.IsDir(),
			})
			return nil
		})
		if err != nil {
			http.Error(w, "could not walk directory", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func handleDownloadShare(shares map[string]Share) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		share, ok := shares[r.PathValue("share")]
		if !ok {
			http.Error(w, "share not found", http.StatusNotFound)
			return
		}

		absPath, err := safePath(share.Path, r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		f, err := os.Open(absPath)
		if err != nil {
			http.Error(w, "file not found", http.StatusInternalServerError)
			return
		}

		defer f.Close()

		info, _ := f.Stat()
		w.Header().Set("Content-Disposition", "attachment; filename="+info.Name())
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))

		_, _ = io.Copy(w, f)
	}
}

func handleUploadShare(shares map[string]Share) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		share, ok := shares[r.PathValue("share")]
		if !ok {
			http.Error(w, "share not found", http.StatusNotFound)
			return
		}

		if share.ReadOnly {
			http.Error(w, "share is read only", http.StatusForbidden)
			return
		}

		absPath, err := safePath(share.Path, r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		f, err := os.Create(absPath)
		if err != nil {
			http.Error(w, "cannot create file", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		_, err = io.Copy(f, r.Body)
		if err != nil {
			http.Error(w, "cannot write file", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func handleDeleteShare(shares map[string]Share) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		share, ok := shares[r.PathValue("share")]
		if !ok {
			http.Error(w, "share not found", http.StatusNotFound)
			return
		}

		if share.ReadOnly {
			http.Error(w, "share is read only", http.StatusForbidden)
			return
		}

		absPath, err := safePath(share.Path, r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		if err := os.Remove(absPath); err != nil {
			http.Error(w, "cannot delete file", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleRenameShare(shares map[string]Share) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		share, ok := shares[r.PathValue("share")]
		if !ok {
			http.Error(w, "share not found", http.StatusNotFound)
			return
		}

		if share.ReadOnly {
			http.Error(w, "share is read only", http.StatusForbidden)
			return
		}

		oldPath, err := safePath(share.Path, r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		newName := r.URL.Query().Get("newname")
		if newName == "" {
			http.Error(w, "new name not provided", http.StatusBadRequest)
			return
		}

		if strings.Contains(newName, "/") || strings.Contains(newName, "..") {
			http.Error(w, "invalid new name", http.StatusBadRequest)
			return
		}

		newPath := filepath.Join(filepath.Dir(oldPath), newName)
		if err := os.Rename(oldPath, newPath); err != nil {
			http.Error(w, "cannot rename file", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
