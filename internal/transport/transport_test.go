package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"google.golang.org/grpc/test/bufconn"
)

const testToken = "test-only-agent-token-that-is-long-enough"

type testEngine struct {
	containerID string
}

func (e *testEngine) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{Items: []container.Summary{{
		ID: e.containerID, Names: []string{"/example"}, Image: "example/app:latest",
		ImageID: "sha256:" + e.containerID, State: "running", Status: "Up",
	}}}, nil
}

func (e *testEngine) ImageList(context.Context, client.ImageListOptions) (client.ImageListResult, error) {
	return client.ImageListResult{Items: []image.Summary{{ID: "sha256:" + e.containerID, RepoTags: []string{"example/app:latest"}}}}, nil
}

func (e *testEngine) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return client.ContainerInspectResult{Container: container.InspectResponse{Name: "/example", Config: &container.Config{Tty: true}}}, nil
}

func (e *testEngine) ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	return io.NopCloser(bytes.NewBufferString("2026-09-15T01:02:03.123456789Z hello\n")), nil
}

func (*testEngine) Close() error { return nil }

func TestReverseSessionRelaysInventoryAndLogs(t *testing.T) {
	registry, err := NewRegistry(testToken)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(ctx, listener, registry) }()

	containerID := repeatHex("a", 64)
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- runAgentSession(ctx, AgentConfig{
			Address: "passthrough:///test", ID: "11111111-1111-4111-8111-111111111111",
			Name: "test-host", Token: testToken, Insecure: true,
			Engine: dockerengine.New(&testEngine{containerID: containerID}),
			Dialer: func(context.Context, string) (net.Conn, error) { return listener.Dial() },
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		agents, err := registry.Agents(context.Background())
		if err == nil && len(agents) == 1 && agents[0].Connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Agent did not connect")
		}
		time.Sleep(10 * time.Millisecond)
	}

	containers, err := registry.Containers(context.Background(), "11111111-1111-4111-8111-111111111111")
	if err != nil || len(containers) != 1 || containers[0].Name != "example" {
		t.Fatalf("containers = %#v, err = %v", containers, err)
	}
	images, err := registry.Images(context.Background(), "11111111-1111-4111-8111-111111111111")
	if err != nil || len(images) != 1 || !images[0].InUse {
		t.Fatalf("images = %#v, err = %v", images, err)
	}
	var events []dockerengine.LogEvent
	err = registry.Logs(context.Background(), "11111111-1111-4111-8111-111111111111", dockerengine.LogRequest{
		ContainerID: containerID, Tail: "10",
	}, func(event dockerengine.LogEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || len(events) != 1 || events[0].ContainerName != "example" {
		t.Fatalf("events = %#v, err = %v", events, err)
	}

	cancel()
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Server did not stop")
	}
	select {
	case <-agentDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Agent did not stop")
	}
}

func TestAgentAuthenticationRejectsWrongToken(t *testing.T) {
	registry, err := NewRegistry(testToken)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, listener, registry)
	err = runAgentSession(ctx, AgentConfig{
		Address: "passthrough:///test", ID: "22222222-2222-4222-8222-222222222222",
		Name: "rejected", Token: "wrong-token-that-is-still-long-enough", Insecure: true,
		Engine: dockerengine.New(&testEngine{containerID: repeatHex("b", 64)}),
		Dialer: func(context.Context, string) (net.Conn, error) { return listener.Dial() },
	})
	if err == nil {
		t.Fatal("wrong token was accepted")
	}
}

func TestLoadOrCreateAgentIDIsDurableAndPrivate(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "agent")
	first, err := LoadOrCreateAgentID(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateAgentID(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !validAgentID(first) {
		t.Fatalf("Agent IDs = %q, %q", first, second)
	}
	info, err := os.Stat(filepath.Join(directory, "agent-id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("Agent ID mode = %o", info.Mode().Perm())
	}
}

func repeatHex(value string, count int) string {
	var result string
	for range count {
		result += value
	}
	return result
}
