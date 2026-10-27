package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/check"
	"etcd-backup-keeper/internal/config"
)

func checkConfigFile(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	path, root := filepath.Join(base, "CONFIG_CANARY.toml"), filepath.Join(base, "ROOT_CANARY")
	input := "endpoints = [\"https://ENDPOINT_CANARY.invalid:2379\"]\nbackup_dir = \"" + root + "\"\nkeep = 1\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	return path, root
}

func TestCheckOfflineCLIJSONAndHuman(t *testing.T) {
	path, root := checkConfigFile(t)
	for _, machine := range []bool{false, true} {
		args := []string{"check", "--config", path}
		if machine {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr, "dev"); code != 0 || stderr.Len() != 0 {
			t.Fatalf("offline result: code=%d %s %s", code, out.String(), stderr.String())
		}
		if strings.Contains(out.String(), "CANARY") || strings.Contains(out.String(), "Ready") {
			t.Fatalf("unsafe/overstated result: %s", out.String())
		}
		if machine {
			var got struct {
				Command string       `json:"command"`
				OK      bool         `json:"ok"`
				Data    check.Result `json:"data"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.OK || got.Command != "check" || got.Data.Connect || len(got.Data.Rows) == 0 {
				t.Fatalf("invalid check JSON: %s %v", out.String(), err)
			}
		} else if !strings.Contains(out.String(), "[unverified] candidate_status candidate=1") || !strings.Contains(out.String(), "Limited read-only") {
			t.Fatalf("missing scope: %s", out.String())
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("CLI created root: %v", err)
	}
}

func TestCheckCLIFlagsAndHelpDoNotLoadOrConnect(t *testing.T) {
	for _, args := range [][]string{
		{"check", "--help"}, {"check", "--connect", "--help", "--config", "/MISSING_CANARY"},
		{"--json", "check", "--connect", "-h"}, {"check", "--connect=false", "--help", "--json"},
	} {
		var out, stderr bytes.Buffer
		code := runWithCheck(context.Background(), args, &out, &stderr, "dev", nil,
			func(context.Context, config.Config, bool) (check.Result, error) {
				t.Fatal("help invoked check")
				return check.Result{}, nil
			})
		if code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "--connect") || !strings.Contains(out.String(), "offline") || strings.Contains(out.String(), "CANARY") {
			t.Fatalf("bad check help: %d %s %s", code, out.String(), stderr.String())
		}
		if requestedJSON(args) && !json.Valid(out.Bytes()) {
			t.Fatalf("invalid help JSON: %s", out.String())
		}
	}
	for _, command := range []string{"backup", "status", "verify", "version"} {
		var out, stderr bytes.Buffer
		code := Run(context.Background(), []string{command, "--connect", "--help", "--json"}, &out, &stderr, "dev")
		if code != 2 || !strings.Contains(out.String(), `"code":"usage"`) {
			t.Fatalf("connect accepted for %s: %d %s", command, code, out.String())
		}
	}
	for _, args := range [][]string{{"check", "--connect=CANARY", "--json"}, {"--connect", "check", "--json"}, {"check", "--verbose", "--json"}, {"check", "--backup", "CANARY", "--json"}} {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr, "dev"); code != 2 || !json.Valid(out.Bytes()) || strings.Contains(out.String()+stderr.String(), "CANARY") {
			t.Fatalf("bad usage handling: %d %s %s", code, out.String(), stderr.String())
		}
	}
	// String values that happen to be flags must not enable JSON.
	if requestedJSON([]string{"check", "--config", "--json", "--connect"}) {
		t.Fatal("config value mistaken for JSON flag")
	}
}

func TestCheckCLIConnectDispatchAndFailureRetainsRows(t *testing.T) {
	path, _ := checkConfigFile(t)
	for _, machine := range []bool{false, true} {
		args := []string{"check", "--config", path, "--connect"}
		if machine {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		code := runWithCheck(context.Background(), args, &out, &stderr, "dev", nil,
			func(ctx context.Context, cfg config.Config, connect bool) (check.Result, error) {
				if !connect || cfg.Keep != 1 {
					t.Fatal("check opts/config not dispatched")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("check has no request deadline")
				}
				return check.Result{Connect: true, Rows: []check.Row{{Label: "candidate_status", State: check.Failed, Message: "Remote candidate Status could not be obtained.", Candidate: 1}}}, apperr.New("remote_unavailable", "candidate Status is unavailable")
			})
		if code != 1 || !strings.Contains(out.String(), "candidate_status") {
			t.Fatalf("lost failure rows: %d %s %s", code, out.String(), stderr.String())
		}
		if machine {
			var got envelope
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.OK || got.Data == nil || got.Error == nil || got.Error.Code != "remote_unavailable" || stderr.Len() != 0 {
				t.Fatalf("wrong failure envelope: %s %v", out.String(), err)
			}
		} else if !strings.Contains(stderr.String(), "error [remote_unavailable]") {
			t.Fatalf("missing human failure: %s", stderr.String())
		}
	}
}

func TestCheckCLIMaterialAndConfigFailureRedaction(t *testing.T) {
	path, _ := checkConfigFile(t)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := io.WriteString(file, "username = \"USER_CANARY\"\npassword_file = \"/MISSING_PASSWORD_CANARY\"\n")
	file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	var out, stderr bytes.Buffer
	code := Run(context.Background(), []string{"check", "--config", path, "--json"}, &out, &stderr, "dev")
	if code != 2 || !strings.Contains(out.String(), `"code":"credentials_invalid"`) || !strings.Contains(out.String(), `"rows"`) || strings.Contains(out.String()+stderr.String(), "CANARY") {
		t.Fatalf("bad credential failure: %d %s %s", code, out.String(), stderr.String())
	}
	fifo := filepath.Join(t.TempDir(), "FIFO_CONFIG_CANARY")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = Run(context.Background(), []string{"check", "--config", fifo, "--json"}, &out, &stderr, "dev")
	if code != 2 || !strings.Contains(out.String(), `"code":"config_read"`) || strings.Contains(out.String(), "CANARY") {
		t.Fatalf("bad config reader: %d %s", code, out.String())
	}
}

func TestCheckHumanFormatterShortWrite(t *testing.T) {
	if err := writeHumanCheck(shortCheckWriter{}, check.Result{}); err != io.ErrShortWrite {
		t.Fatalf("short write ignored: %v", err)
	}
}

type shortCheckWriter struct{}

func (shortCheckWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
