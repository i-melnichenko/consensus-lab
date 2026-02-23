// Package main implements the CLI client for the replicated KV service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	kvgrpc "github.com/i-melnichenko/consensus-lab/internal/transport/grpc/kv"
	adminpb "github.com/i-melnichenko/consensus-lab/pkg/proto/adminv1"
)

const (
	ansiReset            = "\033[0m"
	ansiBold             = "\033[1m"
	ansiDim              = "\033[2m"
	ansiRed              = "\033[31m"
	ansiGreen            = "\033[32m"
	ansiYellow           = "\033[33m"
	ansiBlue             = "\033[34m"
	ansiCyan             = "\033[36m"
	adminRefreshInterval = 500 * time.Millisecond
)

const usage = `Usage:
  client [--addr host:port[,host:port,...]] get <key>
  client [--addr host:port[,host:port,...]] put <key> <value>
  client [--addr host:port[,host:port,...]] delete <key>
  client [--addr host:port[,host:port,...]] admin

When multiple addresses are provided:
  - get    uses a random node (read from any replica)
  - put    finds the leader automatically
  - delete finds the leader automatically
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
		return fmt.Errorf("subcommand required: get | put | delete | admin")
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

type adminConn struct {
	addr   string
	conn   *grpc.ClientConn
	client adminpb.AdminServiceClient
}

type adminRow struct {
	addr          string
	nodeID        string
	consensus     string
	role          string
	status        string
	leaderID      string
	term          int64
	commit        int64
	applied       int64
	lastLog       int64
	lastLogTerm   int64
	snapshot      int64
	cfg           string
	peers         string
	lastAppliedAt string
	err           string
}

func cmdAdmin(addrs []string, timeout time.Duration) error {
	if len(addrs) == 0 {
		return fmt.Errorf("no addresses provided")
	}

	conns, err := openAdminConns(addrs)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range conns {
			_ = c.conn.Close()
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(adminRefreshInterval)
	defer ticker.Stop()

	for {
		rows, ts := pollAdminRows(ctx, conns, timeout)
		renderAdminTable(rows, ts)

		select {
		case <-ctx.Done():
			fmt.Println()
			return nil
		case <-ticker.C:
		}
	}
}

func openAdminConns(addrs []string) ([]adminConn, error) {
	conns := make([]adminConn, 0, len(addrs))
	for _, addr := range addrs {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			for _, c := range conns {
				_ = c.conn.Close()
			}
			return nil, fmt.Errorf("dial admin %s: %w", addr, err)
		}
		conns = append(conns, adminConn{
			addr:   addr,
			conn:   conn,
			client: adminpb.NewAdminServiceClient(conn),
		})
	}
	return conns, nil
}

func pollAdminRows(ctx context.Context, conns []adminConn, timeout time.Duration) ([]adminRow, time.Time) {
	rows := make([]adminRow, 0, len(conns))
	for _, c := range conns {
		row := adminRow{addr: c.addr}

		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := c.client.GetNodeInfo(reqCtx, &adminpb.GetNodeInfoRequest{})
		cancel()
		if err != nil {
			row.err = err.Error()
			rows = append(rows, row)
			continue
		}
		node := resp.GetNode()
		if node == nil {
			row.err = "empty node info"
			rows = append(rows, row)
			continue
		}

		row.nodeID = node.GetNodeId()
		row.consensus = trimEnum(node.GetConsensusType().String(), "CONSENSUS_TYPE_")
		row.role = trimEnum(node.GetRole().String(), "NODE_ROLE_")
		row.status = trimEnum(node.GetStatus().String(), "NODE_STATUS_")
		row.peers = formatPeerList(node.GetPeers())

		if raft := node.GetRaft(); raft != nil {
			row.leaderID = raft.GetLeaderId()
			row.term = raft.GetTerm()
			row.commit = raft.GetCommitIndex()
			row.applied = raft.GetLastApplied()
			row.lastLog = raft.GetLastLogIndex()
			row.lastLogTerm = raft.GetLastLogTerm()
			row.snapshot = raft.GetSnapshotLastIndex()
			row.lastAppliedAt = formatTS(raft.GetLastAppliedAt())
			row.cfg = formatCFG(int(raft.GetQuorumSize()), len(raft.GetClusterMembers()))
		}

		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].nodeID == rows[j].nodeID {
			return rows[i].addr < rows[j].addr
		}
		if rows[i].nodeID == "" {
			return false
		}
		if rows[j].nodeID == "" {
			return true
		}
		return rows[i].nodeID < rows[j].nodeID
	})

	return rows, time.Now()
}

func renderAdminTable(rows []adminRow, ts time.Time) {
	// Clear screen + scrollback and move cursor home to avoid remnants between frames.
	fmt.Print("\033[H\033[2J\033[3J")
	fmt.Printf("%s%sAdmin view%s  %s\n\n",
		ansiBold, ansiCyan, ansiReset, ts.Format(time.RFC3339))
	fmt.Printf("%s%-18s %-10s %-9s %-9s %-10s %4s %6s %6s %6s %6s %-8s %-6s %-28s %-14s\n",
		ansiBold,
		"ADDR", "NODE", "ROLE", "STATUS", "LEADER", "TERM", "CMT", "APL", "LOG", "SNAP", "A_AT", "CFG", "PEERS", "ERR")
	fmt.Printf("%s\n", ansiReset)
	fmt.Printf("%s%s%s\n", ansiDim, strings.Repeat("-", 168), ansiReset)

	for _, r := range rows {
		if r.err != "" {
			fmt.Printf("%s %s %s %s %s %4s %6s %6s %6s %6s %s %s %s %s %s\n",
				cell(r.addr, 18),
				cell("-", 10),
				cell("-", 9),
				colorCell("ERROR", 9, ansiRed),
				cell("-", 10),
				"-", "-", "-", "-", "-",
				cell("-", 8),
				cell("-", 6),
				cell("-", 28),
				colorCell(shorten(errorKind(r.err), 14), 14, ansiRed),
				ansiWrap(ansiDim, shorten(errorSummary(r.err), 0)))
			continue
		}
		fmt.Printf("%s %s %s %s %s %4d %6d %6d %6d %6d %s %s %s %s\n",
			cell(r.addr, 18),
			colorNodeCell(shorten(r.nodeID, 10), 10, r.role),
			colorRoleCell(shorten(r.role, 9), 9, r.role),
			colorStatusCell(shorten(r.status, 9), 9, r.status),
			colorLeaderCell(shorten(r.leaderID, 10), 10, r.role),
			r.term,
			r.commit,
			r.applied,
			r.lastLog,
			r.snapshot,
			cell(shorten(r.lastAppliedAt, 8), 8),
			cell(shorten(r.cfg, 6), 6),
			cell(shorten(r.peers, 28), 28),
			cell("", 14),
		)
	}

	fmt.Println()
	fmt.Printf("%sCtrl+C to exit%s\n", ansiDim, ansiReset)
}

func formatPeerList(peers []*adminpb.PeerInfo) string {
	if len(peers) == 0 {
		return ""
	}
	items := make([]string, 0, len(peers))
	for _, p := range peers {
		if p == nil {
			continue
		}
		id := strings.TrimSpace(p.GetNodeId())
		addr := strings.TrimSpace(p.GetAddress())
		if id != "" {
			items = append(items, id)
			continue
		}
		if addr != "" {
			items = append(items, addr)
		}
	}
	sort.Strings(items)
	return strings.Join(items, ",")
}

func formatTS(ts interface{ AsTime() time.Time }) string {
	if ts == nil {
		return ""
	}
	t := ts.AsTime()
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("15:04:05")
}

func formatCFG(quorumSize, members int) string {
	if quorumSize <= 0 && members <= 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", quorumSize, members)
}

func errorKind(err string) string {
	switch {
	case strings.Contains(err, "code = Unavailable"):
		return "Unavailable"
	case strings.Contains(err, "code = Unimplemented"):
		return "Unimplemented"
	case strings.Contains(err, "code = DeadlineExceeded"):
		return "Timeout"
	default:
		return "Error"
	}
}

func errorSummary(err string) string {
	err = strings.TrimSpace(err)
	err = strings.ReplaceAll(err, "\n", " ")
	err = strings.Join(strings.Fields(err), " ")
	return err
}

func ansiWrap(code, s string) string {
	if code == "" || s == "" {
		return s
	}
	return code + s + ansiReset
}

func cell(s string, width int) string {
	return fmt.Sprintf("%-*s", width, s)
}

func colorCell(s string, width int, code string) string {
	return ansiWrap(code, cell(s, width))
}

func colorRole(s, role string) string {
	switch role {
	case "leader":
		return ansiWrap(ansiGreen+ansiBold, s)
	case "candidate":
		return ansiWrap(ansiYellow+ansiBold, s)
	case "follower":
		return ansiWrap(ansiBlue, s)
	default:
		return s
	}
}

func colorRoleCell(s string, width int, role string) string {
	return colorRole(cell(s, width), role)
}

func colorStatusCode(status string) string {
	switch status {
	case "healthy":
		return ansiGreen
	case "degraded":
		return ansiYellow
	case "unavailable":
		return ansiRed
	default:
		return ""
	}
}

func colorStatus(s, status string) string {
	return ansiWrap(colorStatusCode(status), s)
}

func colorStatusCell(s string, width int, status string) string {
	return colorStatus(cell(s, width), status)
}

func colorLeader(s, role string) string {
	if s == "" {
		return ansiWrap(ansiDim, s)
	}
	if role == "leader" {
		return ansiWrap(ansiGreen, s)
	}
	return ansiWrap(ansiCyan, s)
}

func colorLeaderCell(s string, width int, role string) string {
	return colorLeader(cell(s, width), role)
}

func colorNode(s, role string) string {
	if role == "leader" {
		return ansiWrap(ansiBold, s)
	}
	return s
}

func colorNodeCell(s string, width int, role string) string {
	return colorNode(cell(s, width), role)
}

func trimEnum(s, prefix string) string {
	s = strings.TrimPrefix(s, prefix)
	return strings.ToLower(s)
}

func shorten(s string, n int) string {
	if n <= 0 {
		return s
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
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
