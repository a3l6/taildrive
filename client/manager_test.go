package main

import (
	"context"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"a3l6/m/vfs"
)

// stubFS satisfies vfs.FS and nothing more. It stands in for a backend with no
// transport to watch, the way webdavclient.FS has none.
type stubFS struct{}

func (stubFS) Stat(context.Context, string) (vfs.Entry, error)                  { return vfs.Entry{}, nil }
func (stubFS) ReadDir(context.Context, string) ([]vfs.Entry, error)             { return nil, nil }
func (stubFS) Open(context.Context, string, int, os.FileMode) (vfs.File, error) { return nil, nil }
func (stubFS) Mkdir(context.Context, string, os.FileMode) error                 { return nil }
func (stubFS) Remove(context.Context, string) error                             { return nil }
func (stubFS) Rename(context.Context, string, string) error                     { return nil }
func (stubFS) Truncate(context.Context, string, int64) error                    { return nil }

// waitFS adds the liveness signal, the way sftpclient.FS does. Sending on dead
// stands for the transport shutting down.
type waitFS struct {
	stubFS
	dead chan error
}

func (w *waitFS) Wait() error { return <-w.dead }

func TestWatchBackendIgnoresBackendThatCannotReportLiveness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchBackend(ctx, "/mnt/test", stubFS{}, cancel)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchBackend blocked on a backend with no Wait method")
	}

	if ctx.Err() != nil {
		t.Fatal("watchBackend unmounted a backend that never reported a death")
	}
}

func TestWatchBackendCancelsOnDeadTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := &waitFS{dead: make(chan error, 1)}
	go watchBackend(ctx, "/mnt/test", b, cancel)

	b.dead <- io.EOF

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("a dead transport did not cancel the mount")
	}
}

func TestWatchBackendReturnsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	b := &waitFS{dead: make(chan error, 1)}
	before := runtime.NumGoroutine()

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchBackend(ctx, "/mnt/test", b, cancel)
	}()

	cancel() // the user is shutting the client down

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchBackend did not return when its mount was cancelled")
	}

	// supervise closes the backend next, which unblocks Wait. Nobody is
	// receiving by now, so an unbuffered hand-off would strand that goroutine
	// for the life of the process — one leaked per remount.
	b.dead <- io.EOF

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.NumGoroutine() > before {
		t.Fatal("watchBackend leaked the goroutine waiting on the transport")
	}
}
