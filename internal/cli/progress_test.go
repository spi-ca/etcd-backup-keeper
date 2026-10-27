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

	"etcd-backup-keeper/internal/backup"
	"etcd-backup-keeper/internal/config"
)

func progressConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	input := `endpoints = ["https://SECRET_ENDPOINT_CANARY.invalid:2379"]
backup_dir = "` + filepath.Join(t.TempDir(), "SECRET_PATH_CANARY") + `"
keep = 1
username = "SECRET_USER_CANARY"
password_file = "/missing/SECRET_PASSWORD_CANARY"
`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupProgressStderrOnlyAndNonverboseSilence(t *testing.T) {
	path := progressConfig(t)
	for _, verbose := range []bool{false, true} {
		for _, machine := range []bool{false, true} {
			args := []string{"backup", "--config", path}
			if verbose {
				args = append(args, "--verbose")
			}
			if machine {
				args = append(args, "--json")
			}
			var out, stderr bytes.Buffer
			result := backup.Result{Name: "backup-test", Metadata: backup.Metadata{Bytes: 123, Revision: 2}, Removed: []string{}, Pending: []string{}}
			engine := func(_ context.Context, _ config.Config, _ string, observe func(backup.Progress)) (backup.Result, error) {
				if (observe != nil) != verbose {
					t.Fatalf("observer enabled=%v, verbose=%v", observe != nil, verbose)
				}
				if observe != nil {
					for _, event := range []backup.Progress{
						{Stage: backup.ProbeCandidate, Candidate: 1, Total: 2},
						{Stage: backup.CandidateUnavailable, Candidate: 1, Total: 2},
						{Stage: backup.AttemptSnapshot, Candidate: 2, Total: 2},
						{Stage: backup.AttemptFailed, Candidate: 2, Total: 2},
						{Stage: backup.Failover, Candidate: 1, Total: 2},
						{Stage: backup.Receiving, Bytes: 12, Elapsed: time.Second},
						{Stage: backup.Received, Bytes: 123, Elapsed: 1500 * time.Millisecond},
						{Stage: backup.ChecksumSnapshot}, {Stage: backup.InspectSnapshot},
						{Stage: backup.PublishSnapshot}, {Stage: backup.SyncRoot},
						{Stage: backup.Retention}, {Stage: backup.Complete},
					} {
						observe(event)
					}
				}
				return result, nil
			}
			if code := run(context.Background(), args, &out, &stderr, "test", engine); code != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
			}
			if strings.Contains(out.String()+stderr.String(), "SECRET_") || strings.Contains(out.String(), "stage=") {
				t.Fatalf("unsafe/mixed output: %s %s", out.String(), stderr.String())
			}
			if !verbose && stderr.Len() != 0 {
				t.Fatalf("nonverbose progress: %s", stderr.String())
			}
			if verbose {
				for _, want := range []string{"stage=config_validated", "stage=candidate_probe candidate=1/2", "stage=candidate_unavailable", "stage=snapshot_attempt candidate=2/2", "stage=attempt_failed", "stage=failover", "stage=snapshot_receiving bytes=12 elapsed=1s", "bytes=123 final=true elapsed=1.5s", "stage=checksum", "stage=inspection", "stage=publication", "stage=root_sync", "stage=retention", "stage=complete"} {
					if !strings.Contains(stderr.String(), want) {
						t.Fatalf("missing %q in %s", want, stderr.String())
					}
				}
			}
			if machine {
				want, err := json.Marshal(envelope{Command: "backup", OK: true, Data: result})
				if err != nil || !bytes.Equal(bytes.TrimSpace(out.Bytes()), want) {
					t.Fatalf("JSON schema/data changed: %s (%v)", out.String(), err)
				}
			}
		}
	}
}

type failingProgressWriter struct {
	calls  int
	failAt int
	short  bool
}

func (w *failingProgressWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAt {
		if w.short {
			return 0, nil
		}
		return 0, errors.New("SECRET_SINK_CANARY")
	}
	return len(p), nil
}

func TestProgressOutputFailuresDoNotInterruptEngine(t *testing.T) {
	path := progressConfig(t)
	for _, failAt := range []int{1, 2, 3} {
		for _, short := range []bool{false, true} {
			for _, engineErr := range []error{nil, context.Canceled, context.DeadlineExceeded, errors.New("SECRET_ENGINE_CANARY")} {
				var out bytes.Buffer
				stderr := &failingProgressWriter{failAt: failAt, short: short}
				finished := false
				engine := func(_ context.Context, _ config.Config, _ string, observe func(backup.Progress)) (backup.Result, error) {
					observe(backup.Progress{Stage: backup.PublishSnapshot})
					observe(backup.Progress{Stage: backup.Retention})
					finished = true
					if engineErr == nil {
						observe(backup.Progress{Stage: backup.Complete})
					}
					return backup.Result{}, engineErr
				}
				code := run(context.Background(), []string{"backup", "--config", path, "--verbose", "--json"}, &out, stderr, "test", engine)
				wantCode := 1
				if engineErr != nil {
					wantCode = failureExitCode(engineErr)
				}
				if !finished || code != wantCode || stderr.calls != failAt {
					t.Fatalf("interrupted engine/repeated writes: finished=%v code=%d want=%d calls=%d failAt=%d", finished, code, wantCode, stderr.calls, failAt)
				}
				var result envelope
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.OK != (engineErr == nil) || strings.Contains(out.String(), "SECRET_") {
					t.Fatalf("engine error lost/unsafe JSON: %s (%v)", out.String(), err)
				}
			}
		}
	}
}

func TestProgressFailurePreservesPublishedErrorResult(t *testing.T) {
	path := progressConfig(t)
	for _, machine := range []bool{false, true} {
		var out bytes.Buffer
		stderr := &failingProgressWriter{failAt: 2}
		args := []string{"backup", "--config", path, "--verbose"}
		if machine {
			args = append(args, "--json")
		}
		engine := func(_ context.Context, _ config.Config, _ string, observe func(backup.Progress)) (backup.Result, error) {
			observe(backup.Progress{Stage: backup.PublishSnapshot})
			observe(backup.Progress{Stage: backup.SyncRoot})
			return backup.Result{Name: "backup-test", RetentionPending: true}, context.Canceled
		}
		code := run(context.Background(), args, &out, stderr, "test", engine)
		if code != 130 {
			t.Fatalf("published cancellation hidden by progress error: %d", code)
		}
		if machine {
			if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"retention_pending":true`) || !strings.Contains(out.String(), `"code":"canceled"`) || !strings.Contains(out.String(), `"name":"backup-test"`) {
				t.Fatalf("published result lost: %s", out.String())
			}
		} else if out.Len() != 0 {
			t.Fatalf("human failure polluted stdout: %s", out.String())
		}
	}
}

func TestProgressUnknownStageAndFailureRedaction(t *testing.T) {
	var stderr bytes.Buffer
	p := progressOutput{writer: &stderr}
	p.observe(backup.Progress{Stage: backup.Stage(255)})
	if stderr.Len() != 0 {
		t.Fatal("unknown stage rendered")
	}
	path := progressConfig(t)
	var out bytes.Buffer
	engine := func(_ context.Context, _ config.Config, _ string, observe func(backup.Progress)) (backup.Result, error) {
		observe(backup.Progress{Stage: backup.AttemptSnapshot, Candidate: 1, Total: 1})
		observe(backup.Progress{Stage: backup.AttemptFailed, Candidate: 1, Total: 1})
		return backup.Result{}, errors.New("SECRET_REMOTE_ERROR_CANARY https://SECRET_URL_CANARY")
	}
	code := run(context.Background(), []string{"backup", "--config", path, "--verbose", "--json"}, &out, &stderr, "test", engine)
	if code != 1 || !json.Valid(out.Bytes()) || strings.Contains(out.String()+stderr.String(), "SECRET_") || strings.Contains(stderr.String(), "stage=complete") {
		t.Fatalf("false success/leak: code=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
}
