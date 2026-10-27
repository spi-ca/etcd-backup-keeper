package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"etcd-backup-keeper/internal/backup"
)

func TestStatusHumanSummary(t *testing.T) {
	completed := time.Date(2026, 10, 21, 12, 30, 0, 0, time.FixedZone("local", 9*60*60))
	first := backup.Entry{Name: "backup-first", Metadata: backup.Metadata{Bytes: 1024, Revision: 10, CompletedAt: completed.Add(-time.Hour)}}
	latest := backup.Entry{Name: "backup-latest", Metadata: backup.Metadata{Bytes: 512, Revision: 11, CompletedAt: completed}}
	result := backup.StatusResult{
		Initialized: true, Completed: []backup.Entry{first, latest}, LatestSuccess: &latest,
		TotalBytes: 1536, InProgress: []string{"inprogress-one", "inprogress-two"}, Deleting: []string{"deleting-one"},
	}
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeHumanStatus(&out, result, completed.Add(26*time.Hour+3*time.Minute+4*time.Second)); err != nil {
		t.Fatal(err)
	}
	want := "Local backups: 2 completed, 2 inprogress, 1 deleting.\n" +
		"Completed snapshot bytes: 1.5 KiB (1536 bytes).\n" +
		"Latest completed: backup-latest completed=2026-10-21T03:30:00Z (1d 2h3m4s ago).\n" +
		"backup-first revision=10 snapshot=1.0 KiB completed=2026-10-21T02:30:00Z\n" +
		"backup-latest revision=11 snapshot=512 B completed=2026-10-21T03:30:00Z\n" +
		"ATTENTION: incomplete or pending-deletion items require investigation.\n" +
		"  inprogress (incomplete): inprogress-one\n" +
		"  inprogress (incomplete): inprogress-two\n" +
		"  deleting (pending deletion): deleting-one\n" +
		"Integrity not checked by status; run verify.\n"
	if out.String() != want {
		t.Fatalf("status summary:\n%s\nwant:\n%s", out.String(), want)
	}
	after, err := json.Marshal(result)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("human output mutated the inventory: %s (%v)", after, err)
	}
}

func TestStatusHumanEmptyAndFuture(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for _, initialized := range []bool{false, true} {
		var out bytes.Buffer
		if err := writeHumanStatus(&out, backup.StatusResult{Initialized: initialized}, now); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"0 completed, 0 inprogress, 0 deleting", "Completed snapshot bytes: 0 B (0 bytes)", "Latest completed: none", "Integrity not checked by status; run verify"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q: %s", want, out.String())
			}
		}
		if strings.Contains(out.String(), "uninitialized") == initialized || strings.Contains(out.String(), "ATTENTION") {
			t.Fatalf("incorrect empty status: %s", out.String())
		}
	}
	future := backup.Entry{Name: "backup-future", Metadata: backup.Metadata{CompletedAt: now.Add(time.Nanosecond)}}
	var out bytes.Buffer
	if err := writeHumanStatus(&out, backup.StatusResult{Initialized: true, LatestSuccess: &future}, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2026-10-22T00:00:00.000000001Z (future timestamp; check clock skew)") || strings.Contains(out.String(), "ago") {
		t.Fatalf("misleading future age: %s", out.String())
	}
}

func TestHumanBytesIEC(t *testing.T) {
	for _, tc := range []struct {
		size int64
		want string
	}{
		{0, "0 B"}, {1, "1 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"}, {1 << 30, "1.0 GiB"}, {1 << 40, "1.0 TiB"}, {1 << 50, "1.0 PiB"}, {1 << 60, "1.0 EiB"}, {1<<63 - 1, "8.0 EiB"},
	} {
		if got := humanBytes(tc.size); got != tc.want {
			t.Errorf("humanBytes(%d)=%q, want %q", tc.size, got, tc.want)
		}
	}
}

func TestCompletionAgeInjectedNow(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "0s ago"}, {time.Millisecond, "0s ago"}, {time.Second, "1s ago"},
		{time.Hour + time.Minute + time.Second, "1h1m1s ago"}, {48 * time.Hour, "2d 0s ago"},
		{-time.Nanosecond, "future timestamp; check clock skew"}, {-48 * time.Hour, "future timestamp; check clock skew"},
	} {
		if got := completionAge(now.Add(-tc.elapsed), now); got != tc.want {
			t.Errorf("elapsed=%s got=%q want=%q", tc.elapsed, got, tc.want)
		}
	}
}

type statusSink struct {
	err error
}

func (s statusSink) Write([]byte) (int, error) { return 0, s.err }

func TestStatusHumanOutputFailure(t *testing.T) {
	failure := errors.New("sink failed")
	for _, tc := range []struct{ sinkErr, want error }{{failure, failure}, {nil, io.ErrShortWrite}} {
		if err := writeHumanStatus(statusSink{tc.sinkErr}, backup.StatusResult{}, time.Time{}); !errors.Is(err, tc.want) {
			t.Fatalf("output failure=%v, want %v", err, tc.want)
		}
	}
}

func TestStatusCommandOfflineContract(t *testing.T) {
	const suffix = "20201021T000000.000000000Z-0123456789abcdef0123456789abcdef"
	const name = "backup-" + suffix
	const metadata = `{"format_version":1,"tool_version":"test","server_version":"3.7.3","storage_version":"3.7.0","cluster_id":"1","member_id":"2","created_at":"2020-10-21T00:00:00Z","completed_at":"2020-10-21T00:01:00Z","revision":42,"keys":1,"db_bytes":512,"bytes":544,"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}`
	for _, state := range []string{"missing", "empty", "inventory"} {
		t.Run(state, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "backups")
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			mkdir := func(path string) {
				t.Helper()
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(base, "config.toml")
			write(path, []byte(`endpoints = ["https://SECRET_ENDPOINT.invalid:2379"]
backup_dir = "`+root+`"
keep = 1
username = "SECRET_USER"
password_file = "/missing/SECRET_PASSWORD"
ca_file = "/missing/SECRET_CA"
cert_file = "/missing/SECRET_CERT"
key_file = "/missing/SECRET_KEY"
`))
			wantData := `{"initialized":false,"completed":[],"inprogress":[],"deleting":[],"total_bytes":0,"latest_success":null}`
			if state != "missing" {
				mkdir(root)
				write(filepath.Join(root, ".lock"), nil)
				wantData = strings.Replace(wantData, `"initialized":false`, `"initialized":true`, 1)
			}
			if state == "inventory" {
				mkdir(filepath.Join(root, name))
				// Invalid snapshot contents with valid metadata/size demonstrate that
				// status does not perform hashing or snapshot inspection.
				write(filepath.Join(root, name, "snapshot.db"), make([]byte, 544))
				write(filepath.Join(root, name, "metadata.json"), []byte(metadata))
				digest := sha256.Sum256([]byte(metadata))
				write(filepath.Join(root, name, "SHA256SUMS"), []byte(fmt.Sprintf("%s  snapshot.db\n%x  metadata.json\n", strings.Repeat("0", 64), digest)))
				mkdir(filepath.Join(root, "inprogress-"+suffix))
				mkdir(filepath.Join(root, "deleting-"+suffix))
				entry := `{"name":"` + name + `","metadata":` + metadata + `}`
				wantData = `{"initialized":true,"completed":[` + entry + `],"inprogress":["inprogress-` + suffix + `"],"deleting":["deleting-` + suffix + `"],"total_bytes":544,"latest_success":` + entry + `}`
			}
			for _, machine := range []bool{false, true} {
				args := []string{"status", "--config", path}
				if machine {
					args = append(args, "--json")
				}
				var out, stderr bytes.Buffer
				if code := Run(context.Background(), args, &out, &stderr, "test"); code != 0 || stderr.Len() != 0 {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
				}
				if strings.Contains(out.String(), "SECRET_") {
					t.Fatal("configuration leaked")
				}
				if machine {
					want := `{"command":"status","ok":true,"data":` + wantData + "}\n"
					if out.String() != want {
						t.Fatalf("JSON contract changed:\n%s\nwant:\n%s", out.String(), want)
					}
				} else {
					if !strings.Contains(out.String(), "Integrity not checked by status; run verify.") {
						t.Fatalf("missing integrity boundary: %s", out.String())
					}
					if state == "inventory" && (!strings.Contains(out.String(), "1 completed, 1 inprogress, 1 deleting") || !strings.Contains(out.String(), name+" revision=42 snapshot=544 B") || !strings.Contains(out.String(), "ATTENTION:")) {
						t.Fatalf("missing inventory: %s", out.String())
					}
				}
			}
			if state == "missing" {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("status created storage: %v", err)
				}
			}
		})
	}
}

func TestCommandHelpScopeAndOffline(t *testing.T) {
	base := t.TempDir()
	invalidConfig := filepath.Join(base, "invalid.toml")
	if err := os.WriteFile(invalidConfig, []byte("SECRET_INVALID_CONFIG"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		want    []string
		absent  []string
	}{
		{"backup", []string{"--verbose", "default: off", "publication, root sync and retention", "Examples", "124 timeout"}, []string{"--backup NAME"}},
		{"status", []string{"latest completion UTC time and age", "not whole-volume disk usage", "Integrity not checked by status; run verify", "empty storage", "Examples"}, []string{"--verbose", "--backup NAME"}},
		{"verify", []string{"ALL completed backups by default", "--backup NAME", "default: ALL completed backups", "not a restoration guarantee", "Examples"}, []string{"--verbose"}},
		{"version", []string{"not read by version", "Examples", "Exit codes: 0 success"}, []string{"--verbose", "--backup NAME"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			var humanHelp string
			for _, configPath := range []string{invalidConfig, filepath.Join(base, "SECRET_MISSING_CONFIG")} {
				for _, alias := range []string{"--help", "-h"} {
					for _, machine := range []bool{false, true} {
						// Exercise common flags before the command, and command flags after.
						args := []string{"--config", configPath, tc.command, alias}
						if alias == "-h" {
							args = []string{tc.command, "--config", configPath, alias}
						}
						if machine {
							if alias == "-h" {
								args = append(args, "--json")
							} else {
								args = append([]string{"--json"}, args...)
							}
						}
						var out, stderr bytes.Buffer
						if code := Run(context.Background(), args, &out, &stderr, "test"); code != 0 || stderr.Len() != 0 {
							t.Fatalf("help loaded config: %v exit=%d stdout=%s stderr=%s", args, code, out.String(), stderr.String())
						}
						text := out.String()
						if machine {
							var got map[string]json.RawMessage
							if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 3 || string(got["command"]) != `"`+tc.command+`"` || string(got["ok"]) != "true" {
								t.Fatalf("help envelope changed: %s (%v)", out.String(), err)
							}
							var data map[string]string
							if err := json.Unmarshal(got["data"], &data); err != nil || len(data) != 1 || data["usage"] != humanHelp {
								t.Fatalf("help data changed: %s (%v)", got["data"], err)
							}
							text = data["usage"]
						} else {
							humanHelp = text
						}
						for _, want := range append([]string{"Usage:\n  etcd-backup-keeper " + tc.command, "--config PATH", "--json", "--help, -h"}, tc.want...) {
							if !strings.Contains(text, want) {
								t.Errorf("missing %q in %s", want, text)
							}
						}
						for _, absent := range append([]string{"SECRET_", "Commands:", "check --"}, tc.absent...) {
							if strings.Contains(text, absent) {
								t.Errorf("unexpected %q in %s", absent, text)
							}
						}
					}
				}
			}
		})
	}
}

func TestCommandHelpInvalidOptionsRemainRedacted(t *testing.T) {
	for _, args := range [][]string{
		{"backup", "--help", "--password=SECRET_ARG"},
		{"status", "--help", "--verbose=SECRET_ARG"},
		{"verify", "--help", "--backup", "../SECRET_ARG"},
		{"version", "--help", "SECRET_ARG"},
		{"--config", "SECRET_PATH", "status", "--help", "--backup=SECRET_ARG"},
	} {
		for _, machine := range []bool{false, true} {
			actual := append([]string{}, args...)
			if machine {
				actual = append(actual, "--json")
			}
			var out, stderr bytes.Buffer
			if code := Run(context.Background(), actual, &out, &stderr, "test"); code != 2 || strings.Contains(out.String()+stderr.String(), "SECRET_") {
				t.Fatalf("unsafe usage error: %v exit=%d stdout=%s stderr=%s", actual, code, out.String(), stderr.String())
			}
			if machine && (!json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"code":"usage"`) || stderr.Len() != 0) {
				t.Fatalf("JSON usage error changed: %s %s", out.String(), stderr.String())
			}
		}
	}
}

func TestCommandHelpRootAndVersionDoNotLoadConfig(t *testing.T) {
	for _, args := range [][]string{
		{"--config", "/missing/SECRET_CONFIG", "--help"},
		{"--config", "/missing/SECRET_CONFIG", "--help", "--json"},
		{"version", "--config", "/missing/SECRET_CONFIG"},
		{"version", "--config", "/missing/SECRET_CONFIG", "--json"},
	} {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr, "test-version"); code != 0 || stderr.Len() != 0 || strings.Contains(out.String(), "SECRET_") {
			t.Fatalf("offline help/version: %v exit=%d stdout=%s stderr=%s", args, code, out.String(), stderr.String())
		}
		if requestedJSON(args) && !json.Valid(out.Bytes()) {
			t.Fatalf("invalid JSON: %s", out.String())
		}
		if args[0] == "--config" && !requestedJSON(args) && out.String() != usage {
			t.Fatalf("root help incompatible: %s", out.String())
		}
	}
}
