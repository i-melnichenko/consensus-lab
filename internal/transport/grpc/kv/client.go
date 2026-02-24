// Package kvgrpc contains the KV gRPC client and server adapters.
package kvgrpc

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kvpb "github.com/i-melnichenko/consensus-lab/pkg/proto/kvv1"
)

// ErrNotLeader is returned when the targeted node is not the Raft leader.
var ErrNotLeader = errors.New("kv: node is not the leader")

// ErrNoLeader is returned by ClusterClient when no node in the cluster
// accepted a write — either no leader is elected yet or all nodes are down.
var ErrNoLeader = errors.New("kv: no leader found in cluster")

// Client is a thin wrapper around the generated KVServiceClient.
type Client struct {
	conn   *grpc.ClientConn
	client kvpb.KVServiceClient
}

// Dial connects to a KV gRPC server at target.
func Dial(target string, opts ...grpc.DialOption) (*Client, error) {
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("kv client: dial %s: %w", target, err)
	}
	return &Client{
		conn:   conn,
		client: kvpb.NewKVServiceClient(conn),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Get fetches a key from a KV node.
func (c *Client) Get(ctx context.Context, key string) (value string, found bool, err error) {
	resp, err := c.client.Get(ctx, &kvpb.GetRequest{Key: key})
	if err != nil {
		return "", false, fromGRPCStatus(err)
	}
	return resp.Value, resp.Found, nil
}

// Put sends a write request to a KV node.
func (c *Client) Put(ctx context.Context, key, value string) (index int64, err error) {
	resp, err := c.client.Put(ctx, &kvpb.PutRequest{Key: key, Value: value})
	if err != nil {
		return 0, fromGRPCStatus(err)
	}
	return resp.Index, nil
}

// Delete sends a delete request to a KV node.
func (c *Client) Delete(ctx context.Context, key string) (index int64, err error) {
	resp, err := c.client.Delete(ctx, &kvpb.DeleteRequest{Key: key})
	if err != nil {
		return 0, fromGRPCStatus(err)
	}
	return resp.Index, nil
}

// ClusterClient connects to multiple nodes and routes requests automatically:
//   - Get: tries nodes in random order, returns first successful response.
//   - Put / Delete: tries all nodes until the leader accepts the write.
type ClusterClient struct {
	clients []*Client

	mu         sync.RWMutex
	leaderHint int // -1 means unknown
}

// DialCluster connects to all provided addresses and returns a ClusterClient.
// Connections are lazy (gRPC dials on first use), so this succeeds even if
// nodes are temporarily unavailable.
func DialCluster(addrs []string, opts ...grpc.DialOption) (*ClusterClient, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("kv cluster client: no addresses provided")
	}
	clients := make([]*Client, 0, len(addrs))
	for _, addr := range addrs {
		c, err := Dial(addr, opts...)
		if err != nil {
			for _, cc := range clients {
				_ = cc.Close()
			}
			return nil, err
		}
		clients = append(clients, c)
	}
	return &ClusterClient{
		clients:    clients,
		leaderHint: -1,
	}, nil
}

// Close closes all underlying node client connections.
func (c *ClusterClient) Close() error {
	var errs []error
	for _, client := range c.clients {
		if err := client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Get tries nodes in random order and returns the first successful response.
// Read requests do not require the leader.
func (c *ClusterClient) Get(ctx context.Context, key string) (string, bool, error) {
	for _, i := range rand.Perm(len(c.clients)) {
		value, found, err := c.clients[i].Get(ctx, key)
		if err == nil {
			return value, found, nil
		}
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
	}
	return "", false, fmt.Errorf("kv: all %d nodes unavailable", len(c.clients))
}

// Put forwards the write to the Raft leader, trying all nodes until one accepts.
func (c *ClusterClient) Put(ctx context.Context, key, value string) (int64, error) {
	return c.writeToLeader(ctx, func(client *Client) (int64, error) {
		return client.Put(ctx, key, value)
	})
}

// Delete forwards the write to the Raft leader, trying all nodes until one accepts.
func (c *ClusterClient) Delete(ctx context.Context, key string) (int64, error) {
	return c.writeToLeader(ctx, func(client *Client) (int64, error) {
		return client.Delete(ctx, key)
	})
}

// writeToLeader tries each node in random order until one accepts the write.
// Nodes that respond with ErrNotLeader are skipped.
func (c *ClusterClient) writeToLeader(ctx context.Context, fn func(*Client) (int64, error)) (int64, error) {
	for _, i := range c.writeOrder() {
		index, err := fn(c.clients[i])
		if err == nil {
			c.setLeaderHint(i)
			return index, nil
		}
		if errors.Is(err, ErrNotLeader) {
			c.clearLeaderHintIf(i)
			continue
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// Network or server error — try next node.
	}
	return 0, ErrNoLeader
}

func (c *ClusterClient) writeOrder() []int {
	n := len(c.clients)
	order := make([]int, 0, n)

	hint := c.getLeaderHint()
	if hint >= 0 && hint < n {
		order = append(order, hint)
	}

	for _, i := range rand.Perm(n) {
		if i == hint {
			continue
		}
		order = append(order, i)
	}
	return order
}

func (c *ClusterClient) getLeaderHint() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.leaderHint
}

func (c *ClusterClient) setLeaderHint(i int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.leaderHint = i
}

func (c *ClusterClient) clearLeaderHintIf(i int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.leaderHint == i {
		c.leaderHint = -1
	}
}

func fromGRPCStatus(err error) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
		return ErrNotLeader
	}
	return err
}
