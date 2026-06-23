package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
)

func handleHealth(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"hostname": hostname})
}

func handleListPeers(peers []PeerShares) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(peers)
	}
}

func main() {
	cfg, err := loadConfig("./config.toml")
	if err != nil {
		fmt.Println(err)
		return
	}

	localShares := cfg.ShareMap()

	peers, err := discoverPeers(context.Background(), cfg)
	if err != nil {
		fmt.Println(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /peers", handleListPeers(peers))
	mux.HandleFunc("GET /shares", handleListShares(localShares))
	mux.HandleFunc("GET /shares/{share}/browse", handleBrowseShare(localShares))
	mux.HandleFunc("GET /shares/{share}/download", handleDownloadShare(localShares))
	mux.HandleFunc("POST /shares/{share}/upload", handleUploadShare(localShares))
	mux.HandleFunc("POST /shares/{share}/delete", handleDeleteShare(localShares))
	mux.HandleFunc("POST /shares/{share}/rename", handleRenameShare(localShares))
	mux.HandleFunc("GET /health", handleHealth)

	ip, err := getTailscaleIP(context.Background(), cfg)
	if err != nil {
		fmt.Println(err)
		return
	}

	listener, err := net.Listen("tcp", ip+":8080")
	if err != nil {
		fmt.Println(err)
		return
	}

	err = http.Serve(listener, mux)
	if err != nil {
		fmt.Println(err)
		return
	}
}
