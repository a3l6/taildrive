package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		entries, err := os.ReadDir(absPath)
		if err != nil {
			log.Printf("browse: read dir %s: %v", absPath, err)
			http.Error(w, "cannot read directory", http.StatusInternalServerError)
			return
		}

		type Entry struct {
			Name     string    `json:"name"`
			Size     int64     `json:"size"`
			Modified time.Time `json:"modified"`
			IsDir    bool      `json:"is_dir"`
		}

		type Response struct {
			Entries []Entry `json:"entries"`
			Path    string  `json:"path"`
		}

		var result Response
		result.Path = requestedPath
		for _, entry := range entries {
			info, _ := entry.Info()
			result.Entries = append(result.Entries, Entry{
				Name:     entry.Name(),
				Size:     info.Size(),
				Modified: info.ModTime(),
				IsDir:    info.IsDir(),
			})
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
			log.Printf("download: open %s: %v", absPath, err)
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

		cr := r.Header.Get("Content-Range")
		if cr == "" { // regular file upload
			f, err := os.Create(absPath)
			if err != nil {
				log.Printf("upload: create %s: %v", absPath, err)
				http.Error(w, "cannot create file", http.StatusInternalServerError)
				return
			}
			defer f.Close()

			if _, err := io.Copy(f, r.Body); err != nil {
				log.Printf("upload: write %s: %v", absPath, err)
				http.Error(w, "cannot write file", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}

		var start, end, total int64
		if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total); err != nil {
			log.Printf("upload: bad Content-Range %q: %v", cr, err)
			http.Error(w, "invalid Content-Range", http.StatusBadRequest)
			return
		}

		data, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("upload: read chunk body %s: %v", absPath, err)
			http.Error(w, "cannot read chunk", http.StatusInternalServerError)
			return
		}

		if md5Hex := r.Header.Get("X-Chunk-MD5"); md5Hex != "" {
			h := md5.Sum(data)
			if hex.EncodeToString(h[:]) != md5Hex {
				log.Printf("upload: chunk MD5 mismatch for %s", absPath)
				http.Error(w, "chunk integrity check failed", http.StatusBadRequest)
				return
			}
		}

		f, err := os.OpenFile(absPath, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("upload: open %s: %v", absPath, err)
			http.Error(w, "cannot open file", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		if _, err = f.WriteAt(data, start); err != nil {
			log.Printf("upload: write chunk at %d in %s: %v", start, absPath, err)
			http.Error(w, "cannot write chunk", http.StatusInternalServerError)
			return
		}

		if end+1 == total {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusPartialContent)
		}

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
			log.Printf("delete: remove %s: %v", absPath, err)
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

		newName := r.URL.Query().Get("new_name")
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
			log.Printf("rename: %s -> %s: %v", oldPath, newPath, err)
			http.Error(w, "cannot rename file", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
