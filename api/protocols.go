package main

import (
	"a3l6/m/common/ts"
	"a3l6/m/common/types"
	conf "a3l6/m/config"
	"a3l6/m/sftp"
	"a3l6/m/webdav"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// TODO: Delete Protocol interface entirely
/*
type Protocol interface {
	Name() string
	Enabled() bool
	SetEnabled(bool)
	Public() map[string]any
	Run(context.Context, vfs.FS) error
}
*/

type Registry struct {
	configs     []*types.GenericProtocolServer
	muregistrar sync.Mutex
	registrar   map[string]context.CancelFunc
	wgregistrar sync.WaitGroup
}

func newRegistry() *Registry {
	return &Registry{
		registrar: make(map[string]context.CancelFunc),
	}
}

var registry = newRegistry()

func (r *Registry) Get(name string) *types.GenericProtocolServer {
	for _, val := range r.configs {
		if val.Name == name {
			return val
		}
	}
	return nil
}

func (r *Registry) Start(name string, s *types.GenericProtocolServer) error {
	ctx, cancel := context.WithCancel(context.Background())

	log.Println("protocol: starting ", name, " service")

	r.muregistrar.Lock()
	if _, exists := r.registrar[name]; exists {
		r.muregistrar.Unlock()
		cancel()
		return fmt.Errorf("%s service already running", name)
	}

	r.registrar[name] = cancel
	r.muregistrar.Unlock()

	r.wgregistrar.Add(1)
	go func() {
		defer r.wgregistrar.Done()
		defer func() {
			r.muregistrar.Lock()
			delete(r.registrar, name)
			r.muregistrar.Unlock()
		}()

		if protocol := r.Get(name); protocol != nil {
			protocol.Enabled = true
		}

		if err := s.Run(ctx, filesystem); err != nil && err != context.Canceled {
			log.Printf("protocol: %s service failed: %s", name, err)
			if protocol := r.Get(name); protocol != nil {
				protocol.Enabled = true
			}
		}
	}()

	return nil

}

func (r *Registry) Stop(name string) {
	r.muregistrar.Lock()
	for _, config := range r.configs {
		if config.Name == name {
			config.Enabled = false
		}
	}

	cancel, ok := r.registrar[name]
	r.muregistrar.Unlock()
	if ok {
		cancel()
	}
}

func (r *Registry) StopAll() {
	r.muregistrar.Lock()
	for _, cancel := range r.registrar {
		cancel()
	}

	for _, config := range r.configs {
		config.Enabled = false
	}
	r.muregistrar.Unlock()
	r.wgregistrar.Wait()
}

func (r *Registry) UnRegister(c *types.GenericProtocolServer) {
	for idx, val := range r.configs {
		if val.Name == c.Name {
			r.configs = append(r.configs[:idx], r.configs[idx+1:]...)
		}
	}
}

func (r *Registry) Register(c *types.GenericProtocolServer) { r.configs = append(r.configs, c) }

func handleConfig(apiHost string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		out := make([]map[string]any, 0, len(registry.configs))
		for _, c := range registry.configs {
			out = append(out, map[string]any{
				"name":    c.Name,
				"enabled": c.Enabled,
				"host":    protocolHost(c.Name, apiHost),
				"port":    c.Port,
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"protocols": out})
	}
}

// protocolHost is the tailnet host a client should dial to reach the named
// protocol. WebDAV listens on the API host; SFTP runs on its own tsnet node.
func protocolHost(name, apiHost string) string {
	if name == SFTPProtocolServer.Name {
		return ts.SFTPHostname(apiHost)
	}
	return apiHost
}

func generateAvailableProtocols(peers []ts.PeerShares, cfg conf.Config) map[string]int {
	availableProtocols := make(map[string]int)

	for _, peer := range peers {
		resp, error := http.Get(fmt.Sprintf("http://%s:%d/config", peer.PeerIP, cfg.Port))
		if error != nil {
			log.Println("protocol: ERROR: ", error)
			continue
		}
		defer resp.Body.Close()

		var config map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
			log.Println("protocol: ERROR: ", err)
			continue
		}

		for _, protocol := range config["protocols"].([]any) {
			if protocol.(map[string]any)["enabled"] == true {
				availableProtocols[protocol.(map[string]any)["name"].(string)]++
			}
		}
	}

	log.Println("protocol: found protocols:", availableProtocols)

	return availableProtocols
}

func autoConfigureProtocols(availableProtocols map[string]int, cfg conf.Config) {
	if cfg.ProtocolConfig.AutoProtocols == nil || *cfg.ProtocolConfig.AutoProtocols == false {
		return
	}

	switch *cfg.ProtocolConfig.AutoProtocolsStyle {
	case conf.ProtocolStyleCompatibility:
		for protocol := range availableProtocols {
			resolved := registry.Get(protocol)
			if resolved == nil {
				log.Printf("protocol: cannot find protocol %s on machine, skipping", protocol)
				continue
			}

			registry.Start(protocol, resolved)
		}
	case conf.ProtocolStyleMostCompatible:
		var mostCompatibleProtocol string
		var bestCompatibility int

		for idx, val := range availableProtocols {
			if val > bestCompatibility {
				mostCompatibleProtocol = idx
				bestCompatibility = val
			}
		}

		resolved := registry.Get(mostCompatibleProtocol)
		if resolved == nil {
			log.Printf("protocol: cannot find protocol %s on machine, skipping", mostCompatibleProtocol)
			return
		}

		for idx := range registry.registrar {
			if idx == mostCompatibleProtocol {
				continue
			}
			registry.Stop(idx)
		}

		registry.Start(mostCompatibleProtocol, resolved)
	}
}

// File Transfer Server Wrappers

var SFTPProtocolServer types.GenericProtocolServer = types.GenericProtocolServer{
	Name:    "SFTP",
	Enabled: false,
	Run:     sftp.Run,
}

var WEBDAVProtocolServer types.GenericProtocolServer = types.GenericProtocolServer{
	Name:    "WEBDAV",
	Enabled: false,
	Run:     webdav.Run,
}

// Inits all protocol servers. Empty cfg gets default config. See `config.Config{}.ApplyDefaults`
func (r *Registry) init(cfg *conf.Config) {
	if cfg == nil {
		cfg.ApplyDefaults()
	}

	SFTPProtocolServer.Port = cfg.Sftp.Port
	WEBDAVProtocolServer.Port = cfg.Webdav.Port

	r.Register(&SFTPProtocolServer)
	r.Register(&WEBDAVProtocolServer)

	mapping := make(map[string]func())
	mapping[SFTPProtocolServer.Name] = func() { registry.Start(SFTPProtocolServer.Name, &SFTPProtocolServer) }
	mapping[WEBDAVProtocolServer.Name] = func() { registry.Start(WEBDAVProtocolServer.Name, &WEBDAVProtocolServer) }

	log.Println("protocol: enabled protocols = ", cfg.ProtocolConfig.ProtocolsEnabled)
	for _, val := range cfg.ProtocolConfig.ProtocolsEnabled {
		if fn, ok := mapping[val]; ok {
			log.Println("protocol: enabling ", val)
			fn()
		}
	}

}
