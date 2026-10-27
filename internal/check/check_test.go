package check

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Endpoints: []string{"https://ENDPOINT_CANARY.invalid:2379"},
		BackupDir: filepath.Join(t.TempDir(), "root"), Keep: 1,
		DialTimeout: time.Minute, RequestTimeout: 2 * time.Minute,
	}
}

func rowFor(t *testing.T, result Result, label string, candidate int) Row {
	t.Helper()
	for _, row := range result.Rows {
		if row.Label == label && row.Candidate == candidate {
			return row
		}
	}
	t.Fatalf("missing row %s/%d: %+v", label, candidate, result)
	return Row{}
}

func assertRedacted(t *testing.T, result Result, err error) {
	t.Helper()
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	text := string(data)
	if err != nil {
		text += apperr.Safe(err).Message
	}
	if strings.Contains(text, "CANARY") || strings.Contains(text, "/tmp/") {
		t.Fatalf("unsafe output: %s", text)
	}
}

func TestOfflineNeverCreatesClientOrStorage(t *testing.T) {
	cfg := testConfig(t)
	open := func(context.Context, context.Context, config.Config, int) (connection, error) {
		t.Fatal("offline check constructed a client")
		return nil, nil
	}
	result, err := run(context.Background(), cfg, false, open)
	if err != nil || rowFor(t, result, "storage_path", 0).State != Unverified {
		t.Fatalf("missing final root: %+v %v", result, err)
	}
	if _, err := os.Stat(cfg.BackupDir); !os.IsNotExist(err) {
		t.Fatalf("storage created: %v", err)
	}
	if err := os.Mkdir(cfg.BackupDir, 0700); err != nil {
		t.Fatal(err)
	}
	result, err = run(context.Background(), cfg, false, open)
	if err != nil || rowFor(t, result, "storage_path", 0).State != Confirmed {
		t.Fatalf("empty root should be valid: %+v %v", result, err)
	}
	entries, err := os.ReadDir(cfg.BackupDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("created entries: %v %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(cfg.BackupDir, "untouched"), []byte("CONTENT_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = run(context.Background(), cfg, false, open)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.BackupDir, "untouched"))
	if err != nil || string(data) != "CONTENT_CANARY" {
		t.Fatalf("content mutated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, ".lock")); !os.IsNotExist(err) {
		t.Fatalf("lock created: %v", err)
	}
	for _, label := range []string{"candidate_status", "backup_operations", "snapshot_permissions", "previous_cluster_pin", "snapshot_integrity"} {
		candidate := 0
		if label == "candidate_status" {
			candidate = 1
		}
		if rowFor(t, result, label, candidate).State != Unverified {
			t.Fatalf("overstated assurance: %+v", result)
		}
	}
	assertRedacted(t, result, nil)
}

func TestMaterialReadersAreBoundedAndRedacted(t *testing.T) {
	base := t.TempDir()
	password := filepath.Join(base, "PASSWORD_CANARY")
	if err := os.WriteFile(password, []byte("PASSWORD_CONTENT_CANARY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "SYMLINK_CANARY")
	if err := os.Symlink(password, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "FIFO_CANARY")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(base, "PUBLIC_CANARY")
	if err := os.WriteFile(public, []byte("CONTENT_CANARY"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0644); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(base, "LARGE_CANARY")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", 64*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ path, code string }{
		{password, ""}, {link, "credentials_invalid"}, {fifo, "credentials_invalid"},
		{public, "credentials_invalid"}, {large, "credentials_invalid"},
		{filepath.Join(base, "MISSING_CANARY"), "credentials_invalid"},
	}
	for _, tc := range cases {
		t.Run(filepath.Base(tc.path), func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Username, cfg.PasswordFile = "USER_CANARY", tc.path
			result, err := run(context.Background(), cfg, true, func(context.Context, context.Context, config.Config, int) (connection, error) {
				if tc.code != "" {
					t.Fatal("client created despite invalid material")
				}
				return &fakeConnection{info: validStatus()}, nil
			})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || apperr.Safe(err).Code != tc.code || rowFor(t, result, "client_material", 0).State != Failed {
				t.Fatalf("wrong material failure: %+v %v", result, err)
			}
			assertRedacted(t, result, err)
		})
	}
	for _, path := range []string{fifo, public} {
		cfg := testConfig(t)
		cfg.CAFile = path // FIFO or invalid PEM: no real certificate fixtures.
		result, err := Run(context.Background(), cfg, false)
		if err == nil || apperr.Safe(err).Code != "tls_invalid" {
			t.Fatalf("bad TLS material accepted: %+v %v", result, err)
		}
		assertRedacted(t, result, err)
	}
}

type fakeConnection struct {
	info     status
	err      error
	closed   bool
	onStatus func(context.Context)
}

func (f *fakeConnection) Status(ctx context.Context) (status, error) {
	if f.onStatus != nil {
		f.onStatus(ctx)
	}
	return f.info, f.err
}
func (f *fakeConnection) Close() { f.closed = true }
func validStatus() status        { return status{header: true, clusterID: 1, memberID: 2, version: "3.7.3"} }

func TestConnectStatusValidationAndCoherence(t *testing.T) {
	valid := validStatus()
	badHeader, zeroCluster, zeroMember, otherCluster, oldVersion := valid, valid, valid, valid, valid
	badHeader.header = false
	zeroCluster.clusterID = 0
	zeroMember.memberID = 0
	otherCluster.clusterID = 5
	oldVersion.version = "3.6.9"
	for _, tc := range []struct {
		name, pin, code string
		infos           []status
	}{
		{"coherent", "0x1", "", []status{valid, valid}},
		{"header", "", "remote_invalid", []status{badHeader}},
		{"zero-cluster", "", "remote_invalid", []status{zeroCluster}},
		{"zero-member", "", "remote_invalid", []status{zeroMember}},
		{"version", "", "server_unsupported", []status{oldVersion}},
		{"pin", "2", "cluster_mismatch", []status{valid}},
		{"coherence", "", "cluster_mismatch", []status{valid, otherCluster}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.ExpectedClusterID = tc.pin
			if len(tc.infos) > 1 {
				cfg.Endpoints = append(cfg.Endpoints, "https://SECOND_CANARY.invalid:2379")
			}
			var clients []*fakeConnection
			var dialContexts []context.Context
			var lifetime context.Context
			result, err := run(context.Background(), cfg, true, func(ctx, dial context.Context, got config.Config, index int) (connection, error) {
				lifetime = ctx
				dialContexts = append(dialContexts, dial)
				deadline, ok := dial.Deadline()
				requestDeadline, requestOK := ctx.Deadline()
				if !ok || !requestOK || !deadline.Before(requestDeadline) || got.Endpoints[index] != cfg.Endpoints[index] {
					t.Fatal("missing bounded candidate/lifetime context")
				}
				f := &fakeConnection{info: tc.infos[index]}
				clients = append(clients, f)
				return f, nil
			})
			if (tc.code == "" && err != nil) || (tc.code != "" && (err == nil || apperr.Safe(err).Code != tc.code)) {
				t.Fatalf("wrong result: %+v %v", result, err)
			}
			for i, client := range clients {
				if !client.closed || dialContexts[i].Err() != context.Canceled {
					t.Fatal("client or candidate timer not closed")
				}
			}
			if lifetime.Err() != context.Canceled {
				t.Fatal("request timer not closed")
			}
			if rowFor(t, result, "snapshot_permissions", 0).State != Unverified {
				t.Fatal("Status overstated snapshot permission")
			}
			assertRedacted(t, result, err)
		})
	}
}

func TestConnectUnavailableStillChecksOtherCandidates(t *testing.T) {
	cfg := testConfig(t)
	cfg.Endpoints = append(cfg.Endpoints, "https://SECOND_CANARY.invalid:2379")
	f := &fakeConnection{info: validStatus()}
	calls := 0
	result, err := run(context.Background(), cfg, true, func(context.Context, context.Context, config.Config, int) (connection, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("REMOTE_ERROR_CANARY")
		}
		return f, nil
	})
	if err == nil || apperr.Safe(err).Code != "remote_unavailable" || calls != 2 || !f.closed ||
		rowFor(t, result, "candidate_status", 1).State != Failed || rowFor(t, result, "candidate_status", 2).State != Confirmed {
		t.Fatalf("candidate failure hidden: %+v %v", result, err)
	}
	assertRedacted(t, result, err)
}

func TestProbeFailureTimeoutAndCancellationClose(t *testing.T) {
	for _, kind := range []string{"remote", "timeout", "canceled", "factory"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeConnection{err: errors.New("RPC_CANARY")}
			want := "remote_unavailable"
			if kind == "timeout" {
				f.err, want = context.DeadlineExceeded, "timeout"
			}
			if kind == "canceled" {
				f.onStatus = func(context.Context) { cancel() }
				want = "canceled"
			}
			var dial context.Context
			result, err := run(ctx, cfg, true, func(_ context.Context, d context.Context, _ config.Config, _ int) (connection, error) {
				dial = d
				if kind == "factory" {
					return f, errors.New("AUTH_CANARY")
				}
				return f, nil
			})
			if err == nil || apperr.Safe(err).Code != want || !f.closed || dial.Err() == nil {
				t.Fatalf("bad cleanup/error: %+v %v closed=%v", result, err, f.closed)
			}
			assertRedacted(t, result, err)
		})
	}
}

func TestOfflineInvalidConfigStorageAndEndedContext(t *testing.T) {
	cfg := testConfig(t)
	cfg.Keep = 0
	result, err := Run(context.Background(), cfg, false)
	if err == nil || rowFor(t, result, "configuration", 0).State != Failed {
		t.Fatalf("invalid config accepted: %+v %v", result, err)
	}
	cfg.Keep = 1
	cfg.BackupDir = filepath.Join(cfg.BackupDir, "missing-ancestor")
	result, err = Run(context.Background(), cfg, false)
	if err == nil || rowFor(t, result, "storage_path", 0).State != Failed {
		t.Fatalf("missing ancestor not failed: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = run(ctx, cfg, true, func(context.Context, context.Context, config.Config, int) (connection, error) {
		t.Fatal("client created with ended context")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || rowFor(t, result, "candidate_status", 1).State != Unverified {
		t.Fatalf("cancellation ignored: %+v %v", result, err)
	}
}
