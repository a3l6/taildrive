package main

import (
	tdsftp "a3l6/m/sftp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

type ProtocolConfig interface {
	Name() string
	Enabled() bool
	Public() map[string]any
	Run(ctx context.Context) error
}

type Registry struct {
	configs []ProtocolConfig

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

func (r *Registry) Get(name string) ProtocolConfig {
	for _, val := range r.configs {
		if val.Name() == name {
			return val
		}
	}
	return nil
}

func (r *Registry) Start(name string, s ProtocolConfig) error {
	ctx, cancel := context.WithCancel(context.Background())

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

		if err := s.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("%s service failed: %s", name, err)
		}
	}()

	return nil

}

func (r *Registry) Stop(name string) {
	r.muregistrar.Lock()
	cancel, ok := r.registrar[name]
	r.muregistrar.Unlock()
	if !ok {
		cancel()
	}
}

func (r *Registry) StopAll() {
	r.muregistrar.Lock()
	for _, cancel := range r.registrar {
		cancel()
	}
	r.muregistrar.Unlock()
	r.wgregistrar.Wait()
}

func (r *Registry) UnRegister(c ProtocolConfig) {
	for idx, val := range r.configs {
		if val.Name() == c.Name() {
			r.configs = append(r.configs[:idx], r.configs[idx+1:]...)
		}
	}
}

func (r *Registry) Register(c ProtocolConfig) { r.configs = append(r.configs, c) }

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	out := make([]map[string]any, 0, len(registry.configs))
	for _, c := range registry.configs {
		out = append(out, map[string]any{
			"name":    c.Name(),
			"enabled": c.Enabled(),
			"public":  c.Public(),
		})
	}
	json.NewEncoder(w).Encode(map[string]any{"protocols": out})
}

func generateAvailableProtocols(peers []PeerShares, cfg Config) map[string]int {
	availableProtocols := make(map[string]int)

	for _, peer := range peers {
		resp, error := http.Get(fmt.Sprintf("http://%s:%d/config", peer.PeerIP, cfg.Port))
		if error != nil {
			log.Println(error)
			continue
		}
		defer resp.Body.Close()

		var config map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
			log.Println(err)
			continue
		}

		for _, protocol := range config["protocols"].([]any) {
			if protocol.(map[string]any)["enabled"] == true {
				availableProtocols[protocol.(map[string]any)["name"].(string)]++
			}
		}
	}

	log.Println("found protocols:", availableProtocols)

	return availableProtocols
}

func autoConfigureProtocols(availableProtocols map[string]int, cfg Config) {
	if cfg.AutoProtocols == nil || *cfg.AutoProtocols == false {
		return
	}

	switch *cfg.AutoProtocolsStyle {
	case ProtocolStyleCompatibility:
		for protocol := range availableProtocols {
			resolved := registry.Get(protocol)
			if resolved == nil {
				log.Printf("cannot find protocol %s on machine, skipping", protocol)
				continue
			}

			registry.Start(protocol, resolved)
		}
	case ProtocolStyleMostCompatible:
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
			log.Printf("cannot find protocol %s on machine, skipping", mostCompatibleProtocol)
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

type GenericProtocolServer struct {
	name    string
	enabled bool
	public  map[string]any
	run     func(context.Context) error
}

func (s GenericProtocolServer) Name() string {
	return s.name
}

func (s GenericProtocolServer) Enabled() bool {
	return s.enabled
}

func (s GenericProtocolServer) Public() map[string]any {
	return s.public
}

func (s *GenericProtocolServer) Run(ctx context.Context) error {
	return s.run(ctx)
}

var SFTPProtocolServer GenericProtocolServer = GenericProtocolServer{
	name:    "SFTP",
	enabled: true, // TODO: Hook this up to config struct
	public:  make(map[string]any),
	run:     tdsftp.Run,
}

func (r *Registry) init() {
	r.Register(&SFTPProtocolServer)

	if SFTPProtocolServer.enabled {
		r.Start("SFTP", &SFTPProtocolServer)
	}
}
