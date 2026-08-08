package taildrive_sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"log"
	"net"
	"os"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

func Run(ctx context.Context) error {
	srv := &tsnet.Server{
		Hostname: "sftp-vfs",
		// AuthKey:  // os.Getenv("TS_AUTHKEY"),
	}
	defer srv.Close()

	lc, err := srv.LocalClient()
	if err != nil {
		return err
	}

	sshConfig := &ssh.ServerConfig{
		NoClientAuth: true,
	}

	hostKey, err := loadOrCreateHostKey("host_key")
	if err != nil {
		return err
	}

	sshConfig.AddHostKey(hostKey)

	ln, err := srv.Listen("tcp", ":22")
	if err != nil {
		return err
	}
	defer ln.Close()

	// Unblocks the Accept below when the registry cancels us.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	log.Println("sftp-vfs listening on the tailnet :22")

	var conns sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}

		conns.Add(1)
		go func() {
			defer conns.Done()
			handleConn(ctx, conn, sshConfig, lc)
		}()
	}

	conns.Wait()
	log.Println("sftp-vfs stopped")
	return ctx.Err()
}

func handleConn(ctx context.Context, nConn net.Conn, sshConfig *ssh.ServerConfig, lc *local.Client) {
	login := "anonymous"
	if who, err := lc.WhoIs(ctx, nConn.RemoteAddr().String()); err == nil {
		login = who.UserProfile.LoginName
	}

	fs := newVFS(mountsFor(login))

	sconn, chans, reqs, err := ssh.NewServerConn(nConn, sshConfig)
	if err != nil {
		return
	}

	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	// Tears down the session on cancellation; the derived context keeps this
	// goroutine from outliving the connection.
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-connCtx.Done()
		sconn.Close()
	}()

	var sessions sync.WaitGroup
	defer sessions.Wait()

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}

		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}

		go func() {
			for req := range requests {
				ok := req.Type == "subsystem" &&
					len(req.Payload) >= 4 &&
					string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
			}
		}()

		server := sftp.NewRequestServer(ch, sftp.Handlers{
			FileGet:  fs,
			FilePut:  fs,
			FileCmd:  fs,
			FileList: fs,
		})

		sessions.Add(1)
		go func() {
			defer sessions.Done()
			if err := server.Serve(); err != nil && err != io.EOF {
				log.Println("sftp serve:", err)
			}
			server.Close()
		}()
	}
}

func mountsFor(login string) map[string]string {
	return map[string]string{
		"/projects": "/tmp",
	}
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(data)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
	return ssh.NewSignerFromSigner(priv)
}
