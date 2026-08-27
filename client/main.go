package main

import (
	"a3l6/m/common/ts"
	"a3l6/m/common/types"
	"a3l6/m/config"
	"a3l6/m/fuse"
	"a3l6/m/vfs"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

func main() {
	c, err := loadConfig()
	if err != nil {
		log.Fatal("client: cannot load config. ", err)
	}

	for {
		fmt.Println("show mounts: 0\n")
		fmt.Println("enter desired key: ")

		var desired string
		fmt.Scanln(&desired)

		if desired == "0" {
			fmt.Println(c.Mounts)
		}
	}
}

// I want to have a tmux like config??
// Basically just store it in ~/.config/taildrive

type mount struct {
	MountPoint string       `json:"path"`
	Backend    vfs.FS       `json:"backend"`
	Options    fuse.Options `json:"options"`
}

type mountConfig struct {
	MountPoint string       `json:"path"`
	Options    fuse.Options `json:"options"`
}

/*
	return fuse.Mount(ctx, cfg.Fuse.Mountpoint, backend, fuse.Options{
		Name:     cfg.Fuse.Share,
		ReadOnly: cfg.Fuse.ReadOnly,
	})
*/

type cfg struct {
	Mounts []mount `json:"mounts"`
}

// loadConfig() loads configs from ~/.config/taildrive and creates empty configs if missing
func loadConfig() (cfg, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg{}, err
	}

	configPath := fmt.Sprintf("%s/.config/taildrive", home)
	err = os.MkdirAll(configPath, 0755)
	if err != nil {
		return cfg{}, err
	}

	log.Println("client: opening config file at %s", configPath)
	file, err := os.Open(fmt.Sprintf("%s/config.json", configPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			file, err = os.Create(fmt.Sprintf("%s/config.json", configPath))
			if err != nil {
				return cfg{}, err
			}
		} else {
			return cfg{}, err
		}
	}

	var config cfg

	if err := json.NewDecoder(file).Decode(&config); err != nil {
		if errors.Is(err, io.EOF) {
			config = cfg{Mounts: make([]mount, 0)}
		} else {
			log.Println("client: cannot unmarshall json from ", configPath)
			return cfg{}, err
		}
	}

	log.Println("client: loaded config from ", configPath)
	return config, nil
}

// discoverServers(context.Context, *config.Config) pings configs from all devices on the tailnet it then returns a []types.GenericProtocolServer of all the discovered servers
func discoverServers(ctx context.Context, cfg *config.Config) ([]types.GenericProtocolServer, error) {
	peers, err := ts.ListPeers(ctx, cfg)
	if err != nil {
		return []types.GenericProtocolServer{}, err
	}

	var discovered []types.GenericProtocolServer
	for _, peer := range peers {
		resp, err := http.Get(fmt.Sprintf("http://%s:%d/config", peer, cfg.Port))
		if err != nil {
			// TODO: implement some exponential backoff here
			log.Println("client: could not reach config for ", peer)
			continue
		}

		defer resp.Body.Close()

		var cfg struct {
			Protocols map[string]types.GenericProtocolServer `json:"protocols"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
			log.Println("protocol: ERROR: ", err)
			continue
		}

		for _, val := range cfg.Protocols {
			discovered = append(discovered, val)
		}
	}

	return discovered, nil
}
