package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Port            int     `toml:"port"`
	TailscaleSocket string  `toml:"tailscale_socket"`
	Shares          []Share `toml:"share"`
}

type Share struct {
	Name     string `toml:"name"`
	Path     string `toml:"path"`
	ReadOnly bool   `toml:"read_only"`
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{
		Port:            8080,
		TailscaleSocket: "/var/run/tailscale/tailscaled.sock",
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (cfg *Config) validate() error {
	for _, share := range cfg.Shares {
		if share.Name == "" {
			return fmt.Errorf("share %s has no name", share.Path)
		}

		if share.Path == "" {
			return fmt.Errorf("share %s has no path", share.Name)
		}
		if _, err := os.Stat(share.Path); err != nil {
			return fmt.Errorf("share %s path %s does not exist", share.Name, share.Path)
		}
	}
	return nil
}

func (cfg *Config) ShareMap() map[string]Share {
	m := make(map[string]Share)
	for _, share := range cfg.Shares {
		m[share.Name] = share
	}
	return m
}
