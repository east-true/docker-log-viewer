package transport

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type AgentConfig struct {
	Address   string
	ID        string
	Name      string
	Token     string
	TLSConfig *tls.Config
	Insecure  bool
	Engine    *dockerengine.Client
	Logf      func(string, ...any)
	Dialer    func(context.Context, string) (net.Conn, error)
}

func RunAgent(ctx context.Context, config AgentConfig) error {
	if config.Address == "" || !validAgentID(config.ID) || config.Name == "" || len(config.Token) < 32 || config.Engine == nil {
		return errors.New("Agent address, durable ID, name, token, and Docker Engine are required")
	}
	if config.TLSConfig == nil && !config.Insecure {
		return errors.New("Agent TLS configuration is required unless insecure transport is explicitly enabled")
	}
	if config.Logf == nil {
		config.Logf = func(string, ...any) {}
	}
	delay := time.Second
	for ctx.Err() == nil {
		startedAt := time.Now()
		err := runAgentSession(ctx, config)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(startedAt) >= time.Minute {
			delay = time.Second
		}
		config.Logf("Agent connection ended: %v; retrying in %s", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
	return nil
}

func runAgentSession(ctx context.Context, config AgentConfig) error {
	if config.Logf == nil {
		config.Logf = func(string, ...any) {}
	}
	var transportCredentials credentials.TransportCredentials
	if config.TLSConfig != nil {
		transportCredentials = credentials.NewTLS(config.TLSConfig)
	} else {
		transportCredentials = insecure.NewCredentials()
	}
	options := []grpc.DialOption{
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(codec), grpc.MaxCallRecvMsgSize(maxMessageBytes), grpc.MaxCallSendMsgSize(maxMessageBytes)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	}
	if config.Dialer != nil {
		options = append(options, grpc.WithContextDialer(config.Dialer))
	}
	connection, err := grpc.NewClient(config.Address, options...)
	if err != nil {
		return fmt.Errorf("open Server connection: %w", err)
	}
	defer connection.Close()
	stream, err := connectClient(ctx, connection)
	if err != nil {
		return fmt.Errorf("open reverse session: %w", err)
	}
	sender := &agentSender{stream: stream}
	if err := sender.send(message{Kind: kindHello, AgentID: config.ID, AgentName: config.Name, Token: config.Token, Protocol: protocolVersion}); err != nil {
		return fmt.Errorf("send Agent handshake: %w", err)
	}
	var acknowledged message
	if err := stream.RecvMsg(&acknowledged); err != nil {
		return fmt.Errorf("receive Agent handshake: %w", err)
	}
	if acknowledged.Kind != kindHelloAck || acknowledged.Protocol != protocolVersion {
		return errors.New("Server rejected the Agent protocol")
	}
	config.Logf("Agent %s connected to %s", config.ID, config.Address)

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	handler := &agentHandler{ctx: sessionCtx, engine: config.Engine, sender: sender, requests: make(map[string]context.CancelFunc)}
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case at := <-ticker.C:
				if sender.send(message{Kind: kindHeartbeat, SentAtUnix: at.UTC().UnixNano()}) != nil {
					cancelSession()
					return
				}
			}
		}
	}()

	for {
		var incoming message
		if err := stream.RecvMsg(&incoming); err != nil {
			cancelSession()
			handler.cancelAll()
			<-heartbeatDone
			return err
		}
		switch incoming.Kind {
		case kindRequest:
			handler.start(incoming)
		case kindCancel:
			handler.cancel(incoming.RequestID)
		}
	}
}

type agentSender struct {
	stream grpc.ClientStream
	mu     sync.Mutex
}

func (s *agentSender) send(value message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.SendMsg(&value)
}

type agentHandler struct {
	ctx    context.Context
	engine *dockerengine.Client
	sender *agentSender

	mu       sync.Mutex
	requests map[string]context.CancelFunc
}

func (h *agentHandler) start(request message) {
	if request.RequestID == "" {
		return
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.mu.Lock()
	if previous := h.requests[request.RequestID]; previous != nil {
		previous()
	}
	h.requests[request.RequestID] = cancel
	h.mu.Unlock()
	go func() {
		defer h.finish(request.RequestID)
		h.handle(ctx, request)
	}()
}

func (h *agentHandler) handle(ctx context.Context, request message) {
	switch request.Request {
	case requestContainers:
		values, err := h.engine.Containers(ctx)
		h.respond(request.RequestID, values, err)
	case requestImages:
		values, err := h.engine.Images(ctx)
		h.respond(request.RequestID, values, err)
	case requestLogs:
		err := h.engine.Logs(ctx, dockerengine.LogRequest{
			ContainerID: request.Container, Tail: request.Tail, Since: request.Since, Follow: request.Follow,
		}, func(event dockerengine.LogEvent) error {
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			return h.sender.send(message{Kind: kindLog, RequestID: request.RequestID, Log: payload})
		})
		if errors.Is(err, context.Canceled) {
			return
		}
		terminal := message{Kind: kindEnd, RequestID: request.RequestID}
		if err != nil {
			terminal.Error = err.Error()
		}
		_ = h.sender.send(terminal)
	default:
		_ = h.sender.send(message{Kind: kindResponse, RequestID: request.RequestID, Error: "unsupported Agent request"})
	}
}

func (h *agentHandler) respond(requestID string, value any, requestErr error) {
	response := message{Kind: kindResponse, RequestID: requestID}
	if requestErr != nil {
		response.Error = requestErr.Error()
	} else {
		payload, err := json.Marshal(value)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.Payload = payload
		}
	}
	_ = h.sender.send(response)
}

func (h *agentHandler) finish(requestID string) {
	h.mu.Lock()
	delete(h.requests, requestID)
	h.mu.Unlock()
}

func (h *agentHandler) cancel(requestID string) {
	h.mu.Lock()
	cancel := h.requests[requestID]
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (h *agentHandler) cancelAll() {
	h.mu.Lock()
	for _, cancel := range h.requests {
		cancel()
	}
	h.mu.Unlock()
}

func LoadOrCreateAgentID(stateDirectory string) (string, error) {
	if !filepath.IsAbs(stateDirectory) {
		return "", errors.New("Agent state directory must be an absolute path")
	}
	path := filepath.Join(stateDirectory, "agent-id")
	if data, err := os.ReadFile(path); err == nil {
		value := strings.TrimSpace(string(data))
		if !validAgentID(value) {
			return "", errors.New("stored Agent ID is invalid")
		}
		return value, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read Agent ID: %w", err)
	}
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create Agent state directory: %w", err)
	}
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate Agent ID: %w", err)
	}
	buffer[6] = buffer[6]&0x0f | 0x40
	buffer[8] = buffer[8]&0x3f | 0x80
	encoded := hex.EncodeToString(buffer)
	value := encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("persist Agent ID: %w", err)
	}
	return value, nil
}
