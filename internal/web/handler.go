// Package web serves the embedded browser UI and its read-only HTTP API.
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
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
	"github.com/east-true/docker-log-viewer/internal/transport"
)

//go:embed assets/*
var embeddedAssets embed.FS

var (
	containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	webUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type Backend interface {
	Agents(context.Context) ([]transport.Agent, error)
	Containers(context.Context, string) ([]dockerengine.Container, error)
	Images(context.Context, string) ([]dockerengine.Image, error)
	Logs(context.Context, string, dockerengine.LogRequest, func(dockerengine.LogEvent) error) error
}

type Handler struct {
	backend         Backend
	static          http.Handler
	accessTokenHash [sha256.Size]byte
	username        string
	requireAuth     bool
	secureTransport bool
	logSlots        chan struct{}
}

type Options struct {
	Username        string
	AccessToken     string
	SecureTransport bool
	MaxLogStreams   int
}

func New(backend Backend, options ...Options) (*Handler, error) {
	if backend == nil {
		return nil, errors.New("web backend is required")
	}
	config := Options{Username: "admin", MaxLogStreams: 32}
	if len(options) > 1 {
		return nil, errors.New("only one web options value is allowed")
	}
	if len(options) == 1 {
		config = options[0]
		if config.Username == "" {
			config.Username = "admin"
		}
		if config.MaxLogStreams == 0 {
			config.MaxLogStreams = 32
		}
	}
	if config.MaxLogStreams < 1 || config.MaxLogStreams > 1024 {
		return nil, errors.New("max log streams must be between 1 and 1024")
	}
	if config.AccessToken != "" && len(config.AccessToken) < 32 {
		return nil, errors.New("web access token must contain at least 32 characters")
	}
	if !webUsernamePattern.MatchString(config.Username) {
		return nil, errors.New("web username must contain 1 to 64 letters, digits, dots, underscores, or hyphens")
	}
	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return nil, fmt.Errorf("open embedded assets: %w", err)
	}
	return &Handler{
		backend: backend, static: http.FileServer(http.FS(assets)),
		accessTokenHash: sha256.Sum256([]byte(config.AccessToken)),
		username:        config.Username,
		requireAuth:     config.AccessToken != "", secureTransport: config.SecureTransport,
		logSlots: make(chan struct{}, config.MaxLogStreams),
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if h.secureTransport {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	if h.requireAuth && r.URL.Path != "/api/health" && !h.authenticated(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Docker Log Viewer", charset="UTF-8"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
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

func (h *Handler) authenticated(r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok || username != h.username {
		return false
	}
	provided := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(provided[:], h.accessTokenHash[:]) == 1
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
	case "/api/agents":
		if r.URL.RawQuery != "" {
			writeProblem(w, http.StatusBadRequest, "INVALID_QUERY", "Agent inventory does not accept query parameters")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		value, err := h.backend.Agents(ctx)
		h.respond(w, value, err)
	case "/api/containers":
		agentID, err := decodeAgentQuery(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		value, err := h.backend.Containers(ctx, agentID)
		h.respond(w, value, err)
	case "/api/images":
		agentID, err := decodeAgentQuery(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		value, err := h.backend.Images(ctx, agentID)
		h.respond(w, value, err)
	case "/api/logs":
		h.serveLogs(w, r)
	default:
		writeProblem(w, http.StatusNotFound, "NOT_FOUND", "API route not found")
	}
}

func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	select {
	case h.logSlots <- struct{}{}:
		defer func() { <-h.logSlots }()
	default:
		writeProblem(w, http.StatusTooManyRequests, "TOO_MANY_LOG_STREAMS", "too many concurrent log streams")
		return
	}
	request, agentID, target, err := decodeLogRequest(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
		return
	}
	ctx := r.Context()
	containers, err := h.backend.Containers(ctx, agentID)
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
	if streamErr := h.backend.Logs(ctx, agentID, request, emit); streamErr != nil && ctx.Err() == nil {
		_ = emit(dockerengine.LogEvent{
			Type: "error", ContainerID: selected.ID, ContainerName: selected.Name,
			Message: streamErr.Error(), Retryable: true, ObservedAt: time.Now().UTC(),
		})
	}
	if ctx.Err() == nil {
		_ = emit(dockerengine.LogEvent{Type: "end", ObservedAt: time.Now().UTC()})
	}
}

func decodeLogRequest(r *http.Request) (dockerengine.LogRequest, string, string, error) {
	query := r.URL.Query()
	for key := range query {
		switch key {
		case "agent", "container", "tail", "since", "follow":
		default:
			return dockerengine.LogRequest{}, "", "", fmt.Errorf("unsupported query parameter %q", key)
		}
		if len(query[key]) != 1 {
			return dockerengine.LogRequest{}, "", "", fmt.Errorf("query parameter %q must appear once", key)
		}
	}
	agentID := query.Get("agent")
	if !agentIDPattern.MatchString(agentID) {
		return dockerengine.LogRequest{}, "", "", errors.New("agent must be a valid UUID")
	}
	target := query.Get("container")
	if target == "" {
		return dockerengine.LogRequest{}, "", "", errors.New("container is required")
	}
	if !containerIDPattern.MatchString(target) {
		return dockerengine.LogRequest{}, "", "", errors.New("container must be a full 64-character ID")
	}
	tail := query.Get("tail")
	if tail == "" {
		tail = "200"
	}
	if tail != "all" {
		lines, err := strconv.Atoi(tail)
		if err != nil || lines < 1 || lines > 10_000 {
			return dockerengine.LogRequest{}, "", "", errors.New("tail must be 'all' or a number from 1 to 10000")
		}
	}
	since := query.Get("since")
	allowedSince := map[string]bool{"": true, "5m": true, "15m": true, "1h": true, "6h": true, "24h": true, "7d": true}
	if !allowedSince[since] {
		if len(since) > 64 {
			return dockerengine.LogRequest{}, "", "", errors.New("since timestamp is too long")
		}
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			return dockerengine.LogRequest{}, "", "", errors.New("since must be a supported duration or RFC3339 timestamp")
		}
	}
	follow := true
	if raw := query.Get("follow"); raw != "" {
		var err error
		follow, err = strconv.ParseBool(raw)
		if err != nil {
			return dockerengine.LogRequest{}, "", "", errors.New("follow must be true or false")
		}
	}
	return dockerengine.LogRequest{Tail: tail, Since: since, Follow: follow}, agentID, target, nil
}

var agentIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func decodeAgentQuery(r *http.Request) (string, error) {
	query := r.URL.Query()
	if len(query) != 1 || len(query["agent"]) != 1 || !agentIDPattern.MatchString(query.Get("agent")) {
		return "", errors.New("exactly one valid Agent UUID is required")
	}
	return query.Get("agent"), nil
}

func (h *Handler) respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE", err.Error())
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
