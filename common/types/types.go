package types

import (
	"a3l6/m/config"
	"a3l6/m/vfs"
	"context"
)

// ReadyFunc is called by a protocol's Run once its listener is up, with the
// address a client should dial to reach it.
type ReadyFunc func(host string, port int)

type GenericProtocolServer struct {
	Name    string
	Enabled bool
	Host    string
	Port    int
	Run     func(context.Context, vfs.FS, *config.Config, ReadyFunc) error
}

/*
func (s GenericProtocolServer) Name() string           { return s.name }
func (s GenericProtocolServer) Enabled() bool          { return s.enabled }
func (s *GenericProtocolServer) SetEnabled(x bool)     { s.enabled = x }
func (s GenericProtocolServer) Public() map[string]any { return s.public }
func (s *GenericProtocolServer) Run(ctx context.Context, fs vfs.FS) error {
	return s.run(ctx, fs)
}
*/
