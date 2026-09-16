package transport

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Agent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Connected bool      `json:"connected"`
	LastSeen  time.Time `json:"last_seen"`
}

type Registry struct {
	token string
	now   func() time.Time

	mu       sync.RWMutex
	agents   map[string]Agent
	sessions map[string]*session
}

func NewRegistry(token string) (*Registry, error) {
	if len(token) < 32 {
		return nil, errors.New("Agent token must contain at least 32 characters")
	}
	return &Registry{token: token, now: time.Now, agents: make(map[string]Agent), sessions: make(map[string]*session)}, nil
}

func (r *Registry) Connect(stream grpc.ServerStream) error {
	var hello message
	handshakeContext, cancelHandshake := context.WithTimeout(stream.Context(), 10*time.Second)
	defer cancelHandshake()
	if err := receiveWithContext(handshakeContext, stream, &hello); err != nil {
		return err
	}
	if hello.Kind != kindHello || hello.Protocol != protocolVersion || !validAgentID(hello.AgentID) || hello.AgentName == "" || len(hello.AgentName) > 128 {
		return status.Error(codes.InvalidArgument, "invalid Agent handshake")
	}
	if subtle.ConstantTimeCompare([]byte(hello.Token), []byte(r.token)) != 1 {
		return status.Error(codes.Unauthenticated, "Agent authentication failed")
	}

	ctx, cancel := context.WithCancel(stream.Context())
	value := &session{id: hello.AgentID, name: hello.AgentName, stream: stream, ctx: ctx, cancel: cancel, pending: make(map[string]chan message)}
	if err := value.send(message{Kind: kindHelloAck, Protocol: protocolVersion, SentAtUnix: r.now().UTC().UnixNano()}); err != nil {
		cancel()
		return err
	}
	r.attach(value)
	defer r.detach(value)

	for {
		var incoming message
		if err := stream.RecvMsg(&incoming); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		r.touch(value)
		if incoming.Kind == kindHeartbeat {
			continue
		}
		value.deliver(incoming)
	}
}

func (r *Registry) attach(value *session) {
	r.mu.Lock()
	previous := r.sessions[value.id]
	r.sessions[value.id] = value
	r.agents[value.id] = Agent{ID: value.id, Name: value.name, Connected: true, LastSeen: r.now().UTC()}
	r.mu.Unlock()
	if previous != nil {
		previous.close(errors.New("Agent session replaced"))
	}
}

func (r *Registry) detach(value *session) {
	value.close(errors.New("Agent disconnected"))
	r.mu.Lock()
	if r.sessions[value.id] == value {
		delete(r.sessions, value.id)
		agent := r.agents[value.id]
		agent.Connected = false
		agent.LastSeen = r.now().UTC()
		r.agents[value.id] = agent
	}
	r.mu.Unlock()
}

func (r *Registry) touch(value *session) {
	r.mu.Lock()
	if r.sessions[value.id] == value {
		agent := r.agents[value.id]
		agent.LastSeen = r.now().UTC()
		r.agents[value.id] = agent
	}
	r.mu.Unlock()
}

func (r *Registry) Agents(context.Context) ([]Agent, error) {
	r.mu.RLock()
	values := make([]Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		values = append(values, agent)
	}
	r.mu.RUnlock()
	sort.Slice(values, func(i, j int) bool {
		if values[i].Connected != values[j].Connected {
			return values[i].Connected
		}
		return values[i].Name < values[j].Name
	})
	return values, nil
}

func (r *Registry) Containers(ctx context.Context, agentID string) ([]dockerengine.Container, error) {
	var result []dockerengine.Container
	if err := r.query(ctx, agentID, requestContainers, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Registry) Images(ctx context.Context, agentID string) ([]dockerengine.Image, error) {
	var result []dockerengine.Image
	if err := r.query(ctx, agentID, requestImages, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Registry) query(ctx context.Context, agentID, request string, output any) error {
	session, err := r.active(agentID)
	if err != nil {
		return err
	}
	channel, requestID, err := session.open(ctx, message{Kind: kindRequest, Request: request})
	if err != nil {
		return err
	}
	defer session.cancelRequest(requestID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case response, ok := <-channel:
		if !ok {
			return errors.New("Agent disconnected")
		}
		if response.Error != "" {
			return errors.New(response.Error)
		}
		if response.Kind != kindResponse {
			return errors.New("unexpected Agent response")
		}
		if err := json.Unmarshal(response.Payload, output); err != nil {
			return fmt.Errorf("decode Agent response: %w", err)
		}
		return nil
	}
}

func (r *Registry) Logs(ctx context.Context, agentID string, request dockerengine.LogRequest, emit func(dockerengine.LogEvent) error) error {
	session, err := r.active(agentID)
	if err != nil {
		return err
	}
	channel, requestID, err := session.open(ctx, message{
		Kind: kindRequest, Request: requestLogs, Container: request.ContainerID,
		Tail: request.Tail, Since: request.Since, Follow: request.Follow,
	})
	if err != nil {
		return err
	}
	defer session.cancelRequest(requestID)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case response, ok := <-channel:
			if !ok {
				return errors.New("Agent disconnected")
			}
			if response.Error != "" {
				return errors.New(response.Error)
			}
			switch response.Kind {
			case kindLog:
				var event dockerengine.LogEvent
				if err := json.Unmarshal(response.Log, &event); err != nil {
					return fmt.Errorf("decode Agent log event: %w", err)
				}
				if err := emit(event); err != nil {
					return err
				}
			case kindEnd:
				return nil
			default:
				return errors.New("unexpected Agent log response")
			}
		}
	}
}

func (r *Registry) active(agentID string) (*session, error) {
	if !validAgentID(agentID) {
		return nil, errors.New("invalid Agent ID")
	}
	r.mu.RLock()
	value := r.sessions[agentID]
	r.mu.RUnlock()
	if value == nil {
		return nil, errors.New("Agent is offline")
	}
	return value, nil
}

type session struct {
	id, name string
	stream   grpc.ServerStream
	ctx      context.Context
	cancel   context.CancelFunc

	sendMu  sync.Mutex
	mu      sync.Mutex
	pending map[string]chan message
	closed  bool
}

func (s *session) send(value message) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.SendMsg(&value)
}

func (s *session) open(ctx context.Context, request message) (<-chan message, string, error) {
	requestID, err := randomID()
	if err != nil {
		return nil, "", err
	}
	channel := make(chan message, pendingMessages)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, "", errors.New("Agent disconnected")
	}
	s.pending[requestID] = channel
	s.mu.Unlock()
	request.RequestID = requestID
	if err := s.send(request); err != nil {
		s.cancelRequest(requestID)
		return nil, "", err
	}
	return channel, requestID, nil
}

func (s *session) cancelRequest(requestID string) {
	s.mu.Lock()
	channel := s.pending[requestID]
	delete(s.pending, requestID)
	s.mu.Unlock()
	if channel != nil {
		_ = s.send(message{Kind: kindCancel, RequestID: requestID})
	}
}

func (s *session) deliver(value message) {
	s.mu.Lock()
	channel := s.pending[value.RequestID]
	if channel != nil {
		select {
		case channel <- value:
		default:
			delete(s.pending, value.RequestID)
			close(channel)
			go s.send(message{Kind: kindCancel, RequestID: value.RequestID})
		}
	}
	s.mu.Unlock()
}

func (s *session) close(_ error) {
	s.cancel()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for id, channel := range s.pending {
			delete(s.pending, id)
			close(channel)
		}
	}
	s.mu.Unlock()
}

func randomID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func validAgentID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !(character >= 'a' && character <= 'f' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func receiveWithContext(ctx context.Context, stream grpc.ServerStream, value any) error {
	done := make(chan error, 1)
	go func() { done <- stream.RecvMsg(value) }()
	select {
	case <-ctx.Done():
		return status.Error(codes.DeadlineExceeded, "Agent handshake timed out")
	case err := <-done:
		return err
	}
}
