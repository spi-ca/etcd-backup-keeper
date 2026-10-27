package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
	"etcd-backup-keeper/internal/storage"
)

func TestInspectionOutputCapThroughCopy(t *testing.T) {
	var output cappedBuffer
	// Hide Reader.WriteTo so io.Copy must choose the destination interface.
	source := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 8192))}
	_, err := io.Copy(&output, source)
	if !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("oversized inspection output was not rejected: %v", err)
	}
	if output.buffer.Len() > 4096 {
		t.Fatal("inspection output exceeded its memory bound")
	}
	if _, err := output.Write([]byte("valid")); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionHelperTimeoutReapsBeforeUnlock(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	deps, _ := fakeDependencies(data)
	t.Setenv("EBK_TEST_INSPECT_MODE", "hang")
	started := time.Now()
	deps.inspect = func(ctx context.Context, f *os.File) (Inspection, error) {
		// The live operation must retain its lock during child inspection.
		if r, err := storage.Open(cfg.BackupDir); err == nil {
			r.Close()
			t.Fatal("lock released before inspection")
		}
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		result, err := Inspect(short, f)
		if short.Err() == nil {
			t.Fatal("expected bounded helper timeout")
		}
		return result, err
	}
	_, err := run(context.Background(), cfg, "test", deps)
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("helper did not stop promptly: %v", err)
	}
	root, err := storage.Open(cfg.BackupDir)
	must(t, err)
	root.Close()
	if len(names(t, cfg.BackupDir, "inprogress-")) != 1 {
		t.Fatal("inspection cancellation lost staging")
	}
}
func TestInspectionErrorsNeverExposeDBValues(t *testing.T) {
	data := fixture(t)
	path := filepath.Join(t.TempDir(), "malformed.db")
	must(t, os.WriteFile(path, data[:len(data)-32], 0600))
	db, err := bolt.Open(path, 0600, nil)
	must(t, err)
	must(t, db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("key")).Put(fixtureRevisionKey(), []byte("SECRET_DB_CANARY"))
	}))
	must(t, db.Close())
	body, err := os.ReadFile(path)
	must(t, err)
	sum := sha256.Sum256(body)
	data = append(body, sum[:]...)
	cfg := testConfig(t)
	deps, c := fakeDependencies(data)
	_, err = run(context.Background(), cfg, "test", deps)
	if err == nil || strings.Contains(apperr.Safe(err).Message, "CANARY") || c[1].snapshots != 0 {
		t.Fatalf("unsafe inspection result: %v", err)
	}
}
func TestAuthenticationAndProbeShareOneBudget(t *testing.T) {
	// The connector receives a short auth/probe budget separately from the total
	// lifetime, so completing initialization never cancels snapshot streaming.
	data := fixture(t)
	cfg := testConfig(t)
	cfg.DialTimeout = 100 * time.Millisecond
	deps, _ := fakeDependencies(data)
	original := deps.connect
	deps.connect = func(total, dial context.Context, c config.Config, i int) (connection, error) {
		totalDeadline, ok := total.Deadline()
		if !ok {
			t.Fatal("unbounded total context")
		}
		dialDeadline, ok := dial.Deadline()
		if !ok || !dialDeadline.Before(totalDeadline) {
			t.Fatal("unbounded auth/probe context")
		}
		return original(total, dial, c, i)
	}
	_, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
}
