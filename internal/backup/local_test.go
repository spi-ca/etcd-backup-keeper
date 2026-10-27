package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/storage"
)

func TestLocalStatusAndVerifyScope(t *testing.T) {
	cfg := testConfig(t)
	cfg.Endpoints = []string{"https://unreachable.invalid:2379"}
	cfg.Username, cfg.PasswordFile, cfg.CAFile = "test", "/absent/SECRET", "/absent/CA"
	status, err := Status(context.Background(), cfg)
	must(t, err)
	if status.Initialized || len(status.Completed) != 0 {
		t.Fatalf("bad missing status: %+v", status)
	}
	if _, err := Verify(context.Background(), cfg, ""); apperr.Safe(err).Code != "no_backups" {
		t.Fatalf("missing root verify: %v", err)
	}
	if _, err := os.Stat(cfg.BackupDir); !os.IsNotExist(err) {
		t.Fatal("local command created storage")
	}
	cfg.Keep = 5
	deps, _ := fakeDependencies(fixture(t))
	first, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	second, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	r, err := storage.Open(cfg.BackupDir)
	must(t, err)
	stage, d, err := r.CreateStage(time.Now())
	must(t, err)
	d.Close()
	anotherStage, d, err := r.CreateStage(time.Now())
	must(t, err)
	d.Close()
	deleted := "deleting-" + strings.TrimPrefix(anotherStage, "inprogress-")
	must(t, os.Rename(filepath.Join(cfg.BackupDir, anotherStage), filepath.Join(cfg.BackupDir, deleted)))
	r.Close()
	status, err = Status(context.Background(), cfg)
	must(t, err)
	if !status.Initialized || len(status.Completed) != 2 || len(status.InProgress) != 1 || status.InProgress[0] != stage || len(status.Deleting) != 1 || status.Deleting[0] != deleted || status.TotalBytes != first.Metadata.Bytes+second.Metadata.Bytes || status.LatestSuccess == nil || status.LatestSuccess.Name != second.Name {
		t.Fatalf("bad inventory: %+v", status)
	}
	verified, err := Verify(context.Background(), cfg, "")
	must(t, err)
	if verified.Scope != "all_completed" || len(verified.Verified) != 2 || !strings.Contains(verified.Assurance, "not a restoration guarantee") {
		t.Fatalf("bad verification: %+v", verified)
	}
	verified, err = Verify(context.Background(), cfg, first.Name)
	must(t, err)
	if verified.Scope != "named_completed" || len(verified.Verified) != 1 || verified.Verified[0].Name != first.Name {
		t.Fatalf("bad named verification: %+v", verified)
	}
	for _, name := range []string{stage, deleted, "backup-test", "../" + first.Name, first.Name + "/snapshot.db"} {
		if _, err := Verify(context.Background(), cfg, name); apperr.Safe(err).Code != "usage" {
			t.Fatalf("bad selector accepted %q: %v", name, err)
		}
	}
	missing := first.Name[:len(first.Name)-32] + strings.Repeat("0", 32)
	if _, err := Verify(context.Background(), cfg, missing); apperr.Safe(err).Code != "backup_not_found" {
		t.Fatalf("missing named backup: %v", err)
	}
}

func rewriteMetadata(t *testing.T, path string, m Metadata) {
	t.Helper()
	data, err := json.Marshal(m)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(path, "metadata.json"), data, 0600))
	must(t, os.WriteFile(filepath.Join(path, "SHA256SUMS"), []byte(makeManifest(m.SHA256, data)), 0600))
}

func TestLocalTampering(t *testing.T) {
	for _, kind := range []string{"snapshot", "trailer", "manifest", "metadata", "revision", "keys", "dbbytes", "wholehash", "structure"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			deps, _ := fakeDependencies(fixture(t))
			result, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			path := filepath.Join(cfg.BackupDir, result.Name)
			m := result.Metadata
			switch kind {
			case "snapshot", "trailer":
				p := filepath.Join(path, "snapshot.db")
				data, err := os.ReadFile(p)
				must(t, err)
				if kind == "snapshot" {
					data[100] ^= 1
				} else {
					data[len(data)-1] ^= 1
				}
				must(t, os.WriteFile(p, data, 0600))
			case "manifest", "metadata":
				name := "SHA256SUMS"
				if kind == "metadata" {
					name = "metadata.json"
				}
				f, err := os.OpenFile(filepath.Join(path, name), os.O_APPEND|os.O_WRONLY, 0)
				must(t, err)
				_, err = f.WriteString("tamper")
				must(t, err)
				must(t, f.Close())
			case "revision":
				m.Revision++
				rewriteMetadata(t, path, m)
			case "keys":
				m.Keys++
				rewriteMetadata(t, path, m)
			case "dbbytes":
				m.DBBytes--
				rewriteMetadata(t, path, m)
			case "wholehash":
				m.SHA256 = strings.Repeat("0", 64)
				rewriteMetadata(t, path, m)
			case "structure":
				body := make([]byte, m.Bytes-sha256.Size)
				sum := sha256.Sum256(body)
				data := append(body, sum[:]...)
				must(t, os.WriteFile(filepath.Join(path, "snapshot.db"), data, 0600))
				whole := sha256.Sum256(data)
				m.SHA256 = hex.EncodeToString(whole[:])
				rewriteMetadata(t, path, m)
			}
			_, statusErr := Status(context.Background(), cfg)
			if kind == "manifest" || kind == "metadata" {
				if statusErr == nil {
					t.Fatal("status accepted invalid metadata/manifest")
				}
			} else {
				must(t, statusErr)
			} // status MUST NOT hash or inspect snapshots.
			if _, err := Verify(context.Background(), cfg, ""); err == nil {
				t.Fatal("verify accepted tampering")
			}
		})
	}
}

func TestVerifyHoldsSharedLockThroughInspectionAndComparesAllStats(t *testing.T) {
	cfg := testConfig(t)
	deps, _ := fakeDependencies(fixture(t))
	_, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	inspect := deps.inspect
	deps.inspect = func(ctx context.Context, f *os.File) (Inspection, error) {
		if r, err := storage.Open(cfg.BackupDir); err == nil {
			r.Close()
			t.Fatal("verify released lock during inspection")
		}
		r, err := storage.OpenExisting(cfg.BackupDir)
		must(t, err)
		r.Close()
		return inspect(ctx, f)
	}
	_, err = verify(context.Background(), cfg, "", deps)
	must(t, err)
	for _, field := range []string{"revision", "keys", "dbbytes", "storage_version"} {
		deps.inspect = func(ctx context.Context, f *os.File) (Inspection, error) {
			i, err := inspect(ctx, f)
			if err != nil {
				return i, err
			}
			switch field {
			case "revision":
				i.Revision++
			case "keys":
				i.Keys++
			case "dbbytes":
				i.DBBytes++
			case "storage_version":
				i.StorageVersion = "3.8.0"
			}
			return i, nil
		}
		if _, err := verify(context.Background(), cfg, "", deps); err == nil {
			t.Fatalf("ignored inspection %s", field)
		}
	}
	r, err := storage.Open(cfg.BackupDir)
	must(t, err)
	defer r.Close()
	if _, err := Status(context.Background(), cfg); apperr.Safe(err).Code != "storage_locked" {
		t.Fatalf("status bypassed writer: %v", err)
	}
	if _, err := Verify(context.Background(), cfg, ""); apperr.Safe(err).Code != "storage_locked" {
		t.Fatalf("verify bypassed writer: %v", err)
	}
}

func TestLocalCancellationAndTimeout(t *testing.T) {
	cfg := testConfig(t)
	deps, _ := fakeDependencies(fixture(t))
	_, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Status(ctx, cfg); apperr.Safe(err).Code != "canceled" {
		t.Fatalf("status canceled: %v", err)
	}
	if _, err := Verify(ctx, cfg, ""); apperr.Safe(err).Code != "canceled" {
		t.Fatalf("verify canceled: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	deps.inspect = func(context.Context, *os.File) (Inspection, error) { cancel(); return Inspection{}, context.Canceled }
	if _, err := verify(ctx, cfg, "", deps); apperr.Safe(err).Code != "canceled" {
		t.Fatalf("verify inspection canceled: %v", err)
	}
	// Exercise the real process helper kill/reap path while the shared lock is held.
	t.Setenv("EBK_TEST_INSPECT_MODE", "hang")
	cfg.RequestTimeout = 100 * time.Millisecond
	if _, err := Verify(context.Background(), cfg, ""); apperr.Safe(err).Code != "timeout" {
		t.Fatalf("verify helper timeout: %v", err)
	}
	r, err := storage.Open(cfg.BackupDir)
	must(t, err)
	r.Close()
}
