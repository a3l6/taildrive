package main

import (
	"context"
	"fmt"
	"tailscale.com/client/tailscale"
)

func main() {
	var lc tailscale.LocalClient
	st, err := lc.Status(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(st.Self.DNSName)  // full name
	fmt.Println(st.Self.HostName) // short name
}
