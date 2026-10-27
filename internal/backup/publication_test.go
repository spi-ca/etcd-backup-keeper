package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/storage"
)

func TestPublicationOrdering(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	deps, _ := fakeDependencies(data)
	var events []string
	syncFile, syncDir, publish, syncRoot := deps.syncFile, deps.syncDir, deps.publish, deps.syncRoot
	deps.syncFile = func(f *os.File) error { events = append(events, f.Name()+":sync"); return syncFile(f) }
	deps.syncDir = func(d *storage.Dir) error { events = append(events, "stage:sync"); return syncDir(d) }
	deps.publish = func(r *storage.Root, a, b string) error { events = append(events, "rename"); return publish(r, a, b) }
	deps.syncRoot = func(r *storage.Root) error { events = append(events, "root:sync"); return syncRoot(r) }
	_, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	want := "snapshot.db:sync,metadata.json:sync,SHA256SUMS:sync,stage:sync,rename,root:sync"
	if strings.Join(events, ",") != want {
		t.Fatalf("publication ordering: %v", events)
	}
}
func TestCancellationAfterRenamePreservesCompletedBackup(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	deps, _ := fakeDependencies(data)
	prior, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	priorPath := filepath.Join(cfg.BackupDir, prior.Name)
	priorBytes := treeBytes(t, priorPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps, _ = fakeDependencies(data)
	syncRoot := deps.syncRoot
	deps.syncRoot = func(r *storage.Root) error { err := syncRoot(r); cancel(); return err }
	_, err = run(ctx, cfg, "test", deps)
	if err == nil || apperr.Safe(err).Code != "canceled" || !strings.Contains(apperr.Safe(err).Message, "completed backup preserved") {
		t.Fatalf("wrong post-publication cancel: %v", err)
	}
	assertPrior(t, priorPath, priorBytes)
	if len(names(t, cfg.BackupDir, "backup-")) != 2 || len(names(t, cfg.BackupDir, "inprogress-")) != 0 {
		t.Fatal("post-rename cancellation rolled back or ran retention")
	}
}
func TestShortLocalWriteNeverFailsOver(t *testing.T) {
	cfg := testConfig(t)
	deps, c := fakeDependencies(fixture(t))
	deps.write = func(*os.File, []byte) (int, error) { return 0, nil }
	_, err := run(context.Background(), cfg, "test", deps)
	if err == nil || apperr.Safe(err).Code != "backup_incomplete" || c[1].snapshots != 0 {
		t.Fatalf("short local write treated as remote: %v", err)
	}
}
