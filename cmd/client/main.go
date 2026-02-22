// Package main implements the CLI client for the replicated KV service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	kvgrpc "github.com/i-melnichenko/consensus-lab/internal/transport/grpc/kv"
)

const usage = `Usage:
  client [--addr host:port[,host:port,...]] get <key>
  client [--addr host:port[,host:port,...]] put <key> <value>
  client [--addr host:port[,host:port,...]] delete <key>

When multiple addresses are provided:
  - get    uses a random node (read from any replica)
  - put    finds the leader automatically
  - delete finds the leader automatically

Flags:
  --addr     Comma-separated list of KV gRPC addresses (default "localhost:8080")
  --timeout  Request timeout (default 5s)
`

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "localhost:8080", "comma-separated KV gRPC addresses")
	timeout := flag.Duration("timeout", 5*time.Second, "request timeout")
	flag.Usage = func() { _, _ = fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		return fmt.Errorf("subcommand required: get | put | delete")
	}

	addrs := splitAddrs(*addr)

	client, err := kvgrpc.DialCluster(addrs, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch args[0] {
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: get <key>")
		}
		return cmdGet(ctx, client, args[1])

	case "put":
		if len(args) != 3 {
			return fmt.Errorf("usage: put <key> <value>")
		}
		return cmdPut(ctx, client, args[1], args[2])

	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: delete <key>")
		}
		return cmdDelete(ctx, client, args[1])

	default:
		flag.Usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func cmdGet(ctx context.Context, c *kvgrpc.ClusterClient, key string) error {
	value, found, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		fmt.Printf("(not found) %s\n", key)
		return nil
	}
	fmt.Printf("%s = %s\n", key, value)
	return nil
}

func cmdPut(ctx context.Context, c *kvgrpc.ClusterClient, key, value string) error {
	index, err := c.Put(ctx, key, value)
	if errors.Is(err, kvgrpc.ErrNoLeader) {
		return fmt.Errorf("no leader available, cluster may be degraded")
	}
	if err != nil {
		return err
	}
	fmt.Printf("ok (index %d)\n", index)
	return nil
}

func cmdDelete(ctx context.Context, c *kvgrpc.ClusterClient, key string) error {
	index, err := c.Delete(ctx, key)
	if errors.Is(err, kvgrpc.ErrNoLeader) {
		return fmt.Errorf("no leader available, cluster may be degraded")
	}
	if err != nil {
		return err
	}
	fmt.Printf("ok (index %d)\n", index)
	return nil
}

func splitAddrs(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
