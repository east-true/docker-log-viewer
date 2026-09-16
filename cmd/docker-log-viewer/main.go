package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
	"github.com/east-true/docker-log-viewer/internal/transport"
	"github.com/east-true/docker-log-viewer/internal/web"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: docker-log-viewer <server|agent> [options]")
	}
	switch args[0] {
	case "server":
		return runServer(ctx, args[1:])
	case "agent":
		return runAgent(ctx, args[1:])
	default:
		return fmt.Errorf("unknown mode %q; expected server or agent", args[0])
	}
}

func runServer(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("docker-log-viewer server", flag.ContinueOnError)
	listen := flags.String("listen", envOr("DOCKER_LOG_VIEWER_LISTEN", "127.0.0.1:8080"), "browser HTTP listen address")
	agentListen := flags.String("agent-listen", envOr("DOCKER_LOG_VIEWER_AGENT_LISTEN", "127.0.0.1:9080"), "Agent gRPC listen address")
	tokenFile := flags.String("agent-token-file", "", "file containing the shared Agent token")
	certificateFile := flags.String("tls-cert", "", "TLS certificate for Agent transport")
	privateKeyFile := flags.String("tls-key", "", "TLS private key for Agent transport")
	insecureAgent := flags.Bool("agent-insecure", false, "allow plaintext Agent transport")
	webTokenFile := flags.String("web-token-file", "", "file containing the browser access token")
	webUsername := flags.String("web-user", envOr("DOCKER_LOG_VIEWER_WEB_USER", "admin"), "browser Basic auth username")
	webCertificateFile := flags.String("web-tls-cert", "", "TLS certificate for the browser UI")
	webPrivateKeyFile := flags.String("web-tls-key", "", "TLS private key for the browser UI")
	maxLogStreams := flags.Int("max-log-streams", 32, "maximum concurrent browser log streams")
	if err := flags.Parse(args); err != nil {
		return err
	}
	token, err := loadToken(*tokenFile)
	if err != nil {
		return err
	}
	registry, err := transport.NewRegistry(token)
	if err != nil {
		return err
	}
	webToken, err := loadOptionalToken(*webTokenFile, "DOCKER_LOG_VIEWER_WEB_TOKEN", "web access")
	if err != nil {
		return err
	}
	browserTLS, err := browserTLSConfig(*webCertificateFile, *webPrivateKeyFile)
	if err != nil {
		return err
	}
	handler, err := web.New(registry, web.Options{
		Username: *webUsername, AccessToken: webToken,
		SecureTransport: browserTLS != nil, MaxLogStreams: *maxLogStreams,
	})
	if err != nil {
		return fmt.Errorf("create web handler: %w", err)
	}
	agentListener, err := net.Listen("tcp", *agentListen)
	if err != nil {
		return fmt.Errorf("listen for Agents: %w", err)
	}
	defer agentListener.Close()
	webListener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen for browser UI: %w", err)
	}
	defer webListener.Close()
	grpcOptions, err := serverTransportOptions(*certificateFile, *privateKeyFile, *insecureAgent)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsChannel := make(chan error, 2)
	go func() {
		err := transport.Serve(runCtx, agentListener, registry, grpcOptions...)
		if err != nil && runCtx.Err() == nil {
			errorsChannel <- fmt.Errorf("serve Agent transport: %w", err)
		}
	}()
	httpServer := &http.Server{
		Addr: *listen, Handler: handler, TLSConfig: browserTLS,
		ReadTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	go func() {
		scheme := "http"
		listener := webListener
		if browserTLS != nil {
			scheme = "https"
			listener = tls.NewListener(webListener, browserTLS)
		}
		if webToken == "" && !isLoopbackAddress(*listen) {
			log.Printf("WARNING: browser UI is exposed on %s without authentication", *listen)
		}
		if webToken != "" && browserTLS == nil && !isLoopbackAddress(*listen) {
			log.Printf("WARNING: browser access credentials and logs are crossing the network without TLS")
		}
		log.Printf("Docker Log Viewer Server UI listening on %s://%s", scheme, *listen)
		log.Printf("Docker Log Viewer Server accepting Agents on %s", *agentListen)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- fmt.Errorf("serve HTTP: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errorsChannel:
		cancel()
		return err
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	return httpServer.Shutdown(shutdownCtx)
}

func browserTLSConfig(certificateFile, privateKeyFile string) (*tls.Config, error) {
	if certificateFile == "" && privateKeyFile == "" {
		return nil, nil
	}
	if certificateFile == "" || privateKeyFile == "" {
		return nil, errors.New("browser TLS requires both -web-tls-cert and -web-tls-key")
	}
	certificate, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load browser TLS certificate: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}, nil
}

func runAgent(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("docker-log-viewer agent", flag.ContinueOnError)
	serverAddress := flags.String("server", envOr("DOCKER_LOG_VIEWER_SERVER", "127.0.0.1:9080"), "Server Agent transport address")
	stateDirectory := flags.String("state-dir", envOr("DOCKER_LOG_VIEWER_STATE_DIR", "/var/lib/docker-log-viewer"), "durable Agent state directory")
	displayName := flags.String("name", envOr("DOCKER_LOG_VIEWER_AGENT_NAME", hostname()), "Agent display name")
	tokenFile := flags.String("agent-token-file", "", "file containing the shared Agent token")
	serverCAFile := flags.String("server-ca", "", "PEM CA used to authenticate the Server")
	tlsServerName := flags.String("tls-server-name", "", "expected Server certificate name")
	insecureTransport := flags.Bool("insecure", false, "allow plaintext connection to the Server")
	if err := flags.Parse(args); err != nil {
		return err
	}
	token, err := loadToken(*tokenFile)
	if err != nil {
		return err
	}
	agentID, err := transport.LoadOrCreateAgentID(*stateDirectory)
	if err != nil {
		return err
	}
	tlsConfig, err := agentTLSConfig(*serverCAFile, *tlsServerName, *insecureTransport)
	if err != nil {
		return err
	}
	engine, err := dockerengine.Open()
	if err != nil {
		return fmt.Errorf("open Docker Engine client: %w", err)
	}
	defer engine.Close()
	return transport.RunAgent(ctx, transport.AgentConfig{
		Address: *serverAddress, ID: agentID, Name: strings.TrimSpace(*displayName), Token: token,
		TLSConfig: tlsConfig, Insecure: *insecureTransport, Engine: engine, Logf: log.Printf,
	})
}

func serverTransportOptions(certificateFile, privateKeyFile string, insecureTransport bool) ([]grpc.ServerOption, error) {
	if insecureTransport {
		if certificateFile != "" || privateKeyFile != "" {
			return nil, errors.New("do not combine -agent-insecure with TLS certificate options")
		}
		return nil, nil
	}
	if certificateFile == "" || privateKeyFile == "" {
		return nil, errors.New("Agent transport requires -tls-cert and -tls-key, or explicit -agent-insecure")
	}
	certificate, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load Agent transport certificate: %w", err)
	}
	config := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(config))}, nil
}

func agentTLSConfig(caFile, serverName string, insecureTransport bool) (*tls.Config, error) {
	if insecureTransport {
		if caFile != "" || serverName != "" {
			return nil, errors.New("do not combine -insecure with Agent TLS options")
		}
		return nil, nil
	}
	if caFile == "" {
		return nil, errors.New("Agent requires -server-ca, or explicit -insecure")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read Server CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("Server CA file contains no certificate")
	}
	return &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS13}, nil
}

func loadToken(path string) (string, error) {
	if path == "" {
		value := strings.TrimSpace(os.Getenv("DOCKER_LOG_VIEWER_AGENT_TOKEN"))
		if len(value) < 32 {
			return "", errors.New("set -agent-token-file or DOCKER_LOG_VIEWER_AGENT_TOKEN with at least 32 characters")
		}
		return value, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Agent token: %w", err)
	}
	value := strings.TrimSpace(string(data))
	if len(value) < 32 {
		return "", errors.New("Agent token must contain at least 32 characters")
	}
	return value, nil
}

func loadOptionalToken(path, environment, label string) (string, error) {
	var value string
	if path == "" {
		value = strings.TrimSpace(os.Getenv(environment))
	} else {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s token: %w", label, err)
		}
		value = strings.TrimSpace(string(data))
	}
	if value != "" && len(value) < 32 {
		return "", fmt.Errorf("%s token must contain at least 32 characters", label)
	}
	return value, nil
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func hostname() string {
	value, err := os.Hostname()
	if err != nil || strings.TrimSpace(value) == "" {
		return "docker-host"
	}
	return value
}
