// Package main implements the node process that runs Raft and the KV gRPC API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	apppkg "github.com/i-melnichenko/consensus-lab/internal/app"
	"github.com/i-melnichenko/consensus-lab/internal/consensus"
	raftconsensus "github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	"github.com/i-melnichenko/consensus-lab/internal/kv"
	"github.com/i-melnichenko/consensus-lab/internal/service"
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
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}

	applyCh := make(chan consensus.ApplyMsg, 256)
	store := kv.NewStore()
	storage := raftconsensus.NewJSONStorage(cfg.DataDir)

	node, err := raftconsensus.NewNode(cfg.NodeID, peers, applyCh, storage, logger)
	if err != nil {
		for _, p := range peers {
			_ = p.Close()
		}
		return err
	}

	kvSvc := service.NewKV(node, store, logger)
	kvSvc.SnapshotEvery = cfg.SnapshotEvery
	raftSrv := raftgrpc.NewServer(node)

	app, err := apppkg.New(cfg, logger, node, kvSvc, raftSrv)
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
