package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/backup"
	"etcd-backup-keeper/internal/config"
)

func TestRecoveryHintsHumanOnlyAndExitCodes(t *testing.T) {
	cases := []struct {
		code string
		exit int
		hint string
	}{
		{"usage", 2, "Use --help to check supported commands and options."},
		{"config_read", 2, "Check that the intended configuration file exists and is readable by the execution user."},
		{"config_invalid", 2, "Review the configuration against config.example.toml and the configuration contract in docs/operations.md."},
		{"credentials_invalid", 2, "Check credential provisioning, ownership and private-file requirements for the execution user."},
		{"tls_invalid", 2, "Check CA trust, certificate validity and the client certificate/key pair; keep TLS verification enabled."},
		{"storage_locked", 1, "Wait for the active operation to finish; do not remove or replace .lock."},
		{"storage_unsafe", 1, "Check backup storage ownership and private-file requirements; preserve existing files for investigation."},
		{"storage_unsupported", 1, "Check snapshot storage-version compatibility; preserve the snapshot and existing backups for investigation."},
		{"no_backups", 1, "Inspect status and confirm the intended backup storage; verification requires a completed local backup."},
		{"backup_not_found", 1, "Use status to find the exact completed backup name for --backup."},
		{"snapshot_invalid", 1, "Preserve the affected files and investigate the verification failure; status alone does not establish snapshot integrity."},
		{"metadata_invalid", 1, "Preserve the affected files and investigate the verification failure; status alone does not establish snapshot integrity."},
		{"space_insufficient", 1, "Check free bytes and inodes on the backup volume, including incomplete and pending-deletion items; preserve existing backups."},
		{"remote_unavailable", 1, "Check endpoint reachability, server health and TLS/authentication provisioning before another attempt."},
		{"remote_invalid", 1, "Check endpoint reachability, server health and TLS/authentication provisioning before another attempt."},
		{"server_unsupported", 1, "Check server compatibility; only etcd 3.7 servers are supported."},
		{"cluster_mismatch", 1, "Check the intended cluster and configured cluster pin; preserve existing backups and staging."},
		{"backup_incomplete", 1, "Inspect status and backup-volume health; preserve incomplete staging for investigation."},
		{"durability_uncertain", 1, "Investigate backup-volume sync failures; status cannot establish durability or snapshot integrity."},
		{"retention_incomplete", 1, "Inspect status and pending deletion items; investigate cleanup failures before scheduling another backup."},
		{"timeout", 124, "Inspect status and preserved staging, then review operation duration and timeout limits before another attempt."},
		{"canceled", 130, "Confirm the operation has exited, then inspect status and preserved staging before deciding next steps."},
		// Unknown codes do not gain a hint by matching words in their message.
		{"future_code", 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			safe := apperr.New(tc.code, "static safe diagnostic: timeout retention_incomplete")
			err := fmt.Errorf("SECRET_RPC_FILESYSTEM_CANARY: %w", safe)
			for _, machine := range []bool{false, true} {
				var out, stderr bytes.Buffer
				if code := failure(options{command: "backup", json: machine}, err, &out, &stderr); code != tc.exit {
					t.Fatalf("json=%v exit=%d want=%d", machine, code, tc.exit)
				}
				if machine {
					want, marshalErr := json.Marshal(envelope{Command: "backup", OK: false, Error: safe})
					if marshalErr != nil || out.String() != string(want)+"\n" || stderr.Len() != 0 {
						t.Fatalf("JSON contract changed: stdout=%q stderr=%q (%v)", out.String(), stderr.String(), marshalErr)
					}
				} else {
					want := "error [" + tc.code + "]: " + safe.Message + "\n"
					if tc.hint != "" {
						want += "hint: " + tc.hint + "\n"
					}
					if out.Len() != 0 || stderr.String() != want {
						t.Fatalf("human recovery output: stdout=%q stderr=%q want=%q", out.String(), stderr.String(), want)
					}
				}
			}
		})
	}
	for _, machine := range []bool{false, true} {
		var out, stderr bytes.Buffer
		code := failure(options{json: machine}, errors.New("SECRET_RAW_ERROR_CANARY tls_invalid"), &out, &stderr)
		all := out.String() + stderr.String()
		if code != 1 || strings.Contains(all, "SECRET_") || strings.Contains(all, "hint:") || !strings.Contains(all, "internal_error") {
			t.Fatalf("raw error influenced recovery: code=%d output=%s", code, all)
		}
	}
}

func TestRecoveryInputRedaction(t *testing.T) {
	base := t.TempDir()
	invalid := filepath.Join(base, "SECRET_CONFIG_PATH_CANARY.toml")
	malformed := filepath.Join(base, "SECRET_PARSE_PATH_CANARY.toml")
	if err := os.WriteFile(invalid, []byte(`SECRET_UNKNOWN_KEY_CANARY = "SECRET_PASSWORD_CANARY"`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(malformed, []byte(`endpoints = ["SECRET_CONFIG_CONTENT_CANARY"`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"SECRET_COMMAND_CANARY"}, "usage"},
		{[]string{"backup", "--password=SECRET_ARG_CANARY"}, "usage"},
		{[]string{"backup", "--config", filepath.Join(base, "SECRET_MISSING_CANARY")}, "config_read"},
		{[]string{"backup", "--config", invalid}, "config_invalid"},
		{[]string{"backup", "--config", malformed}, "config_invalid"},
	} {
		for _, machine := range []bool{false, true} {
			args := append([]string{}, tc.args...)
			if machine {
				args = append(args, "--json")
			}
			var out, stderr bytes.Buffer
			engine := func(context.Context, config.Config, string, func(backup.Progress)) (backup.Result, error) {
				t.Fatal("invalid input reached the backup engine")
				return backup.Result{}, nil
			}
			code := run(context.Background(), args, &out, &stderr, "test", engine)
			all := out.String() + stderr.String()
			if code != 2 || strings.Contains(all, "SECRET_") || !strings.Contains(all, tc.code) {
				t.Fatalf("input leaked/wrong error: exit=%d output=%s", code, all)
			}
			if machine {
				if !json.Valid(out.Bytes()) || stderr.Len() != 0 || strings.Contains(all, "hint:") {
					t.Fatalf("JSON recovery output changed: stdout=%q stderr=%q", out.String(), stderr.String())
				}
			} else if out.Len() != 0 || !strings.Contains(stderr.String(), "hint: ") {
				t.Fatalf("missing human recovery hint: stdout=%q stderr=%q", out.String(), stderr.String())
			}
		}
	}
}

func TestRecoveryPublishedFailureHeadingsAndJSON(t *testing.T) {
	path := progressConfig(t)
	for _, tc := range []struct {
		name    string
		err     error
		exit    int
		summary string
	}{
		{"durability", apperr.New("durability_uncertain", "completed backup preserved but root sync failed; durability is uncertain and retention was not run; run status"), 1, "root durability is uncertain; retention was not run. Inspect status and storage health."},
		{"retention", apperr.New("retention_incomplete", "new completed backup is published; retention cleanup is pending; run status and retry with a successful backup"), 1, "retention cleanup is incomplete. Inspect status before further action."},
		{"canceled", context.Canceled, 130, "operation incomplete. Inspect status before further action."},
		{"timeout", context.DeadlineExceeded, 124, "operation incomplete. Inspect status before further action."},
		{"canceled_retention", apperr.New("canceled", "new completed backup is published; retention cleanup is pending; run status and retry with a successful backup"), 130, "operation incomplete. Inspect status before further action."},
		{"timeout_after_publication", apperr.New("timeout", "operation ended after publication; completed backup preserved and retention was not run; run status"), 124, "operation incomplete. Inspect status before further action."},
		{"unknown", errors.New("SECRET_ENGINE_CANARY retention_incomplete"), 1, "operation incomplete. Inspect status before further action."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, named := range []bool{false, true} {
				// Neither pending state alone nor the error message selects a heading.
				for _, pending := range []bool{false, true} {
					for _, machine := range []bool{false, true} {
						result := backup.Result{RetentionPending: pending, Removed: []string{}, Pending: []string{}}
						if named {
							result.Name = "backup-test"
						}
						engine := func(context.Context, config.Config, string, func(backup.Progress)) (backup.Result, error) {
							return result, tc.err
						}
						args := []string{"backup", "--config", path}
						if machine {
							args = append(args, "--json")
						}
						var out, stderr bytes.Buffer
						if code := run(context.Background(), args, &out, &stderr, "test", engine); code != tc.exit {
							t.Fatalf("exit=%d want=%d", code, tc.exit)
						}
						safe := apperr.Safe(tc.err)
						if machine {
							var data any
							if named {
								data = result
							}
							want, err := json.Marshal(envelope{Command: "backup", OK: false, Data: data, Error: safe})
							if err != nil || out.String() != string(want)+"\n" || stderr.Len() != 0 {
								t.Fatalf("JSON failure contract changed: stdout=%q stderr=%q (%v)", out.String(), stderr.String(), err)
							}
						} else {
							want := ""
							if named {
								want = "Published backup backup-test; " + tc.summary + "\n"
							}
							want += "error [" + safe.Code + "]: " + safe.Message + "\n"
							if hint := recoveryHint(safe.Code); hint != "" {
								want += "hint: " + hint + "\n"
							}
							if out.Len() != 0 || stderr.String() != want {
								t.Fatalf("incorrect publication heading: stdout=%q stderr=%q want=%q", out.String(), stderr.String(), want)
							}
						}
					}
				}
			}
		})
	}
}

func TestRecoveryHintSinkFailureAndEnginePriority(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, short := range []bool{false, true} {
			var out bytes.Buffer
			stderr := &failingProgressWriter{failAt: 2, short: short}
			// The error line succeeds; failure to write the new hint is an output error.
			if code := failure(options{}, err, &out, stderr); code != 1 || stderr.calls != 2 {
				t.Fatalf("hint sink failure ignored: exit=%d calls=%d", code, stderr.calls)
			}
		}
	}
	path := progressConfig(t)
	for _, tc := range []struct {
		err  error
		exit int
	}{{context.Canceled, 130}, {context.DeadlineExceeded, 124}, {apperr.New("tls_invalid", "cannot load trusted CA certificates"), 2}} {
		for _, short := range []bool{false, true} {
			var out bytes.Buffer
			stderr := &failingProgressWriter{failAt: 1, short: short}
			finished := false
			engine := func(context.Context, config.Config, string, func(backup.Progress)) (backup.Result, error) {
				finished = true
				return backup.Result{Name: "backup-test", RetentionPending: true}, tc.err
			}
			code := run(context.Background(), []string{"backup", "--config", path, "--verbose"}, &out, stderr, "test", engine)
			if !finished || code != tc.exit || out.Len() != 0 {
				t.Fatalf("progress failure hid engine error: finished=%v exit=%d want=%d stdout=%q", finished, code, tc.exit, out.String())
			}
		}
	}
}
