package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	sftpclient "a3l6/m/sftp/client"
	"a3l6/m/vfs"
	webdavclient "a3l6/m/webdav/client"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// protocolInfo is one entry of the server's GET /config response.
type protocolInfo struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

// fetchProtocols asks the taildrive API at host:port what it serves.
func fetchProtocols(ctx context.Context, host string, port int) ([]protocolInfo, error) {
	url := fmt.Sprintf("http://%s/config", net.JoinHostPort(host, strconv.Itoa(port)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("client: %s returned %s", url, resp.Status)
	}

	var body struct {
		Protocols []protocolInfo `json:"protocols"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Protocols, nil
}

// chooseProtocol resolves a mount's protocol intent against what the server
// offers. A concrete intent ("sftp"/"webdav") must be enabled; "auto" takes
// the first enabled protocol in pref order.
func chooseProtocol(intent string, protos []protocolInfo, pref []string) (protocolInfo, bool) {
	byName := make(map[string]protocolInfo, len(protos))
	for _, p := range protos {
		byName[strings.ToLower(p.Name)] = p
	}

	if intent != "auto" {
		p, ok := byName[intent]
		return p, ok && p.Enabled
	}

	for _, name := range pref {
		if p, ok := byName[strings.ToLower(name)]; ok && p.Enabled {
			return p, true
		}
	}
	return protocolInfo{}, false
}

// openBackend dials p and returns a live vfs.FS.
func openBackend(p protocolInfo) (vfs.FS, error) {
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	switch strings.ToLower(p.Name) {
	case "sftp":
		return sftpclient.Dial(addr)
	case "webdav":
		return webdavclient.Dial("http://" + addr), nil
	default:
		return nil, fmt.Errorf("client: unknown protocol %q", p.Name)
	}
}
