package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// clientConfig holds the client's own settings, kept under
// <user-config-dir>/taildrive/config.toml. The client does not read the
// server's config.toml.
type clientConfig struct {
	TailscaleSocket    string   `toml:"tailscale_socket"`
	ServerPort         int      `toml:"server_port"`         // API port serving GET /config
	ProtocolPreference []string `toml:"protocol_preference"` // order an "auto" mount prefers
	PollInterval       int      `toml:"poll_interval"`       // /config re-check, ms; <=0 disables
}

func (c *clientConfig) applyDefaults() {
	if c.TailscaleSocket == "" {
		c.TailscaleSocket = "/var/run/tailscale/tailscaled.sock"
	}
	if c.ServerPort == 0 {
		c.ServerPort = 8080
	}
	if len(c.ProtocolPreference) == 0 {
		c.ProtocolPreference = []string{"webdav", "sftp"}
	}
	if c.PollInterval == 0 {
		c.PollInterval = 60000
	}
}

// mountRecord is one persisted mount. The live vfs.FS is built from it at
// mount time and is never stored.
type mountRecord struct {
	MountPoint string `toml:"mountpoint"`
	Peer       string `toml:"peer"`
	Protocol   string `toml:"protocol"` // "auto" | "sftp" | "webdav"
	ReadOnly   bool   `toml:"read_only"`
}

type mountsFile struct {
	Mount []mountRecord `toml:"mount"`
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "taildrive")
	return dir, os.MkdirAll(dir, 0o755)
}

// loadClientConfig reads config.toml, writing a default file if none exists.
func loadClientConfig() (clientConfig, error) {
	dir, err := configDir()
	if err != nil {
		return clientConfig{}, err
	}
	path := filepath.Join(dir, "config.toml")

	var cc clientConfig
	_, err = toml.DecodeFile(path, &cc)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return clientConfig{}, err
	}

	cc.applyDefaults()

	if missing {
		if werr := writeTOML(path, cc); werr != nil {
			return clientConfig{}, werr
		}
	}
	return cc, nil
}

// loadMounts reads mounts.toml, returning an empty list if none exists.
func loadMounts() ([]mountRecord, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "mounts.toml")

	var mf mountsFile
	if _, err := toml.DecodeFile(path, &mf); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for i := range mf.Mount {
		p := strings.ToLower(strings.TrimSpace(mf.Mount[i].Protocol))
		if p == "" {
			p = "auto"
		}
		mf.Mount[i].Protocol = p
	}
	return mf.Mount, nil
}

// saveMounts writes mounts.toml atomically.
func saveMounts(records []mountRecord) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	return writeTOML(filepath.Join(dir, "mounts.toml"), mountsFile{Mount: records})
}

// writeTOML encodes v to path atomically: a temp file in the same directory,
// then rename over the target.
func writeTOML(path string, v any) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := toml.NewEncoder(tmp).Encode(v); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
