package main

import (
	"context"
	"log"
	"sync"
	"time"

	"tailscale.com/client/local"
)

// peerProbeTimeout bounds each peer's /config probe so one hung peer cannot
// eat the whole discovery deadline.
const peerProbeTimeout = 2 * time.Second

// serverInfo is one reachable taildrive peer and the protocols it serves.
type serverInfo struct {
	Peer      string
	Protocols []protocolInfo
}

// discoverServers pings /config on every online tailnet peer in parallel and
// returns those that answered. Unreachable peers are skipped, not fatal.
func discoverServers(ctx context.Context, socket string, apiPort int) ([]serverInfo, error) {
	client := &local.Client{Socket: socket}

	st, err := client.Status(ctx)
	if err != nil {
		return nil, err
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		found []serverInfo
	)
	for _, peer := range st.Peer {
		if !peer.Online {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			pctx, cancel := context.WithTimeout(ctx, peerProbeTimeout)
			defer cancel()

			ip := peer.TailscaleIPs[0].String()
			protos, err := fetchProtocols(pctx, ip, apiPort)
			if err != nil {
				log.Printf("discover: cannot find protocol for %s: %v", peer.HostName, err)
				return
			}

			mu.Lock()
			found = append(found, serverInfo{Peer: ip, Protocols: protos})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return found, nil
}
