package api

import (
	"context"
	"encoding/json"
	"net"

	"github.com/tabladrum/grove-suite/grove/internal/graph"
	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
	"github.com/tabladrum/grove-suite/grove/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

type JSONCodec struct{}

func (JSONCodec) Name() string                       { return "json" }
func (JSONCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (JSONCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func init() {
	encoding.RegisterCodec(JSONCodec{})
}

type GRPCService struct {
	server *Server
}

type groveGRPCServer interface {
	Health(context.Context, map[string]any) (map[string]any, error)
	Status(context.Context, map[string]any) (map[string]any, error)
	Index(context.Context, map[string]any) (map[string]any, error)
	Query(context.Context, map[string]any) (map[string]any, error)
	Symbols(context.Context, map[string]any) (map[string]any, error)
	Deps(context.Context, map[string]any) (map[string]any, error)
	Impact(context.Context, map[string]any) (map[string]any, error)
	Tests(context.Context, map[string]any) (map[string]any, error)
	ICR(context.Context, map[string]any) (map[string]any, error)
}

func NewGRPCService(codeGraph *graph.CodeGraph, engine *parser.Engine, st *store.Store, root string) *GRPCService {
	return &GRPCService{server: NewServer(codeGraph, engine, st, root)}
}

func (s *GRPCService) Health(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"status": "ok", "version": version.Version}, nil
}

func (s *GRPCService) Status(ctx context.Context, _ map[string]any) (map[string]any, error) {
	status, err := s.server.store.Status(ctx)
	if err != nil {
		return nil, err
	}
	return toMap(status)
}

func (s *GRPCService) Index(ctx context.Context, req map[string]any) (map[string]any, error) {
	root, _ := req["dir"].(string)
	if root == "" {
		root = s.server.root
	}
	codeGraph, result, err := index.New(s.server.parser, s.server.store).Index(ctx, root)
	if err != nil {
		return nil, err
	}
	s.server.mu.Lock()
	s.server.graph = codeGraph
	s.server.mu.Unlock()
	return toMap(result)
}

func (s *GRPCService) Query(_ context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{"symbols": s.server.currentGraph().Search(stringArg(req, "intent"), intArg(req, "limit", 50))}, nil
}

func (s *GRPCService) Symbols(_ context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{"symbols": s.server.currentGraph().Search(stringArg(req, "query"), intArg(req, "limit", 50))}, nil
}

func (s *GRPCService) Deps(_ context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{"edges": s.server.currentGraph().Deps(stringArg(req, "file"))}, nil
}

func (s *GRPCService) Impact(_ context.Context, req map[string]any) (map[string]any, error) {
	query := stringArg(req, "query")
	if query == "" {
		query = stringArg(req, "file")
	}
	return map[string]any{"nodes": s.server.currentGraph().Impact(query, intArg(req, "maxDepth", 3))}, nil
}

func (s *GRPCService) Tests(_ context.Context, req map[string]any) (map[string]any, error) {
	query := stringArg(req, "query")
	if query == "" {
		query = stringArg(req, "file")
	}
	return map[string]any{"tests": s.server.currentGraph().TestsFor(query)}, nil
}

func (s *GRPCService) ICR(_ context.Context, req map[string]any) (map[string]any, error) {
	return toMap(s.server.currentGraph().ComputeICR(stringArg(req, "intent")))
}

func RegisterGRPC(server *grpc.Server, service *GRPCService) {
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "grove.v1.GroveService",
		HandlerType: (*groveGRPCServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Health", Handler: unary(service.Health)},
			{MethodName: "Status", Handler: unary(service.Status)},
			{MethodName: "Index", Handler: unary(service.Index)},
			{MethodName: "Query", Handler: unary(service.Query)},
			{MethodName: "Symbols", Handler: unary(service.Symbols)},
			{MethodName: "Deps", Handler: unary(service.Deps)},
			{MethodName: "Impact", Handler: unary(service.Impact)},
			{MethodName: "Tests", Handler: unary(service.Tests)},
			{MethodName: "ICR", Handler: unary(service.ICR)},
		},
	}, service)
}

func ListenGRPC(addr string, service *GRPCService) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	RegisterGRPC(server, service)
	return server.Serve(listener)
}

type unaryHandler func(context.Context, map[string]any) (map[string]any, error)

func unary(handler unaryHandler) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		var req map[string]any
		if err := dec(&req); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return handler(ctx, req)
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/grove.v1.GroveService"}
		return interceptor(ctx, req, info, func(ctx context.Context, request any) (any, error) {
			payload, _ := request.(map[string]any)
			return handler(ctx, payload)
		})
	}
}

func toMap(value any) (map[string]any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}
