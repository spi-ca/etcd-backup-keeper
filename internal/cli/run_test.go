package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionAndHelp(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"version", "--json"}, {"--json", "version"}, {"--help"}, {"backup", "--help"}, {"--json", "--help"}} {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr, "test-version"); code != 0 || stderr.Len() != 0 || out.Len() == 0 {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, out.String(), stderr.String())
		}
		if requestedJSON(args) && !json.Valid(out.Bytes()) {
			t.Fatalf("invalid JSON: %q", out.String())
		}
	}
}

func TestLocalCommandsOfflineMissingStorage(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.toml")
	root := filepath.Join(base, "absent", "backups")
	// Missing credentials and unreachable endpoints must never be accessed.
	input := `endpoints = ["https://unreachable.invalid:2379"]
backup_dir = "` + root + `"
keep = 1
username = "test"
password_file = "/nonexistent/SECRET_CANARY"
ca_file = "/nonexistent/SECRET_CANARY_CA"
`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "verify"} {
		for _, machine := range []bool{false, true} {
			args := []string{command, "--config", path}
			if machine {
				args = append(args, "--json")
			}
			var out, stderr bytes.Buffer
			want := 0
			if command == "verify" {
				want = 1
			}
			if code := Run(context.Background(), args, &out, &stderr, "dev"); code != want {
				t.Fatalf("%v: exit=%d output=%s %s", args, code, out.String(), stderr.String())
			}
			all := out.String() + stderr.String()
			if strings.Contains(all, "SECRET_CANARY") || strings.Contains(all, "unreachable") {
				t.Fatalf("unsafe offline result: %s", all)
			}
			if command == "verify" && !strings.Contains(all, "no_backups") {
				t.Fatalf("missing no-backup error: %s", all)
			}
			if machine {
				var result envelope
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.OK != (command == "status") {
					t.Fatalf("invalid envelope: %q (%v)", out.String(), err)
				}
			} else if command == "status" && !strings.Contains(all, "uninitialized") {
				t.Fatalf("unclear missing-root status: %s", all)
			}
		}
	}
	if _, err := os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
		t.Fatalf("local command created storage: %v", err)
	}
}

func TestUsageAndErrorsNeverEchoArguments(t *testing.T) {
	cases := [][]string{
		{}, {"SECRET_CANARY", "--json"}, {"backup", "--password=SECRET_CANARY", "--json"},
		{"backup", "--verbose=SECRET_CANARY", "--json"}, {"status", "--verbose", "--json"},
		{"verify", "--backup", "../SECRET_CANARY", "--json"},
		{"backup", "--config", "/missing/SECRET_CANARY", "--json"},
		{"version", "SECRET_CANARY", "--json"},
	}
	for _, args := range cases {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr, "dev"); code != 2 {
			t.Fatalf("%v: exit=%d", args, code)
		}
		if strings.Contains(out.String()+stderr.String(), "SECRET_CANARY") {
			t.Fatal("argument leaked in diagnostic")
		}
		if requestedJSON(args) && !json.Valid(out.Bytes()) {
			t.Fatalf("invalid JSON error: %q", out.String())
		}
	}
	var out, stderr bytes.Buffer
	code := failure(options{json: true}, errors.New("SECRET_CANARY"), &out, &stderr)
	if code != 1 || strings.Contains(out.String(), "SECRET_CANARY") || !strings.Contains(out.String(), "internal_error") {
		t.Fatal("raw error escaped safe boundary")
	}
}

func TestCanceledAndExpiredContexts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		ctx  context.Context
		code int
		kind string
	}{{ctx, 130, "canceled"}, {expired, 124, "timeout"}} {
		var out, stderr bytes.Buffer
		code := Run(tc.ctx, []string{"backup", "--json", "--config", "/must-not-read"}, &out, &stderr, "dev")
		if code != tc.code || !strings.Contains(out.String(), tc.kind) {
			t.Fatalf("cancellation ignored: code=%d output=%s", code, out.String())
		}
	}
}

func TestBackupDispatchesToEngineWithoutLeakingCredentials(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.toml")
	input := `endpoints = ["https://unreachable.invalid:2379"]
backup_dir = "BACKUP_ROOT"
keep = 1
ca_file = "/nonexistent/SECRET_CANARY_CA"
`
	input = strings.ReplaceAll(input, "BACKUP_ROOT", filepath.Join(base, "backups"))
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Run(context.Background(), []string{"backup", "--config", path, "--json", "--verbose"}, &out, &stderr, "test")
	if code != 2 || !strings.Contains(out.String(), "tls_invalid") || strings.Contains(out.String()+stderr.String(), "CANARY") || strings.Contains(out.String(), "not_implemented") {
		t.Fatalf("wrong dispatch: %d %s %s", code, out.String(), stderr.String())
	}
	if strings.Contains(usage, "not implemented") || !strings.Contains(usage, "ALL completed") {
		t.Fatal("help does not describe local command scope")
	}
}
