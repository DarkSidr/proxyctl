package singboxapi

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type testStatsService interface{}

func TestClientQueryStats(t *testing.T) {
	t.Parallel()

	listener := bufconn.Listen(1024 * 1024)
	defer listener.Close()

	server := grpc.NewServer()
	defer server.Stop()

	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "v2ray.core.app.stats.command.StatsService",
		HandlerType: (*testStatsService)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "QueryStats",
				Handler: func(_ interface{}, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
					var req QueryStatsRequest
					if err := dec(&req); err != nil {
						return nil, err
					}
					if len(req.Patterns) != 1 || req.Patterns[0] != "user>>>" || !req.Reset_ {
						t.Fatalf("unexpected request: %+v", req)
					}
					return &QueryStatsResponse{
						Stat: []*Stat{
							{Name: "user>>>alice>>>traffic>>>uplink", Value: 10},
							{Name: "user>>>alice>>>traffic>>>downlink", Value: 20},
						},
					}, nil
				},
			},
		},
	}, &struct{}{})

	go server.Serve(listener)

	client, err := NewClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client = client.withContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stats, err := client.QueryStats(ctx, []string{"user>>>"}, true)
	if err != nil {
		t.Fatalf("query stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(stats))
	}
	if stats[0].Name != "user>>>alice>>>traffic>>>uplink" || stats[0].Value != 10 {
		t.Fatalf("unexpected first stat: %+v", stats[0])
	}
}
