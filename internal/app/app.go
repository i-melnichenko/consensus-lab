// Package app wires the consensus node, state machine, and transports together.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
	"github.com/i-melnichenko/consensus-lab/internal/service"
	kvgrpc "github.com/i-melnichenko/consensus-lab/internal/transport/grpc/kv"
	adminpb "github.com/i-melnichenko/consensus-lab/pkg/proto/adminv1"
	kvpb "github.com/i-melnichenko/consensus-lab/pkg/proto/kvv1"
	raftpb "github.com/i-melnichenko/consensus-lab/pkg/proto/raftv1"
)

// Logger is the logging interface required by App.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// App wires consensus and the KV state machine into a runnable service.
// All dependencies are injected; App does not create transport connections.
type App struct {
	config    Config
	logger    Logger
	consensus consensus.Consensus
	kv        *service.KV
	raftSrv   raftpb.RaftServiceServer
	adminSrv  adminpb.AdminServiceServer
}

// New validates dependencies and constructs a runnable application.
func New(
	cfg Config,
	logger Logger,
	c consensus.Consensus,
	kvSvc *service.KV,
	raftSrv raftpb.RaftServiceServer,
	adminSrv adminpb.AdminServiceServer,
) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		return nil, fmt.Errorf("app: nil logger")
	}
	if c == nil {
		return nil, fmt.Errorf("app: nil consensus")
	}
	if kvSvc == nil {
		return nil, fmt.Errorf("app: nil kv service")
	}
	if raftSrv == nil {
		return nil, fmt.Errorf("app: nil raft server")
	}
	if adminSrv == nil {
		return nil, fmt.Errorf("app: nil admin server")
	}
	return &App{
		config:    cfg,
		logger:    logger,
		consensus: c,
		kv:        kvSvc,
		raftSrv:   raftSrv,
		adminSrv:  adminSrv,
	}, nil
}

// Stop stops the underlying consensus engine.
func (a *App) Stop() {
	a.consensus.Stop()
}

// Run starts consensus and a shared gRPC server and blocks until shutdown or fatal error.
func (a *App) Run(ctx context.Context) error {
	a.consensus.Run(ctx)

	lis, err := net.Listen("tcp", a.config.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen grpc %s: %w", a.config.GRPCAddr, err)
	}
	defer func() { _ = lis.Close() }()

	a.logger.Info(
		"node started",
		"node_id", a.config.NodeID,
		"consensus_type", a.config.ConsensusType,
		"grpc_addr", a.config.GRPCAddr,
	)

	return a.serve(ctx, lis)
}

// serve registers gRPC services, starts goroutines, and blocks until ctx is
// canceled or a fatal error occurs.
func (a *App) serve(ctx context.Context, lis net.Listener) error {
	server := grpc.NewServer()
	kvpb.RegisterKVServiceServer(server, kvgrpc.NewServer(a.kv))
	adminpb.RegisterAdminServiceServer(server, a.adminSrv)
	raftpb.RegisterRaftServiceServer(server, a.raftSrv)
	reflection.Register(server)

	errCh := make(chan error, 2)

	go func() {
		if err := a.kv.RunApplyLoop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- fmt.Errorf("kv apply loop: %w", err)
		}
	}()
	go func() {
		if err := server.Serve(lis); err != nil {
			errCh <- fmt.Errorf("grpc serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		server.GracefulStop()
		return nil
	case err := <-errCh:
		server.Stop()
		return err
	}
}
