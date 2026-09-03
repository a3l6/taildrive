package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"a3l6/m/fuse"
	"a3l6/m/vfs"
)

const (
	backoffMin = time.Second
	backoffMax = 30 * time.Second
)

type mountState int

const (
	stateStarting mountState = iota
	stateMounted
	stateRetrying
	stateStopped
)

func (s mountState) String() string {
	switch s {
	case stateStarting:
		return "starting"
	case stateMounted:
		return "mounted"
	case stateRetrying:
		return "retrying"
	case stateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// activeMount is one supervised mount.
type activeMount struct {
	rec    mountRecord
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	state    mountState
	resolved string // protocol currently serving the mount
}

func (a *activeMount) setStatus(s mountState, resolved string) {
	a.mu.Lock()
	a.state = s
	a.resolved = resolved
	a.mu.Unlock()
}

func (a *activeMount) status() (mountState, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state, a.resolved
}

// mountManager supervises the set of active mounts. It mirrors the server's
// api Registry: a cancel func per entry, a mutex, and a WaitGroup for
// shutdown. opMu serialises lifecycle operations; mu guards the map.
type mountManager struct {
	base context.Context
	cc   clientConfig

	opMu   sync.Mutex
	mu     sync.Mutex
	active map[string]*activeMount // keyed by mountpoint
	wg     sync.WaitGroup
}

func newMountManager(ctx context.Context, cc clientConfig) *mountManager {
	return &mountManager{
		base:   ctx,
		cc:     cc,
		active: make(map[string]*activeMount),
	}
}

// Start supervises rec. It errors if the mountpoint is already active.
func (m *mountManager) Start(rec mountRecord) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	am, exists := m.active[rec.MountPoint]
	m.mu.Unlock()
	if exists {
		if st, _ := am.status(); st != stateStopped {
			return fmt.Errorf("client: %s already mounted", rec.MountPoint)
		}
	}

	m.spawn(rec)
	return nil
}

// Stop cancels the mount at mountpoint and waits for it to unmount.
func (m *mountManager) Stop(mountpoint string) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.stop(mountpoint)
}

// Restart stops the mount and starts it again from its record.
func (m *mountManager) Restart(mountpoint string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	am := m.active[mountpoint]
	m.mu.Unlock()
	if am == nil {
		return fmt.Errorf("client: %s not configured", mountpoint)
	}

	rec := am.rec
	m.stop(mountpoint)
	m.spawn(rec)
	return nil
}

// Remove stops the mount and forgets it.
func (m *mountManager) Remove(mountpoint string) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.stop(mountpoint)
	m.mu.Lock()
	delete(m.active, mountpoint)
	m.mu.Unlock()
}

// StopAll cancels every mount and waits.
func (m *mountManager) StopAll() {
	m.mu.Lock()
	for _, am := range m.active {
		am.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

type mountStatus struct {
	Record   mountRecord
	State    mountState
	Resolved string
}

// List reports every mount the manager knows about.
func (m *mountManager) List() []mountStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]mountStatus, 0, len(m.active))
	for _, am := range m.active {
		st, resolved := am.status()
		out = append(out, mountStatus{Record: am.rec, State: st, Resolved: resolved})
	}
	return out
}

// spawn launches a supervise goroutine for rec. Caller holds opMu.
func (m *mountManager) spawn(rec mountRecord) {
	ctx, cancel := context.WithCancel(m.base)
	am := &activeMount{
		rec:    rec,
		cancel: cancel,
		done:   make(chan struct{}),
		state:  stateStarting,
	}

	m.mu.Lock()
	m.active[rec.MountPoint] = am
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(am.done)
		m.supervise(ctx, am)
	}()
}

// stop cancels and waits for a single mount. Caller holds opMu.
func (m *mountManager) stop(mountpoint string) {
	m.mu.Lock()
	am := m.active[mountpoint]
	m.mu.Unlock()
	if am == nil {
		return
	}

	am.cancel()
	<-am.done
	am.setStatus(stateStopped, "")
}

// supervise keeps one mount up: resolve /config, open a backend, serve it
// until it drops or ctx is cancelled, retry with capped backoff.
func (m *mountManager) supervise(ctx context.Context, am *activeMount) {
	backoff := backoffMin

	for ctx.Err() == nil {
		p, err := m.resolve(ctx, am.rec)
		if err != nil {
			am.setStatus(stateRetrying, "")
			log.Printf("client: %s: %v", am.rec.MountPoint, err)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backend, err := openBackend(p)
		if err != nil {
			am.setStatus(stateRetrying, "")
			log.Printf("client: %s: dial %s: %v", am.rec.MountPoint, p.Name, err)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		am.setStatus(stateMounted, strings.ToLower(p.Name))
		backoff = backoffMin
		log.Printf("client: %s: mounted over %s", am.rec.MountPoint, strings.ToLower(p.Name))

		err = fuse.Mount(ctx, am.rec.MountPoint, backend, fuse.Options{
			Name:     filepath.Base(am.rec.MountPoint),
			ReadOnly: am.rec.ReadOnly,
		})
		closeBackend(backend)

		if ctx.Err() != nil {
			log.Printf("client: %s: unmounted", am.rec.MountPoint)
			return
		}

		am.setStatus(stateRetrying, "")
		log.Printf("client: %s: backend dropped (%v), reconnecting", am.rec.MountPoint, err)
		if !sleep(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// resolve fetches the peer's /config and picks a protocol for rec.
func (m *mountManager) resolve(ctx context.Context, rec mountRecord) (protocolInfo, error) {
	protos, err := fetchProtocols(ctx, rec.Peer, m.cc.ServerPort)
	if err != nil {
		return protocolInfo{}, err
	}

	p, ok := chooseProtocol(rec.Protocol, protos, m.cc.ProtocolPreference)
	if !ok {
		return protocolInfo{}, fmt.Errorf("peer %s offers no usable %s protocol", rec.Peer, rec.Protocol)
	}
	return p, nil
}

func nextBackoff(d time.Duration) time.Duration {
	if d *= 2; d > backoffMax {
		return backoffMax
	}
	return d
}

// sleep waits d or until ctx is done; it returns false if ctx was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// closeBackend closes b if it holds resources. vfs.FS has no Close, so this
// is a type assertion — sftpclient.FS implements io.Closer, webdavclient.FS
// does not.
func closeBackend(b vfs.FS) {
	if c, ok := b.(io.Closer); ok {
		_ = c.Close()
	}
}
