// Package main implements the node process that runs Raft and the KV gRPC API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	apppkg "github.com/i-melnichenko/consensus-lab/internal/app"
	"github.com/i-melnichenko/consensus-lab/internal/consensus"
	raftconsensus "github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	"github.com/i-melnichenko/consensus-lab/internal/kv"
	obsmetrics "github.com/i-melnichenko/consensus-lab/internal/observability/metrics"
	"github.com/i-melnichenko/consensus-lab/internal/service"
	admingrpc "github.com/i-melnichenko/consensus-lab/internal/transport/grpc/admin"
	raftgrpc "github.com/i-melnichenko/consensus-lab/internal/transport/grpc/raft"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "node: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := apppkg.LoadConfigFromEnv()
	if err != nil {
		return err
	}

	slog.SetDefault(newLogger(cfg.LogLevel))
	logger := slog.Default()

	peerAddrs, err := cfg.PeerAddrMap()
	if err != nil {
		return err
	}
	delete(peerAddrs, cfg.NodeID) // exclude self if listed

	peers, err := raftgrpc.DialPeers(
		peerAddrs,
		otel.Tracer("consensus-lab/internal/transport/grpc/raft"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}

	applyCh := make(chan consensus.ApplyMsg, 256)
	store := kv.NewStore(otel.Tracer("consensus-lab/internal/kv"))
	storage := raftconsensus.NewJSONStorage(cfg.DataDir)
	promMetrics, err := obsmetrics.NewPrometheus(nil)
	if err != nil {
		return err
	}

	node, err := raftconsensus.NewNode(
		cfg.NodeID,
		peers,
		applyCh,
		storage,
		logger,
		otel.Tracer("consensus-lab/internal/consensus/raft"),
		promMetrics,
	)
	if err != nil {
		for _, p := range peers {
			_ = p.Close()
		}
		return err
	}

	kvSvc := service.NewKV(node, store, logger, otel.Tracer("consensus-lab/internal/service/kv"), promMetrics, cfg.NodeID)
	kvSvc.SnapshotEvery = cfg.SnapshotEvery
	raftSrv := raftgrpc.NewServer(node, otel.Tracer("consensus-lab/internal/transport/grpc/raft"))
	adminSrv := admingrpc.NewServer(cfg.NodeID, string(cfg.ConsensusType), peerAddrs, node)

	app, err := apppkg.New(cfg, logger, node, kvSvc, raftSrv, adminSrv)
	if err != nil {
		node.Stop()
		return err
	}
	defer app.Stop()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return app.Run(ctx)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
