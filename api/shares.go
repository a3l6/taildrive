package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
