// Package web serves the embedded browser UI and its read-only HTTP API.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
)

//go:embed assets/*
var embeddedAssets embed.FS

var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Backend interface {
	Containers(context.Context) ([]dockerengine.Container, error)
	Images(context.Context) ([]dockerengine.Image, error)
	Logs(context.Context, dockerengine.LogRequest, func(dockerengine.LogEvent) error) error
}

type Handler struct {
	backend Backend
	static  http.Handler
}

func New(backend Backend) (*Handler, error) {
	if backend == nil {
		return nil, errors.New("web backend is required")
	}
	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return nil, fmt.Errorf("open embedded assets: %w", err)
	}
	return &Handler{backend: backend, static: http.FileServer(http.FS(assets))}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.serveAPI(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "UI assets require GET or HEAD")
		return
	}
	if r.URL.Path != "/" && !strings.Contains(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], ".") {
		clone := r.Clone(r.Context())
		clone.URL.Path = "/"
		h.static.ServeHTTP(w, clone)
		return
	}
	h.static.ServeHTTP(w, r)
}

func (h *Handler) serveAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "API routes are read-only and require GET")
		return
	}
	switch r.URL.Path {
	case "/api/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/api/containers":
		if r.URL.RawQuery != "" {
			writeProblem(w, http.StatusBadRequest, "INVALID_QUERY", "container inventory does not accept query parameters")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		value, err := h.backend.Containers(ctx)
		h.respond(w, value, err)
	case "/api/images":
		if r.URL.RawQuery != "" {
			writeProblem(w, http.StatusBadRequest, "INVALID_QUERY", "image inventory does not accept query parameters")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		value, err := h.backend.Images(ctx)
		h.respond(w, value, err)
	case "/api/logs":
		h.serveLogs(w, r)
	default:
		writeProblem(w, http.StatusNotFound, "NOT_FOUND", "API route not found")
	}
}

func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	request, target, err := decodeLogRequest(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
		return
	}
	ctx := r.Context()
	containers, err := h.backend.Containers(ctx)
	if err != nil {
		h.respond(w, nil, err)
		return
	}
	var selected *dockerengine.Container
	for index := range containers {
		if containers[index].ID == target {
			selected = &containers[index]
			break
		}
	}
	if selected == nil {
		writeProblem(w, http.StatusNotFound, "CONTAINER_NOT_FOUND", "container no longer exists")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, canFlush := w.(http.Flusher)
	encoder := json.NewEncoder(w)
	emit := func(event dockerengine.LogEvent) error {
		if err := encoder.Encode(event); err != nil {
			return err
		}
		if canFlush {
			flusher.Flush()
		}
		return nil
	}
	_ = emit(dockerengine.LogEvent{Type: "ready", Message: "1", ObservedAt: time.Now().UTC()})
	request.ContainerID = selected.ID
	if streamErr := h.backend.Logs(ctx, request, emit); streamErr != nil && ctx.Err() == nil {
		_ = emit(dockerengine.LogEvent{
			Type: "error", ContainerID: selected.ID, ContainerName: selected.Name,
			Message: streamErr.Error(), ObservedAt: time.Now().UTC(),
		})
	}
	if ctx.Err() == nil {
		_ = emit(dockerengine.LogEvent{Type: "end", ObservedAt: time.Now().UTC()})
	}
}

func decodeLogRequest(r *http.Request) (dockerengine.LogRequest, string, error) {
	query := r.URL.Query()
	for key := range query {
		switch key {
		case "container", "tail", "since", "follow":
		default:
			return dockerengine.LogRequest{}, "", fmt.Errorf("unsupported query parameter %q", key)
		}
		if len(query[key]) != 1 {
			return dockerengine.LogRequest{}, "", fmt.Errorf("query parameter %q must appear once", key)
		}
	}
	target := query.Get("container")
	if target == "" {
		return dockerengine.LogRequest{}, "", errors.New("container is required")
	}
	if !containerIDPattern.MatchString(target) {
		return dockerengine.LogRequest{}, "", errors.New("container must be a full 64-character ID")
	}
	tail := query.Get("tail")
	if tail == "" {
		tail = "200"
	}
	if tail != "all" {
		lines, err := strconv.Atoi(tail)
		if err != nil || lines < 1 || lines > 10_000 {
			return dockerengine.LogRequest{}, "", errors.New("tail must be 'all' or a number from 1 to 10000")
		}
	}
	since := query.Get("since")
	allowedSince := map[string]bool{"": true, "5m": true, "15m": true, "1h": true, "6h": true, "24h": true, "7d": true}
	if !allowedSince[since] {
		if len(since) > 64 {
			return dockerengine.LogRequest{}, "", errors.New("since timestamp is too long")
		}
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			return dockerengine.LogRequest{}, "", errors.New("since must be a supported duration or RFC3339 timestamp")
		}
	}
	follow := true
	if raw := query.Get("follow"); raw != "" {
		var err error
		follow, err = strconv.ParseBool(raw)
		if err != nil {
			return dockerengine.LogRequest{}, "", errors.New("follow must be true or false")
		}
	}
	return dockerengine.LogRequest{Tail: tail, Since: since, Follow: follow}, target, nil
}

func (h *Handler) respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "DOCKER_UNAVAILABLE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"code": code, "message": message, "status": status,
	})
}
