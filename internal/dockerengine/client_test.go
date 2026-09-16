package dockerengine

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

type fakeEngine struct {
	containers []container.Summary
	images     []image.Summary
	inspect    container.InspectResponse
	logs       []byte
	logOptions client.ContainerLogsOptions
}

func (f *fakeEngine) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{Items: f.containers}, nil
}

func (f *fakeEngine) ImageList(context.Context, client.ImageListOptions) (client.ImageListResult, error) {
	return client.ImageListResult{Items: f.images}, nil
}

func (f *fakeEngine) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return client.ContainerInspectResult{Container: f.inspect}, nil
}

func (f *fakeEngine) ContainerLogs(_ context.Context, _ string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	f.logOptions = options
	return io.NopCloser(bytes.NewReader(f.logs)), nil
}

func (f *fakeEngine) Close() error { return nil }

func TestContainersIncludesStoppedAndSortsRunningFirst(t *testing.T) {
	engine := &fakeEngine{containers: []container.Summary{
		{ID: "b" + string(make([]byte, 63)), Names: []string{"/z-stopped"}, Image: "busybox", State: "exited", Status: "Exited (0)"},
		{ID: "a" + string(make([]byte, 63)), Names: []string{"/a-running"}, Image: "nginx", State: "running", Status: "Up 1 minute"},
	}}
	items, err := New(engine).Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "a-running" || items[1].State != "exited" {
		t.Fatalf("unexpected containers: %#v", items)
	}
}

func TestImagesReportsActualContainerUsage(t *testing.T) {
	imageID := "sha256:" + repeat("a", 64)
	engine := &fakeEngine{
		containers: []container.Summary{
			{ID: repeat("b", 64), Names: []string{"/web"}, ImageID: imageID, State: "running", Status: "Up"},
			{ID: repeat("c", 64), Names: []string{"/old-web"}, ImageID: imageID, State: "exited", Status: "Exited"},
		},
		images: []image.Summary{
			{ID: imageID, RepoTags: []string{"example/web:latest"}, Size: 42},
			{ID: "sha256:" + repeat("d", 64), RepoTags: []string{"example/unused:latest"}, Size: 12},
		},
	}
	items, err := New(engine).Images(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].InUse || items[0].ContainerCount != 2 || items[0].RunningCount != 1 {
		t.Fatalf("unexpected image usage: %#v", items)
	}
	if items[1].Containers == nil {
		t.Fatal("unused image containers must be an empty array, not nil")
	}
}

func TestLogsDemultiplexesStdoutAndStderr(t *testing.T) {
	var stream bytes.Buffer
	writeFrame(&stream, 1, "2026-01-01T00:00:00Z hello\n")
	writeFrame(&stream, 2, "2026-01-01T00:00:01Z warning\n")
	engine := &fakeEngine{
		inspect: container.InspectResponse{Name: "/web", Config: &container.Config{Tty: false}},
		logs:    stream.Bytes(),
	}
	var events []LogEvent
	err := New(engine).Logs(context.Background(), LogRequest{ContainerID: repeat("d", 64), Tail: "20", Since: "2026-01-01T00:00:00.123456789Z", Follow: true}, func(event LogEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Stream != "stdout" || events[1].Stream != "stderr" || events[0].ContainerName != "web" {
		t.Fatalf("unexpected events: %#v", events)
	}
	if !engine.logOptions.Follow || engine.logOptions.Tail != "20" || engine.logOptions.Since != "2026-01-01T00:00:00.123456789Z" || !engine.logOptions.Timestamps || !engine.logOptions.ShowStdout || !engine.logOptions.ShowStderr {
		t.Fatalf("log options = %#v", engine.logOptions)
	}
}

func writeFrame(target *bytes.Buffer, stream byte, payload string) {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	target.Write(header)
	target.WriteString(payload)
}

func repeat(value string, count int) string {
	var result string
	for range count {
		result += value
	}
	return result
}
