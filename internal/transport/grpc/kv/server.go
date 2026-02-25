package kvgrpc

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
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
	tracer  oteltrace.Tracer
}

// NewServer creates a KV gRPC server adapter for the provided handler.
func NewServer(handler Handler, tracer oteltrace.Tracer) *Server {
	return &Server{handler: handler, tracer: tracer}
}

// Put handles a KV Put RPC.
func (s *Server) Put(ctx context.Context, req *kvpb.PutRequest) (*kvpb.PutResponse, error) {
	ctx, span := s.tracer.Start(
		ctx,
		"kvgrpc.server.Put",
		oteltrace.WithAttributes(
			attribute.String("kv.key", req.Key),
			attribute.Int("kv.value.bytes", len(req.Value)),
		),
	)
	defer span.End()

	index, err := s.handler.Put(ctx, req.Key, req.Value)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return nil, toGRPCStatus(err)
	}
	span.SetAttributes(attribute.Int64("raft.log.index", index))
	return &kvpb.PutResponse{Index: index}, nil
}

// Get handles a KV Get RPC.
func (s *Server) Get(ctx context.Context, req *kvpb.GetRequest) (*kvpb.GetResponse, error) {
	_, span := s.tracer.Start(
		ctx,
		"kvgrpc.server.Get",
		oteltrace.WithAttributes(attribute.String("kv.key", req.Key)),
	)
	defer span.End()

	value, found := s.handler.Get(req.Key)
	span.SetAttributes(
		attribute.Bool("kv.found", found),
		attribute.Int("kv.value.bytes", len(value)),
	)
	return &kvpb.GetResponse{
		Found: found,
		Value: value,
	}, nil
}

// Delete handles a KV Delete RPC.
func (s *Server) Delete(ctx context.Context, req *kvpb.DeleteRequest) (*kvpb.DeleteResponse, error) {
	ctx, span := s.tracer.Start(
		ctx,
		"kvgrpc.server.Delete",
		oteltrace.WithAttributes(attribute.String("kv.key", req.Key)),
	)
	defer span.End()

	index, err := s.handler.Delete(ctx, req.Key)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return nil, toGRPCStatus(err)
	}
	span.SetAttributes(attribute.Int64("raft.log.index", index))
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
