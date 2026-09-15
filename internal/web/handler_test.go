package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
)

type fakeBackend struct {
	containers  []dockerengine.Container
	images      []dockerengine.Image
	mu          sync.Mutex
	logTargets  []string
	logRequests []dockerengine.LogRequest
}

func (f *fakeBackend) Containers(context.Context) ([]dockerengine.Container, error) {
	return append([]dockerengine.Container(nil), f.containers...), nil
}

func (f *fakeBackend) Images(context.Context) ([]dockerengine.Image, error) {
	return append([]dockerengine.Image(nil), f.images...), nil
}

func (f *fakeBackend) Logs(_ context.Context, request dockerengine.LogRequest, emit func(dockerengine.LogEvent) error) error {
	f.mu.Lock()
	f.logTargets = append(f.logTargets, request.ContainerID)
	f.logRequests = append(f.logRequests, request)
	f.mu.Unlock()
	return emit(dockerengine.LogEvent{Type: "log", ContainerID: request.ContainerID, Message: "hello\n", ObservedAt: time.Now()})
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
}

func TestContainerInventory(t *testing.T) {
	backend := &fakeBackend{containers: []dockerengine.Container{{ID: repeatID("a"), Name: "web"}}}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/containers", nil))
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
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?container="+repeatID("b")+"&tail=100&follow=false", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.logTargets) != 1 || backend.logTargets[0] != repeatID("b") {
		t.Fatalf("log targets = %#v", backend.logTargets)
	}
	if response.Header().Get("Content-Type") != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
}

func TestLogsRejectsShortContainerID(t *testing.T) {
	handler := testHandler(t, &fakeBackend{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?container=abc", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestLogsAcceptsRFC3339NanoSinceCursor(t *testing.T) {
	backend := &fakeBackend{containers: []dockerengine.Container{{ID: repeatID("a"), Name: "web"}}}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?container="+repeatID("a")+"&tail=all&since=2026-09-15T01%3A02%3A03.123456789Z&follow=true", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.logRequests) != 1 || backend.logRequests[0].Since != "2026-09-15T01:02:03.123456789Z" || backend.logRequests[0].Tail != "all" || !backend.logRequests[0].Follow {
		t.Fatalf("log requests = %#v", backend.logRequests)
	}
}

func TestLogsRejectsInvalidSinceCursor(t *testing.T) {
	backend := &fakeBackend{containers: []dockerengine.Container{{ID: repeatID("a"), Name: "web"}}}
	handler := testHandler(t, backend)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/logs?container="+repeatID("a")+"&since=yesterday", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestAPIIsReadOnly(t *testing.T) {
	handler := testHandler(t, &fakeBackend{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/containers", nil))
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
