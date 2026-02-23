// Package app wires the consensus node, state machine, and transports together.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"

	"google.golang.org/grpc"

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

// Run starts consensus and gRPC servers and blocks until shutdown or fatal error.
func (a *App) Run(ctx context.Context) error {
	a.consensus.Run(ctx)

	kvLis, err := net.Listen("tcp", a.config.KVGRPCAddr)
	if err != nil {
		return fmt.Errorf("listen kv grpc %s: %w", a.config.KVGRPCAddr, err)
	}
	defer func() { _ = kvLis.Close() }()

	consensusLis, err := net.Listen("tcp", a.config.ConsensusGRPCAddr)
	if err != nil {
		return fmt.Errorf("listen consensus grpc %s: %w", a.config.ConsensusGRPCAddr, err)
	}
	defer func() { _ = consensusLis.Close() }()

	adminLis, err := net.Listen("tcp", a.config.AdminGRPCAddr)
	if err != nil {
		return fmt.Errorf("listen admin grpc %s: %w", a.config.AdminGRPCAddr, err)
	}
	defer func() { _ = adminLis.Close() }()

	a.logger.Info(
		"node started",
		"node_id", a.config.NodeID,
		"consensus_type", a.config.ConsensusType,
		"kv_grpc_addr", a.config.KVGRPCAddr,
		"admin_grpc_addr", a.config.AdminGRPCAddr,
		"consensus_grpc_addr", a.config.ConsensusGRPCAddr,
	)

	return a.serve(ctx, kvLis, adminLis, consensusLis)
}

// serve registers gRPC services, starts goroutines, and blocks until ctx is
// canceled or a fatal error occurs.
func (a *App) serve(ctx context.Context, kvLis, adminLis, raftLis net.Listener) error {
	kvServer := grpc.NewServer()
	kvpb.RegisterKVServiceServer(kvServer, kvgrpc.NewServer(a.kv))

	adminServer := grpc.NewServer()
	adminpb.RegisterAdminServiceServer(adminServer, a.adminSrv)

	raftServer := grpc.NewServer()
	raftpb.RegisterRaftServiceServer(raftServer, a.raftSrv)

	errCh := make(chan error, 4)

	go func() {
		if err := a.kv.RunApplyLoop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- fmt.Errorf("kv apply loop: %w", err)
		}
	}()
	go func() {
		if err := kvServer.Serve(kvLis); err != nil {
			errCh <- fmt.Errorf("kv grpc serve: %w", err)
		}
	}()
	go func() {
		if err := adminServer.Serve(adminLis); err != nil {
			errCh <- fmt.Errorf("admin grpc serve: %w", err)
		}
	}()
	go func() {
		if err := raftServer.Serve(raftLis); err != nil {
			errCh <- fmt.Errorf("consensus grpc serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		raftServer.GracefulStop()
		adminServer.GracefulStop()
		kvServer.GracefulStop()
		return nil
	case err := <-errCh:
		raftServer.Stop()
		adminServer.Stop()
		kvServer.Stop()
		return err
	}
}
