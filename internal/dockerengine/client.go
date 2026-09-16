// Package dockerengine is the narrow, read-only Docker Engine boundary used by
// docker-log-viewer.
package dockerengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

type Engine interface {
	ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
	ImageList(context.Context, client.ImageListOptions) (client.ImageListResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	Close() error
}

type Client struct{ engine Engine }

type Container struct {
	ID        string `json:"id"`
	ShortID   string `json:"short_id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	ImageID   string `json:"image_id"`
	State     string `json:"state"`
	Status    string `json:"status"`
	Health    string `json:"health,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type ImageContainer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Status string `json:"status"`
}

type Image struct {
	ID             string           `json:"id"`
	ShortID        string           `json:"short_id"`
	Tags           []string         `json:"tags"`
	Digests        []string         `json:"digests,omitempty"`
	Size           int64            `json:"size"`
	CreatedAt      int64            `json:"created_at"`
	InUse          bool             `json:"in_use"`
	RunningCount   int              `json:"running_count"`
	ContainerCount int              `json:"container_count"`
	Containers     []ImageContainer `json:"containers"`
}

type LogRequest struct {
	ContainerID string
	Tail        string
	Since       string
	Follow      bool
}

type LogEvent struct {
	Type          string    `json:"type"`
	ContainerID   string    `json:"container_id,omitempty"`
	ContainerName string    `json:"container_name,omitempty"`
	Stream        string    `json:"stream,omitempty"`
	Message       string    `json:"message,omitempty"`
	Retryable     bool      `json:"retryable,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
}

func Open() (*Client, error) {
	api, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return New(api), nil
}

func New(engine Engine) *Client { return &Client{engine: engine} }

func (c *Client) Close() error { return c.engine.Close() }

func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	result, err := c.engine.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	items := make([]Container, 0, len(result.Items))
	for _, value := range result.Items {
		name := shortID(value.ID)
		if len(value.Names) > 0 {
			name = strings.TrimPrefix(value.Names[0], "/")
		}
		health := ""
		if value.Health != nil {
			health = string(value.Health.Status)
		}
		items = append(items, Container{
			ID: value.ID, ShortID: shortID(value.ID), Name: name, Image: value.Image,
			ImageID: value.ImageID, State: string(value.State), Status: value.Status,
			Health: health, CreatedAt: value.Created,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if (items[i].State == "running") != (items[j].State == "running") {
			return items[i].State == "running"
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func (c *Client) Images(ctx context.Context) ([]Image, error) {
	containers, err := c.Containers(ctx)
	if err != nil {
		return nil, err
	}
	result, err := c.engine.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	usage := make(map[string][]ImageContainer)
	for _, value := range containers {
		id := normalizeID(value.ImageID)
		usage[id] = append(usage[id], ImageContainer{
			ID: value.ID, Name: value.Name, State: value.State, Status: value.Status,
		})
	}
	items := make([]Image, 0, len(result.Items))
	for _, value := range result.Items {
		containers := usage[normalizeID(value.ID)]
		if containers == nil {
			containers = []ImageContainer{}
		}
		running := 0
		for _, linked := range containers {
			if linked.State == "running" {
				running++
			}
		}
		tags := append([]string(nil), value.RepoTags...)
		if len(tags) == 0 {
			tags = []string{"<none>:<none>"}
		}
		sort.Strings(tags)
		items = append(items, Image{
			ID: value.ID, ShortID: shortID(normalizeID(value.ID)), Tags: tags,
			Digests: append([]string(nil), value.RepoDigests...), Size: value.Size,
			CreatedAt: value.Created, InUse: len(containers) > 0, RunningCount: running,
			ContainerCount: len(containers), Containers: containers,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].InUse != items[j].InUse {
			return items[i].InUse
		}
		return strings.ToLower(items[i].Tags[0]) < strings.ToLower(items[j].Tags[0])
	})
	return items, nil
}

func (c *Client) Logs(ctx context.Context, request LogRequest, emit func(LogEvent) error) error {
	if emit == nil {
		return errors.New("log event callback is required")
	}
	inspect, err := c.engine.ContainerInspect(ctx, request.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	name := strings.TrimPrefix(inspect.Container.Name, "/")
	body, err := c.engine.ContainerLogs(ctx, request.ContainerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     request.Follow,
		Tail:       request.Tail,
		Since:      request.Since,
		Timestamps: true,
	})
	if err != nil {
		return fmt.Errorf("read container logs: %w", err)
	}
	defer body.Close()

	stdout := eventWriter{ctx: ctx, containerID: request.ContainerID, containerName: name, stream: "stdout", emit: emit}
	stderr := eventWriter{ctx: ctx, containerID: request.ContainerID, containerName: name, stream: "stderr", emit: emit}
	if inspect.Container.Config != nil && inspect.Container.Config.Tty {
		_, err = io.Copy(stdout, body)
	} else {
		_, err = stdcopy.StdCopy(stdout, stderr, body)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("stream container logs: %w", err)
	}
	return err
}

type eventWriter struct {
	ctx                                context.Context
	containerID, containerName, stream string
	emit                               func(LogEvent) error
}

func (w eventWriter) Write(payload []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(payload) == 0 {
		return 0, nil
	}
	if err := w.emit(LogEvent{
		Type: "log", ContainerID: w.containerID, ContainerName: w.containerName,
		Stream: w.stream, Message: string(bytes.Clone(payload)), ObservedAt: time.Now().UTC(),
	}); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func normalizeID(value string) string { return strings.TrimPrefix(value, "sha256:") }

func shortID(value string) string {
	value = normalizeID(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
