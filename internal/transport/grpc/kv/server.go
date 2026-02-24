package kvgrpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/i-melnichenko/consensus-lab/internal/service"
	kvpb "github.com/i-melnichenko/consensus-lab/pkg/proto/kvv1"
)

// Handler is the subset of *service.KV required by the gRPC server.
// *service.KV satisfies this interface.
type Handler interface {
	Get(key string) (string, bool)
	Put(ctx context.Context, key, value string) (int64, error)
	Delete(ctx context.Context, key string) (int64, error)
}

// Server implements kvpb.KVServiceServer by delegating to a KV service.
type Server struct {
	kvpb.UnimplementedKVServiceServer
	handler Handler
}

// NewServer creates a KV gRPC server adapter for the provided handler.
func NewServer(handler Handler) *Server {
	return &Server{handler: handler}
}

// Put handles a KV Put RPC.
func (s *Server) Put(ctx context.Context, req *kvpb.PutRequest) (*kvpb.PutResponse, error) {
	index, err := s.handler.Put(ctx, req.Key, req.Value)
	if err != nil {
		return nil, toGRPCStatus(err)
	}
	return &kvpb.PutResponse{Index: index}, nil
}

// Get handles a KV Get RPC.
func (s *Server) Get(_ context.Context, req *kvpb.GetRequest) (*kvpb.GetResponse, error) {
	value, found := s.handler.Get(req.Key)
	return &kvpb.GetResponse{
		Found: found,
		Value: value,
	}, nil
}

// Delete handles a KV Delete RPC.
func (s *Server) Delete(ctx context.Context, req *kvpb.DeleteRequest) (*kvpb.DeleteResponse, error) {
	index, err := s.handler.Delete(ctx, req.Key)
	if err != nil {
		return nil, toGRPCStatus(err)
	}
	return &kvpb.DeleteResponse{Index: index}, nil
}

func toGRPCStatus(err error) error {
	if errors.Is(err, service.ErrNotLeader) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, service.ErrCommitTimeout) {
		return status.Error(codes.Unavailable, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
