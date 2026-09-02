package ts

import (
	"a3l6/m/config"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"tailscale.com/client/local"
	"time"
)

type PeerShares struct {
	PeerName string
	PeerIP   string
	Shares   []RemoteShare
}

type RemoteShare struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
}

func ListPeers(ctx context.Context, cfg *config.Config) ([]string, error) {
	client := &local.Client{
		Socket: cfg.TailscaleSocket,
	}

	st, err := client.Status(ctx)
	if err != nil {
		return []string{}, err
	}

	peers := make([]string, len(st.Peer))

	for _, peer := range st.Peer {
		for _, addr := range peer.TailscaleIPs {
			if addr.Is4() && addr.String() != "" {
				peers = append(peers, addr.String())
			}
		}
	}

	return peers, nil
}

func DiscoverPeers(ctx context.Context, cfg *config.Config) ([]PeerShares, error) {
	client := &local.Client{
		Socket: cfg.TailscaleSocket,
	}
	st, err := client.Status(ctx)
	if err != nil {
		return nil, err
	}

	type result struct {
		ps PeerShares
		ok bool
	}

	results := make(chan result, len(st.Peer))

	for _, peer := range st.Peer {
		peer := peer

		go func() {
			ip := ""
			for _, addr := range peer.TailscaleIPs {
				if addr.Is4() {
					ip = addr.String()
					break
				}
			}

			if ip == "" {
				results <- result{ok: false}
				return
			}

			base := fmt.Sprintf("http://%s:%d", ip, cfg.Port)
			client := &http.Client{Timeout: 2 * time.Second}

			resp, err := client.Get(base + "/health")
			if err != nil || resp.StatusCode != http.StatusOK {
				log.Printf("discovery: peer %s unreachable", peer.HostName)
				results <- result{ok: false}
				return
			}

			resp.Body.Close()

			resp, err = client.Get(base + "/shares")
			if err != nil || resp.StatusCode != http.StatusOK {
				log.Printf("discovery: peer %s unreachable", peer.HostName)
				results <- result{ok: false}
				return
			}
			defer resp.Body.Close()

			var shares []RemoteShare
			err = json.NewDecoder(resp.Body).Decode(&shares)
			if err != nil {
				log.Printf("discovery: peer %s unreachable", peer.HostName)
				results <- result{ok: false}
				return
			}

			results <- result{
				ok: true,
				ps: PeerShares{
					PeerName: peer.HostName,
					PeerIP:   ip,
					Shares:   shares,
				},
			}
		}()
	}

	var peers []PeerShares
	for range st.Peer {
		if r := <-results; r.ok {
			peers = append(peers, r.ps)
		}
	}
	return peers, nil

}

func GetTailscaleIP(ctx context.Context, cfg *config.Config) (string, error) {
	client := &local.Client{
		Socket: cfg.TailscaleSocket,
	}
	st, err := client.Status(ctx)
	if err != nil {
		return "", err
	}

	for _, ip := range st.TailscaleIPs {
		if ip.Is4() {
			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("no tailscale IPv4 address found")
}

// SelfHostname returns this node's short tailnet hostname (e.g. "mylaptop").
func SelfHostname(ctx context.Context, cfg *config.Config) (string, error) {
	client := &local.Client{
		Socket: cfg.TailscaleSocket,
	}
	st, err := client.Status(ctx)
	if err != nil {
		return "", err
	}

	return st.Self.HostName, nil
}

// SFTPHostname is the tailnet hostname the SFTP server registers under, derived
// from the host's short hostname. The SFTP server runs on its own tsnet node
// (see sftp.Run), so it is not reachable at the API host's address.
func SFTPHostname(base string) string {
	return base + "-sftp-vfs"
}
