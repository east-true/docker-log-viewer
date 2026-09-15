// Package transport implements the narrow reverse gRPC control channel between
// a Docker Log Viewer Server and its outbound-only Agents.
package transport

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

const (
	protocolVersion = 1
	connectMethod   = "/dockerlogviewer.v1.AgentService/Connect"
	maxMessageBytes = 1 << 20
	pendingMessages = 32
)

const (
	kindHello     = "hello"
	kindHelloAck  = "hello_ack"
	kindHeartbeat = "heartbeat"
	kindRequest   = "request"
	kindResponse  = "response"
	kindLog       = "log"
	kindEnd       = "end"
	kindCancel    = "cancel"
)

const (
	requestContainers = "containers"
	requestImages     = "images"
	requestLogs       = "logs"
)

type message struct {
	Kind       string          `json:"kind"`
	RequestID  string          `json:"request_id,omitempty"`
	Request    string          `json:"request,omitempty"`
	AgentID    string          `json:"agent_id,omitempty"`
	AgentName  string          `json:"agent_name,omitempty"`
	Token      string          `json:"token,omitempty"`
	Protocol   int             `json:"protocol,omitempty"`
	Container  string          `json:"container,omitempty"`
	Tail       string          `json:"tail,omitempty"`
	Since      string          `json:"since,omitempty"`
	Follow     bool            `json:"follow,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Log        json.RawMessage `json:"log,omitempty"`
	Error      string          `json:"error,omitempty"`
	SentAtUnix int64           `json:"sent_at_unix_nano,omitempty"`
}

type jsonCodec struct{}

func (jsonCodec) Name() string                           { return "json" }
func (jsonCodec) Marshal(value any) ([]byte, error)      { return json.Marshal(value) }
func (jsonCodec) Unmarshal(data []byte, value any) error { return json.Unmarshal(data, value) }

var codec encoding.Codec = jsonCodec{}

type agentServiceServer interface {
	Connect(grpc.ServerStream) error
}

func registerAgentService(server *grpc.Server, service agentServiceServer) {
	server.RegisterService(&agentServiceDescription, service)
}

func connectClient(ctx context.Context, connection grpc.ClientConnInterface, options ...grpc.CallOption) (grpc.ClientStream, error) {
	return connection.NewStream(ctx, &agentServiceDescription.Streams[0], connectMethod, options...)
}

var agentServiceDescription = grpc.ServiceDesc{
	ServiceName: "dockerlogviewer.v1.AgentService",
	HandlerType: (*agentServiceServer)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName:    "Connect",
		Handler:       func(service any, stream grpc.ServerStream) error { return service.(agentServiceServer).Connect(stream) },
		ServerStreams: true,
		ClientStreams: true,
	}},
}
