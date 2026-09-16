package transport

import (
	"context"
	"errors"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

func Serve(ctx context.Context, listener net.Listener, registry *Registry, options ...grpc.ServerOption) error {
	if listener == nil || registry == nil {
		return errors.New("Agent listener and registry are required")
	}
	options = append(options,
		grpc.ForceServerCodec(codec),
		grpc.MaxRecvMsgSize(maxMessageBytes),
		grpc.MaxSendMsgSize(maxMessageBytes),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
	server := grpc.NewServer(options...)
	registerAgentService(server, registry)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stopped := make(chan struct{})
		go func() { server.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			server.Stop()
		}
		return nil
	}
}
