package main

import (
	"context"
	"log"

	"tailscale.com/client/local"
)

// serverInfo is one reachable taildrive peer and the protocols it serves.
type serverInfo struct {
	Peer      string
	Protocols []protocolInfo
}

// discoverServers pings /config on every tailnet peer and returns those that
// answered. Unreachable peers are skipped, not fatal.
func discoverServers(ctx context.Context, socket string, apiPort int) ([]serverInfo, error) {
	client := &local.Client{Socket: socket}

	st, err := client.Status(ctx)
	if err != nil {
		return nil, err
	}

	var found []serverInfo
	for _, peer := range st.Peer {
		protos, err := fetchProtocols(ctx, peer.TailscaleIPs[0].String(), apiPort)
		if err != nil {
			log.Printf("discover: cannot find protocol for %s", peer.HostName)
			continue
		}
		found = append(found, serverInfo{Peer: peer.TailscaleIPs[0].String(), Protocols: protos})
	}
	return found, nil
}
