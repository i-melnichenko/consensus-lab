// Package main implements the CLI client for the replicated KV service.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	kvgrpc "github.com/i-melnichenko/consensus-lab/internal/transport/grpc/kv"
)

const usage = `Usage:
  client [--addr host:port[,host:port,...]] get <key>
  client [--addr host:port[,host:port,...]] get-batch [--in <file|->]
  client [--addr host:port[,host:port,...]] put <key> <value>
  client [--addr host:port[,host:port,...]] put-batch [--in <file|->]
  client [--addr host:port[,host:port,...]] delete <key>
  client [--addr host:port[,host:port,...]] delete-batch [--in <file|->]
  client [--addr host:port[,host:port,...]] admin

When multiple addresses are provided:
 - get    uses a random node (read from any replica)
 - get-batch reads many keys with one long-lived client (one key per line)
  - put    finds the leader automatically
 - put-batch writes many key/value pairs with one long-lived client (TSV: key<TAB>value)
  - delete finds the leader automatically
 - delete-batch deletes many keys with one long-lived client (one key per line)
  - admin  polls each admin gRPC endpoint and renders a live table

Flags:
  --addr     Comma-separated gRPC addresses for the selected mode (KV or Admin)
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
		return fmt.Errorf("subcommand required: get | get-batch | put | put-batch | delete | delete-batch | admin")
	}

	addrs := splitAddrs(*addr)

	switch args[0] {
	case "get":
		client, err := kvgrpc.DialCluster(addrs, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()

		if len(args) != 2 {
			return fmt.Errorf("usage: get <key>")
		}
		return cmdGet(ctx, client, args[1])

	case "get-batch":
		fs := flag.NewFlagSet("get-batch", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		inPath := fs.String("in", "-", "input path (one key per line), use - for stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("usage: get-batch [--in <file|->]")
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: get-batch [--in <file|->]")
		}
		client, err := kvgrpc.DialCluster(addrs, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		return cmdGetBatch(client, *timeout, *inPath)

	case "put":
		client, err := kvgrpc.DialCluster(addrs, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()

		if len(args) != 3 {
			return fmt.Errorf("usage: put <key> <value>")
		}
		return cmdPut(ctx, client, args[1], args[2])

	case "put-batch":
		fs := flag.NewFlagSet("put-batch", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		inPath := fs.String("in", "-", "TSV input path (key<TAB>value), use - for stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("usage: put-batch [--in <file|->]")
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: put-batch [--in <file|->]")
		}
		client, err := kvgrpc.DialCluster(addrs, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		return cmdPutBatch(client, *timeout, *inPath)

	case "delete":
		client, err := kvgrpc.DialCluster(addrs, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()

		if len(args) != 2 {
			return fmt.Errorf("usage: delete <key>")
		}
		return cmdDelete(ctx, client, args[1])

	case "delete-batch":
		fs := flag.NewFlagSet("delete-batch", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		inPath := fs.String("in", "-", "input path (one key per line), use - for stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("usage: delete-batch [--in <file|->]")
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: delete-batch [--in <file|->]")
		}
		client, err := kvgrpc.DialCluster(addrs, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		return cmdDeleteBatch(client, *timeout, *inPath)

	case "admin":
		if len(args) != 1 {
			return fmt.Errorf("usage: admin")
		}
		return cmdAdmin(addrs, *timeout)

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

func cmdPutBatch(c *kvgrpc.ClusterClient, timeout time.Duration, inPath string) error {
	var (
		r   io.Reader = os.Stdin
		f   *os.File
		err error
	)
	if inPath != "-" {
		// #nosec G304 -- CLI intentionally reads a user-provided local input file.
		f, err = os.Open(inPath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	scanner := bufio.NewScanner(r)
	seq := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		seq++
		key, value, ok := strings.Cut(line, "\t")
		if !ok {
			fmt.Printf("err\t%d\t0\t\tinvalid_tsv_line\n", seq)
			continue
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		index, putErr := c.Put(ctx, key, value)
		cancel()
		ms := time.Since(start).Milliseconds()

		switch {
		case putErr == nil:
			fmt.Printf("ok\t%d\t%d\t%d\t%s\n", seq, ms, index, key)
		case errors.Is(putErr, context.DeadlineExceeded), status.Code(putErr) == codes.DeadlineExceeded:
			fmt.Printf("timeout\t%d\t%d\t0\t%s\t%s\n", seq, ms, key, oneLineErr(putErr))
		default:
			fmt.Printf("err\t%d\t%d\t0\t%s\t%s\n", seq, ms, key, oneLineErr(putErr))
		}
	}
	return scanner.Err()
}

func cmdGetBatch(c *kvgrpc.ClusterClient, timeout time.Duration, inPath string) error {
	var (
		r   io.Reader = os.Stdin
		f   *os.File
		err error
	)
	if inPath != "-" {
		// #nosec G304 -- CLI intentionally reads a user-provided local input file.
		f, err = os.Open(inPath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	scanner := bufio.NewScanner(r)
	seq := 0
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key == "" {
			continue
		}
		seq++
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		value, found, getErr := c.Get(ctx, key)
		cancel()
		us := time.Since(start).Microseconds()

		if getErr == nil {
			if found {
				fmt.Printf("ok\t%d\t%d\t%s\t%d\n", seq, us, key, len(value))
			} else {
				fmt.Printf("notfound\t%d\t%d\t%s\t0\n", seq, us, key)
			}
			continue
		}

		switch {
		case errors.Is(getErr, context.DeadlineExceeded), status.Code(getErr) == codes.DeadlineExceeded:
			fmt.Printf("timeout\t%d\t%d\t%s\t%s\n", seq, us, key, oneLineErr(getErr))
		default:
			fmt.Printf("err\t%d\t%d\t%s\t%s\n", seq, us, key, oneLineErr(getErr))
		}
	}
	return scanner.Err()
}

func cmdDeleteBatch(c *kvgrpc.ClusterClient, timeout time.Duration, inPath string) error {
	var (
		r   io.Reader = os.Stdin
		f   *os.File
		err error
	)
	if inPath != "-" {
		// #nosec G304 -- CLI intentionally reads a user-provided local input file.
		f, err = os.Open(inPath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	scanner := bufio.NewScanner(r)
	seq := 0
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key == "" {
			continue
		}
		seq++
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		index, delErr := c.Delete(ctx, key)
		cancel()
		ms := time.Since(start).Milliseconds()

		switch {
		case delErr == nil:
			fmt.Printf("ok\t%d\t%d\t%d\t%s\n", seq, ms, index, key)
		case errors.Is(delErr, context.DeadlineExceeded), status.Code(delErr) == codes.DeadlineExceeded:
			fmt.Printf("timeout\t%d\t%d\t0\t%s\t%s\n", seq, ms, key, oneLineErr(delErr))
		default:
			fmt.Printf("err\t%d\t%d\t0\t%s\t%s\n", seq, ms, key, oneLineErr(delErr))
		}
	}
	return scanner.Err()
}

func oneLineErr(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(err.Error(), "\n", " ")
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
