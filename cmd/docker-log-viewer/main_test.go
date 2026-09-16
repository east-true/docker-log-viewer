package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOptionalToken(t *testing.T) {
	t.Setenv("DOCKER_LOG_VIEWER_TEST_TOKEN", strings.Repeat("a", 32))
	value, err := loadOptionalToken("", "DOCKER_LOG_VIEWER_TEST_TOKEN", "test")
	if err != nil || len(value) != 32 {
		t.Fatalf("value length = %d, err = %v", len(value), err)
	}

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err = loadOptionalToken(path, "DOCKER_LOG_VIEWER_TEST_TOKEN", "test")
	if err != nil || value != strings.Repeat("b", 32) {
		t.Fatalf("file value = %q, err = %v", value, err)
	}
}

func TestLoadOptionalTokenRejectsWeakValue(t *testing.T) {
	t.Setenv("DOCKER_LOG_VIEWER_TEST_TOKEN", "short")
	if _, err := loadOptionalToken("", "DOCKER_LOG_VIEWER_TEST_TOKEN", "test"); err == nil {
		t.Fatal("expected short token to be rejected")
	}
}

func TestLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		if !isLoopbackAddress(address) {
			t.Fatalf("expected loopback: %s", address)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080", "192.0.2.1:8080"} {
		if isLoopbackAddress(address) {
			t.Fatalf("expected non-loopback: %s", address)
		}
	}
}
