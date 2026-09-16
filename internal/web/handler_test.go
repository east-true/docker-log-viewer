package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
	"github.com/east-true/docker-log-viewer/internal/transport"
)

const testAgentID = "11111111-1111-4111-8111-111111111111"

type fakeBackend struct {
	agents      []transport.Agent
	containers  []dockerengine.Container
	images      []dockerengine.Image
	mu          sync.Mutex
	logTargets  []string
	logAgents   []string
	logRequests []dockerengine.LogRequest
	logErr      error
}

func (f *fakeBackend) Agents(context.Context) ([]transport.Agent, error) {
	return append([]transport.Agent(nil), f.agents...), nil
}

func (f *fakeBackend) Containers(context.Context, string) ([]dockerengine.Container, error) {
	return append([]dockerengine.Container(nil), f.containers...), nil
}

func (f *fakeBackend) Images(context.Context, string) ([]dockerengine.Image, error) {
	return append([]dockerengine.Image(nil), f.images...), nil
}

func (f *fakeBackend) Logs(_ context.Context, agentID string, request dockerengine.LogRequest, emit func(dockerengine.LogEvent) error) error {
	f.mu.Lock()
	f.logAgents = append(f.logAgents, agentID)
	f.logTargets = append(f.logTargets, request.ContainerID)
	f.logRequests = append(f.logRequests, request)
	f.mu.Unlock()
	if err := emit(dockerengine.LogEvent{Type: "log", ContainerID: request.ContainerID, Message: "hello\n", ObservedAt: time.Now()}); err != nil {
		return err
	}
	return f.logErr
}

func testHandler(t *testing.T, backend Backend) *Handler {
	t.Helper()
	handler, err := New(backend)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestEmbeddedUIAndSecurityHeaders(t *testing.T) {
	handler := testHandler(t, &fakeBackend{})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing Content-Security-Policy")
	}
	if response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing X-Frame-Options")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store cache policy")
	}
}

func TestWebAccessTokenProtectsUIAndAPI(t *testing.T) {
	token := repeatID("a")
	handler, err := New(&fakeBackend{}, Options{Username: "viewer", AccessToken: token, SecureTransport: true, MaxLogStreams: 1})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthorized response = %d, headers = %#v", unauthorized.Code, unauthorized.Header())
	}

	wrongUserRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	wrongUserRequest.SetBasicAuth("admin", token)
	wrongUser := httptest.NewRecorder()
	handler.ServeHTTP(wrongUser, wrongUserRequest)
	if wrongUser.Code != http.StatusUnauthorized {
		t.Fatalf("wrong user status = %d", wrongUser.Code)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	authorizedRequest.SetBasicAuth("viewer", token)
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d", authorized.Code)
	}
	if authorized.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("missing HSTS for secure transport")
	}
}

func TestWebAccessRejectsInvalidUsername(t *testing.T) {
	for _, username := range []string{"bad:user", "has space", "-leading", repeatID("a") + "a"} {
		t.Run(username, func(t *testing.T) {
			if _, err := New(&fakeBackend{}, Options{Username: username, AccessToken: repeatID("a")}); err == nil {
				t.Fatal("expected invalid username error")
			}
		})
	}
}

func TestContainerInventory(t *testing.T) {
	backend := &fakeBackend{containers: []dockerengine.Container{{ID: repeatID("a"), Name: "web"}}}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/containers?agent="+testAgentID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	var items []dockerengine.Container
	if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "web" {
		t.Fatalf("unexpected response: %#v", items)
	}
}

func TestSelectedContainerStartsOneLogStream(t *testing.T) {
	backend := &fakeBackend{containers: []dockerengine.Container{
		{ID: repeatID("a"), Name: "web"},
		{ID: repeatID("b"), Name: "worker"},
	}}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?agent="+testAgentID+"&container="+repeatID("b")+"&tail=100&follow=false", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.logTargets) != 1 || backend.logTargets[0] != repeatID("b") {
		t.Fatalf("log targets = %#v", backend.logTargets)
	}
	if len(backend.logAgents) != 1 || backend.logAgents[0] != testAgentID {
		t.Fatalf("log Agents = %#v", backend.logAgents)
	}
	if response.Header().Get("Content-Type") != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
}

func TestLogsRejectsShortContainerID(t *testing.T) {
	handler := testHandler(t, &fakeBackend{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?agent="+testAgentID+"&container=abc", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestLogsAcceptsRFC3339NanoSinceCursor(t *testing.T) {
	backend := &fakeBackend{containers: []dockerengine.Container{{ID: repeatID("a"), Name: "web"}}}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?agent="+testAgentID+"&container="+repeatID("a")+"&tail=all&since=2026-09-15T01%3A02%3A03.123456789Z&follow=true", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.logRequests) != 1 || backend.logRequests[0].Since != "2026-09-15T01:02:03.123456789Z" || backend.logRequests[0].Tail != "all" || !backend.logRequests[0].Follow {
		t.Fatalf("log requests = %#v", backend.logRequests)
	}
}

func TestLogStreamMarksBackendDisconnectAsRetryable(t *testing.T) {
	backend := &fakeBackend{
		containers: []dockerengine.Container{{ID: repeatID("a"), Name: "web"}},
		logErr:     errors.New("Agent disconnected"),
	}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?agent="+testAgentID+"&container="+repeatID("a")+"&follow=true", nil))
	decoder := json.NewDecoder(response.Body)
	found := false
	for {
		var event dockerengine.LogEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			found = true
			if event.Message != "Agent disconnected" || !event.Retryable {
				t.Fatalf("unexpected error event: %#v", event)
			}
		}
	}
	if !found {
		t.Fatal("missing retryable stream error event")
	}
}

func TestLogsRejectsInvalidSinceCursor(t *testing.T) {
	backend := &fakeBackend{containers: []dockerengine.Container{{ID: repeatID("a"), Name: "web"}}}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?agent="+testAgentID+"&container="+repeatID("a")+"&since=yesterday", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestAPIIsReadOnly(t *testing.T) {
	handler := testHandler(t, &fakeBackend{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/containers?agent="+testAgentID, nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.Code)
	}
}

func repeatID(character string) string {
	value := ""
	for range 64 {
		value += character
	}
	return value
}
