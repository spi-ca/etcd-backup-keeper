package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validTOML = `endpoints = ["https://first.example:2379", "https://second.example:2379"]
backup_dir = "/var/lib/etcd-backup-keeper"
keep = 3
`

func TestParseDefaultsAndOrder(t *testing.T) {
	cfg, err := Parse([]byte(validTOML + "expected_cluster_id = \"0xabcdef\"\nmin_free_bytes = 42\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DialTimeout != DefaultDialTimeout || cfg.RequestTimeout != DefaultRequestTimeout || cfg.MinFreeBytes != 42 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Endpoints[0] != "https://first.example:2379" || cfg.Endpoints[1] != "https://second.example:2379" {
		t.Fatal("candidate ordering changed")
	}
}

func TestStrictParsing(t *testing.T) {
	tests := map[string]string{
		"unknown":           "password = \"SECRET_CANARY\"\n",
		"miscased":          "Keep = 8\n",
		"table":             "[tls]\nca_file = \"SECRET_CANARY\"\n",
		"malformed":         "username = \"SECRET_CANARY\n",
		"wrong_type":        "min_free_bytes = \"SECRET_CANARY\"\n",
		"negative_space":    "min_free_bytes = -1\n",
		"zero_timeout":      "dial_timeout = \"0s\"\n",
		"negative_timeout":  "request_timeout = \"-1s\"\n",
		"overflow_timeout":  "dial_timeout = \"999999999999999999999h\"\n",
		"integer_timeout":   "request_timeout = 4\n",
		"bad_timeout":       "request_timeout = \"SECRET_CANARY\"\n",
		"empty_timeout":     "dial_timeout = \"\"\n",
		"bad_cluster":       "expected_cluster_id = \"SECRET_CANARY\"\n",
		"zero_cluster":      "expected_cluster_id = \"0\"\n",
		"overflow_cluster":  "expected_cluster_id = \"10000000000000000\"\n",
		"cert_without_key":  "cert_file = \"/abs/client.crt\"\n",
		"key_without_cert":  "key_file = \"/abs/client.key\"\n",
		"relative_ca":       "ca_file = \"relative.crt\"\n",
		"relative_password": "username = \"u\"\npassword_file = \"relative\"\n",
		"username_only":     "username = \"SECRET_CANARY\"\n",
		"password_only":     "password_file = \"/SECRET_CANARY\"\n",
		"tls_skip":          "insecure_skip_verify = true\n",
		"quoted_dot":        "\"tls.ca_file\" = \"SECRET_CANARY\"\n",
	}
	for name, suffix := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(validTOML + suffix))
			if err == nil {
				t.Fatal("accepted invalid configuration")
			}
			if strings.Contains(err.Error(), "SECRET_CANARY") {
				t.Fatal("diagnostic leaked a value")
			}
		})
	}
	for _, input := range []string{
		strings.ReplaceAll(validTOML, "keep = 3", "Keep = 3"),
		strings.ReplaceAll(validTOML, "keep = 3", "keep = 0"),
		strings.ReplaceAll(validTOML, "keep = 3", "keep = -1"),
		strings.ReplaceAll(validTOML, "keep = 3", ""),
		strings.ReplaceAll(validTOML, "\"/var/lib/etcd-backup-keeper\"", "\"relative\""),
		"keep = 1\nbackup_dir = \"/tmp\"\nendpoints = []\n",
	} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Fatalf("accepted invalid document: %s", input)
		}
	}
}

func TestEndpointValidation(t *testing.T) {
	bad := []string{
		"http://SECRET_CANARY:password@example:2379", "https://example:2379?SECRET_CANARY",
		"https://example:2379?", "https://example:2379#SECRET_CANARY", "https://example:2379#",
		"https://example:2379/path", "https://example:2379/%2f", "ftp://example:2379",
		"https:///", "https://example:0", "https://example:65536", "https://example:",
		"https://example:abc", "https://bad host:2379", "unix:///tmp/etcd.sock",
	}
	for _, endpoint := range bad {
		t.Run(endpoint, func(t *testing.T) {
			cfg, _ := Parse([]byte(validTOML))
			cfg.Endpoints = []string{endpoint}
			err := cfg.Validate()
			if err == nil || strings.Contains(err.Error(), "SECRET_CANARY") {
				t.Fatalf("unsafe validation result: %v", err)
			}
		})
	}
	cfg, _ := Parse([]byte(validTOML))
	cfg.Endpoints = []string{"https://first:2379", "http://second:2379"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted mixed transports")
	}
	cfg.Endpoints = []string{"http://127.0.0.1:2379"}
	if err := cfg.Validate(); err != nil {
		t.Fatal("unauthenticated HTTP should be allowed", err)
	}
	cfg.Username, cfg.PasswordFile = "user", "/private/password"
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted credentials over HTTP")
	}
	cfg.Username, cfg.PasswordFile, cfg.CAFile = "", "", "/ca.crt"
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted TLS configuration over HTTP")
	}
}

func TestContexts(t *testing.T) {
	cfg, _ := Parse([]byte(validTOML))
	ctx, cancel := cfg.RequestContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > DefaultRequestTimeout || time.Until(deadline) < DefaultRequestTimeout-time.Second {
		t.Fatal("request deadline not applied")
	}
	probe, stop := cfg.DialContext(ctx)
	defer stop()
	deadline, _ = probe.Deadline()
	if time.Until(deadline) > DefaultDialTimeout || time.Until(deadline) < DefaultDialTimeout-time.Second {
		t.Fatal("probe deadline not applied")
	}
	parent, cancelParent := context.WithCancel(context.Background())
	child, stopChild := cfg.RequestContext(parent)
	defer stopChild()
	cancelParent()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatal("parent cancellation not propagated")
	}
	expired, stopExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stopExpired()
	child, stop = cfg.DialContext(expired)
	defer stop()
	if !errors.Is(child.Err(), context.DeadlineExceeded) {
		t.Fatal("parent deadline was extended")
	}
}

func TestLoadAndExample(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "SECRET_CANARY")); err == nil || strings.Contains(err.Error(), "SECRET_CANARY") {
		t.Fatalf("unsafe missing file error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "oversized")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxConfigBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted oversized configuration")
	}
	if _, err := Load("../../config.example.toml"); err != nil {
		t.Fatal(err)
	}
}
