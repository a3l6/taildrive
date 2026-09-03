package main

import (
	"context"
	"log"
	"strings"
	"time"
)

// pollConfig periodically re-checks each auto mount's peer and restarts the
// mount when its preferred protocol changes. It mirrors the server's
// auto_generate_delay loop; a non-positive poll interval disables it.
func pollConfig(ctx context.Context, mgr *mountManager) {
	if mgr.cc.PollInterval <= 0 {
		return
	}

	t := time.NewTicker(time.Duration(mgr.cc.PollInterval) * time.Millisecond)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, st := range mgr.List() {
				if st.Record.Protocol != "auto" || st.State != stateMounted {
					continue
				}

				protos, err := fetchProtocols(ctx, st.Record.Peer, mgr.cc.ServerPort)
				if err != nil {
					continue
				}

				want, ok := chooseProtocol("auto", protos, mgr.cc.ProtocolPreference)
				if !ok || strings.ToLower(want.Name) == st.Resolved {
					continue
				}

				log.Printf("client: %s: switching %s -> %s", st.Record.MountPoint, st.Resolved, strings.ToLower(want.Name))
				if err := mgr.Restart(st.Record.MountPoint); err != nil {
					log.Printf("client: %s: %v", st.Record.MountPoint, err)
				}
			}
		}
	}
}
