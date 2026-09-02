package types

import (
	"a3l6/m/vfs"
	"context"
)

type GenericProtocolServer struct {
	Name    string
	Enabled bool
	Port    int
	Run     func(context.Context, vfs.FS) error
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
