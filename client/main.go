package main

import (
	"fmt"
	"log"
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

	fmt.Printf("client: server API port %d, %d mount(s) configured\n", cc.ServerPort, len(mounts))
	for _, m := range mounts {
		fmt.Printf("  %s\t%s\t%s\n", m.MountPoint, m.Peer, m.Protocol)
	}
}
