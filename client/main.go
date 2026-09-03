package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	cc, err := loadClientConfig()
	if err != nil {
		log.Fatal("client: cannot load config: ", err)
	}

	mounts, err := loadMounts()
	if err != nil {
		log.Fatal("client: cannot load mounts: ", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mgr := newMountManager(ctx, cc)
	for _, rec := range mounts {
		if err := mgr.Start(rec); err != nil {
			log.Printf("client: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollConfig(ctx, mgr)
	}()

	go runREPL(ctx, &repl{mgr: mgr, stop: stop, mounts: mounts})

	log.Printf("client: supervising %d mount(s); Ctrl-C to unmount", len(mounts))
	<-ctx.Done()
	log.Print("client: shutting down")
	mgr.StopAll()
	wg.Wait()
}
