package singboxapi

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const queryStatsMethod = "/v2ray.core.app.stats.command.StatsService/QueryStats"

type Client struct {
	addr        string
	dialOptions []grpc.DialOption
}

func NewClient(addr string) (*Client, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("sing-box stats address is required")
	}
	return &Client{
		addr: addr,
		dialOptions: []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		},
	}, nil
}

func (c *Client) QueryStats(ctx context.Context, patterns []string, reset bool) ([]*Stat, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	conn, err := grpc.DialContext(
		ctx,
		c.addr,
		c.dialOptions...,
	)
	if err != nil {
		return nil, fmt.Errorf("dial sing-box stats api %q: %w", c.addr, err)
	}
	defer conn.Close()

	req := &QueryStatsRequest{
		Patterns: compactUniqueStrings(patterns),
		Reset_:   reset,
	}
	if len(req.Patterns) == 0 {
		req.Pattern = ""
	} else if len(req.Patterns) == 1 {
		req.Pattern = req.Patterns[0]
	}

	var resp QueryStatsResponse
	if err := conn.Invoke(ctx, queryStatsMethod, req, &resp); err != nil {
		return nil, fmt.Errorf("query sing-box stats api %q: %w", c.addr, err)
	}
	return resp.Stat, nil
}

func (c *Client) withContextDialer(dialer func(context.Context, string) (net.Conn, error)) *Client {
	clone := *c
	clone.dialOptions = append([]grpc.DialOption{}, c.dialOptions...)
	clone.dialOptions = append(clone.dialOptions, grpc.WithContextDialer(dialer))
	return &clone
}

func compactUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
