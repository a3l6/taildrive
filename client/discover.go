package main

import (
	"context"

	"a3l6/m/common/ts"
)

// serverInfo is one reachable taildrive peer and the protocols it serves.
type serverInfo struct {
	Peer      string
	Protocols []protocolInfo
}

// discoverServers pings /config on every tailnet peer and returns those that
// answered. Unreachable peers are skipped, not fatal.
func discoverServers(ctx context.Context, socket string, apiPort int) ([]serverInfo, error) {
	peers, err := ts.ListPeers(ctx, socket)
	if err != nil {
		return nil, err
	}

	var found []serverInfo
	for _, peer := range peers {
		protos, err := fetchProtocols(ctx, peer, apiPort)
		if err != nil {
			continue
		}
		found = append(found, serverInfo{Peer: peer, Protocols: protos})
	}
	return found, nil
}
