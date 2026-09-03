package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// repl is a small interactive control surface over the mount manager. It owns
// the canonical record list and writes mounts.toml on every mutation.
type repl struct {
	mgr    *mountManager
	stop   context.CancelFunc
	mounts []mountRecord
}

const replUsage = "commands: list | start <mp> | stop <mp> | restart <mp> | " +
	"add <mp> <peer> [auto|sftp|webdav] | remove <mp> | switch <mp> <proto> | discover | quit"

func runREPL(ctx context.Context, r *repl) {
	sc := bufio.NewScanner(os.Stdin)
	fmt.Println(replUsage)

	for {
		fmt.Print("taildrive> ")
		if !sc.Scan() {
			return
		}

		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if r.dispatch(fields) {
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (r *repl) dispatch(fields []string) (quit bool) {
	cmd, args := fields[0], fields[1:]

	switch cmd {
	case "list":
		r.list()
	case "start":
		if len(args) != 1 {
			fmt.Println("usage: start <mountpoint>")
			return
		}
		rec, ok := r.find(args[0])
		if !ok {
			fmt.Println("no such mount:", args[0])
			return
		}
		if err := r.mgr.Start(rec); err != nil {
			fmt.Println(err)
		}
	case "stop":
		if len(args) != 1 {
			fmt.Println("usage: stop <mountpoint>")
			return
		}
		r.mgr.Stop(args[0])
	case "restart":
		if len(args) != 1 {
			fmt.Println("usage: restart <mountpoint>")
			return
		}
		if err := r.mgr.Restart(args[0]); err != nil {
			fmt.Println(err)
		}
	case "add":
		if len(args) < 2 || len(args) > 3 {
			fmt.Println("usage: add <mountpoint> <peer> [auto|sftp|webdav]")
			return
		}
		r.add(args)
	case "remove":
		if len(args) != 1 {
			fmt.Println("usage: remove <mountpoint>")
			return
		}
		r.remove(args[0])
	case "switch":
		if len(args) != 2 {
			fmt.Println("usage: switch <mountpoint> <auto|sftp|webdav>")
			return
		}
		r.switchProto(args[0], args[1])
	case "discover":
		r.discover()
	case "quit", "exit":
		r.stop()
		return true
	default:
		fmt.Println("unknown command:", cmd)
	}
	return false
}

func (r *repl) list() {
	active := make(map[string]mountStatus)
	for _, s := range r.mgr.List() {
		active[s.Record.MountPoint] = s
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "MOUNTPOINT\tPEER\tINTENT\tRESOLVED\tSTATE")
	for _, m := range r.mounts {
		resolved, state := "-", "-"
		if s, ok := active[m.MountPoint]; ok {
			state = s.State.String()
			if s.Resolved != "" {
				resolved = s.Resolved
			}
		}
		mp := m.MountPoint
		if m.ReadOnly {
			mp += " (ro)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", mp, m.Peer, m.Protocol, resolved, state)
	}
	tw.Flush()
}

func (r *repl) add(args []string) {
	proto := "auto"
	if len(args) == 3 {
		proto = strings.ToLower(args[2])
	}
	if !validProto(proto) {
		fmt.Println("protocol must be auto, sftp or webdav")
		return
	}

	rec := mountRecord{MountPoint: args[0], Peer: args[1], Protocol: proto}
	if _, exists := r.find(rec.MountPoint); exists {
		fmt.Println("mount already configured:", rec.MountPoint)
		return
	}

	r.mounts = append(r.mounts, rec)
	if err := saveMounts(r.mounts); err != nil {
		fmt.Println("save:", err)
		return
	}
	if err := r.mgr.Start(rec); err != nil {
		fmt.Println(err)
	}
}

func (r *repl) remove(mp string) {
	idx := r.index(mp)
	if idx < 0 {
		fmt.Println("no such mount:", mp)
		return
	}

	r.mgr.Remove(mp)
	r.mounts = append(r.mounts[:idx], r.mounts[idx+1:]...)
	if err := saveMounts(r.mounts); err != nil {
		fmt.Println("save:", err)
	}
}

func (r *repl) switchProto(mp, proto string) {
	proto = strings.ToLower(proto)
	if !validProto(proto) {
		fmt.Println("protocol must be auto, sftp or webdav")
		return
	}

	idx := r.index(mp)
	if idx < 0 {
		fmt.Println("no such mount:", mp)
		return
	}

	r.mgr.Stop(mp)
	r.mounts[idx].Protocol = proto
	if err := saveMounts(r.mounts); err != nil {
		fmt.Println("save:", err)
		return
	}
	if err := r.mgr.Start(r.mounts[idx]); err != nil {
		fmt.Println(err)
	}
}

func (r *repl) discover() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Println("scanning the tailnet...")
	servers, err := discoverServers(ctx, r.mgr.cc.TailscaleSocket, r.mgr.cc.ServerPort)
	if err != nil {
		fmt.Println("discover:", err)
		return
	}
	if len(servers) == 0 {
		fmt.Println("no taildrive servers found")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PEER\tPROTOCOL\tENABLED\tHOST\tPORT")
	for _, s := range servers {
		for _, p := range s.Protocols {
			fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%d\n", s.Peer, p.Name, p.Enabled, p.Host, p.Port)
		}
	}
	tw.Flush()
}

func (r *repl) find(mp string) (mountRecord, bool) {
	if i := r.index(mp); i >= 0 {
		return r.mounts[i], true
	}
	return mountRecord{}, false
}

func (r *repl) index(mp string) int {
	for i, m := range r.mounts {
		if m.MountPoint == mp {
			return i
		}
	}
	return -1
}

func validProto(p string) bool {
	return p == "auto" || p == "sftp" || p == "webdav"
}
