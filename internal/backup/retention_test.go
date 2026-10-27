package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/storage"
)

func TestRetentionKeepAndClockRollback(t *testing.T) {
	cfg := testConfig(t)
	cfg.Keep = 3
	deps, _ := fakeDependencies(fixture(t))
	clock := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	deps.now = func() time.Time { return clock }
	var prior []Result
	for i := 0; i < 3; i++ {
		r, err := run(context.Background(), cfg, "test", deps)
		must(t, err)
		prior = append(prior, r)
		clock = clock.Add(time.Hour)
	}
	// Keep the newly published backup even though it is older than all priors.
	clock = clock.Add(-24 * time.Hour)
	r, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	if r.RetentionPending || len(r.Pending) != 0 || len(r.Removed) != 1 || r.Removed[0] != prior[0].Name || len(names(t, cfg.BackupDir, "backup-")) != 3 {
		t.Fatalf("bad keep result: %+v", r)
	}
	for _, name := range []string{r.Name, prior[1].Name, prior[2].Name} {
		if _, err := os.Stat(filepath.Join(cfg.BackupDir, name)); err != nil {
			t.Fatal("clock rollback removed pinned/newest backup", err)
		}
	}
	cfg.Keep = 1
	r, err = run(context.Background(), cfg, "test", deps)
	must(t, err)
	if r.RetentionPending || len(r.Removed) != 3 || len(names(t, cfg.BackupDir, "backup-")) != 1 {
		t.Fatalf("bad keep=1 result: %+v", r)
	}
	if got := names(t, cfg.BackupDir, "backup-")[0]; got != r.Name {
		t.Fatal("newest publication not pinned")
	}
}

func TestRetentionFaultsRecoverOnLaterSuccessfulBackup(t *testing.T) {
	for _, phase := range []string{"quarantine", "collision", "quarantine-sync", "unlink-snapshot", "unlink-metadata", "unlink-manifest", "directory-sync", "rmdir", "final-sync"} {
		t.Run(phase, func(t *testing.T) {
			cfg := testConfig(t)
			data := fixture(t)
			deps, _ := fakeDependencies(data)
			prior, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			priorPath := filepath.Join(cfg.BackupDir, prior.Name)
			priorBytes := treeBytes(t, priorPath)
			deps, _ = fakeDependencies(data)
			fault := errors.New("SECRET_RETENTION_CANARY")
			baseRootSync, baseDirSync := deps.syncRoot, deps.syncDir
			rootSyncs, dirSyncs := 0, 0
			deps.syncRoot = func(r *storage.Root) error {
				rootSyncs++
				if (phase == "quarantine-sync" && rootSyncs == 2) || (phase == "final-sync" && rootSyncs == 3) {
					return fault
				}
				return baseRootSync(r)
			}
			deps.syncDir = func(d *storage.Dir) error {
				dirSyncs++
				if phase == "directory-sync" && dirSyncs == 2 {
					return fault
				}
				return baseDirSync(d)
			}
			if phase == "quarantine" {
				deps.quarantine = func(*storage.Root, string) (string, error) { return "", fault }
			}
			if phase == "collision" {
				deps.quarantine = func(r *storage.Root, name string) (string, error) {
					must(t, os.Mkdir(filepath.Join(cfg.BackupDir, "deleting-"+strings.TrimPrefix(name, "backup-")), 0700))
					return r.Quarantine(name)
				}
			}
			if strings.HasPrefix(phase, "unlink-") {
				target := map[string]string{"unlink-snapshot": "snapshot.db", "unlink-metadata": "metadata.json", "unlink-manifest": "SHA256SUMS"}[phase]
				base := deps.unlink
				deps.unlink = func(d *storage.Dir, name string) error {
					if name == target {
						return fault
					}
					return base(d, name)
				}
			}
			if phase == "rmdir" {
				deps.removeDir = func(*storage.Root, string) error { return fault }
			}
			published, err := run(context.Background(), cfg, "test", deps)
			if err == nil || apperr.Safe(err).Code != "retention_incomplete" || strings.Contains(apperr.Safe(err).Message, "CANARY") || !strings.Contains(apperr.Safe(err).Message, "run status") || published.Name == "" || !published.RetentionPending || len(published.Pending) != 1 {
				t.Fatalf("bad failed cleanup: %+v %v", published, err)
			}
			publishedBytes, readErr := os.ReadFile(filepath.Join(cfg.BackupDir, published.Name, "snapshot.db"))
			must(t, readErr)
			if string(publishedBytes) != string(data) {
				t.Fatal("published snapshot changed during cleanup")
			}
			deleting := filepath.Join(cfg.BackupDir, "deleting-"+strings.TrimPrefix(prior.Name, "backup-"))
			switch phase {
			case "quarantine", "collision":
				assertPrior(t, priorPath, priorBytes)
			case "quarantine-sync", "unlink-snapshot":
				assertPrior(t, deleting, priorBytes)
			case "unlink-metadata":
				if _, err := os.Stat(filepath.Join(deleting, "snapshot.db")); !os.IsNotExist(err) {
					t.Fatal("snapshot unlink did not precede metadata failure")
				}
			case "directory-sync", "rmdir":
				entries, err := os.ReadDir(deleting)
				must(t, err)
				if len(entries) != 0 {
					t.Fatal("empty quarantine not preserved")
				}
			case "final-sync":
				if _, err := os.Stat(deleting); !os.IsNotExist(err) || len(published.Removed) != 1 {
					t.Fatal("final sync result does not reflect removal")
				}
			}
			// A destination collision requires operator resolution. Do not
			// silently erase even an empty colliding destination on a retry.
			if phase == "collision" {
				deps, _ = fakeDependencies(data)
				blocked, retryErr := run(context.Background(), cfg, "test", deps)
				if retryErr == nil || !blocked.RetentionPending {
					t.Fatal("collision was silently cleaned on retry")
				}
				assertPrior(t, priorPath, priorBytes)
				must(t, os.Remove(deleting)) // explicit test/operator resolution of empty collision
			}
			// A fresh successful backup is the only cleanup/recovery entrypoint.
			deps, _ = fakeDependencies(data)
			retried, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			if retried.RetentionPending || len(retried.Pending) != 0 || len(names(t, cfg.BackupDir, "deleting-")) != 0 || len(names(t, cfg.BackupDir, "backup-")) != 1 {
				t.Fatalf("restart did not converge: %+v", retried)
			}
		})
	}
}

func TestRetentionCancellationAtEachPhase(t *testing.T) {
	for _, phase := range []string{"quarantine", "quarantine-sync", "snapshot", "metadata", "manifest", "directory-sync", "rmdir", "final-sync"} {
		t.Run(phase, func(t *testing.T) {
			cfg := testConfig(t)
			data := fixture(t)
			deps, _ := fakeDependencies(data)
			_, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deps, _ = fakeDependencies(data)
			quarantine, rootSync, unlink, dirSync, remove := deps.quarantine, deps.syncRoot, deps.unlink, deps.syncDir, deps.removeDir
			deps.quarantine = func(r *storage.Root, name string) (string, error) {
				name, err := quarantine(r, name)
				if phase == "quarantine" {
					cancel()
				}
				return name, err
			}
			count := 0
			deps.syncRoot = func(r *storage.Root) error {
				count++
				err := rootSync(r)
				if (phase == "quarantine-sync" && count == 2) || (phase == "final-sync" && count == 3) {
					cancel()
				}
				return err
			}
			deps.unlink = func(d *storage.Dir, file string) error {
				err := unlink(d, file)
				if file == map[string]string{"snapshot": "snapshot.db", "metadata": "metadata.json", "manifest": "SHA256SUMS"}[phase] {
					cancel()
				}
				return err
			}
			dirCount := 0
			deps.syncDir = func(d *storage.Dir) error {
				dirCount++
				err := dirSync(d)
				if phase == "directory-sync" && dirCount == 2 {
					cancel()
				}
				return err
			}
			deps.removeDir = func(r *storage.Root, name string) error {
				err := remove(r, name)
				if phase == "rmdir" {
					cancel()
				}
				return err
			}
			r, err := run(ctx, cfg, "test", deps)
			if apperr.Safe(err).Code != "canceled" || r.Name == "" || !r.RetentionPending {
				t.Fatalf("bad cancellation result: %+v %v", r, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.BackupDir, r.Name)); err != nil {
				t.Fatal("new publication removed", err)
			}
			deps, _ = fakeDependencies(data)
			r, err = run(context.Background(), cfg, "test", deps)
			must(t, err)
			if r.RetentionPending || len(names(t, cfg.BackupDir, "deleting-")) != 0 || len(names(t, cfg.BackupDir, "backup-")) != 1 {
				t.Fatal("cancel recovery did not converge")
			}
		})
	}
}

func TestRetentionPreservesInprogressAndOnlyRecoversAfterSuccessfulPublication(t *testing.T) {
	cfg := testConfig(t)
	data := fixture(t)
	deps, _ := fakeDependencies(data)
	prior, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	r, err := storage.Open(cfg.BackupDir)
	must(t, err)
	deleting, err := r.Quarantine(prior.Name)
	must(t, err)
	must(t, r.Sync())
	stage, d, err := r.CreateStage(time.Now())
	must(t, err)
	f, err := d.OpenFile("snapshot.db", true)
	must(t, err)
	_, err = f.WriteString("incomplete canary")
	must(t, err)
	f.Close()
	d.Close()
	r.Close()
	quarantineBytes := treeBytes(t, filepath.Join(cfg.BackupDir, deleting))
	for _, mode := range []string{"failed", "canceled", "publication-sync"} {
		deps, _ := fakeDependencies(data)
		ctx, cancel := context.WithCancel(context.Background())
		deps.unlink = func(*storage.Dir, string) error { t.Fatal("retention ran without durable success"); return nil }
		if mode == "failed" {
			deps.write = func(*os.File, []byte) (int, error) { return 0, errors.New("failed") }
		}
		if mode == "canceled" {
			deps.inspect = func(context.Context, *os.File) (Inspection, error) { cancel(); return Inspection{}, context.Canceled }
		}
		if mode == "publication-sync" {
			deps.syncRoot = func(*storage.Root) error { return errors.New("failed") }
		}
		_, err := run(ctx, cfg, "test", deps)
		cancel()
		if err == nil {
			t.Fatal("expected failed backup")
		}
		assertPrior(t, filepath.Join(cfg.BackupDir, deleting), quarantineBytes)
	}
	deps, _ = fakeDependencies(data)
	result, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	if result.RetentionPending || len(names(t, cfg.BackupDir, "deleting-")) != 0 {
		t.Fatal("pending cleanup not retried")
	}
	content, err := os.ReadFile(filepath.Join(cfg.BackupDir, stage, "snapshot.db"))
	must(t, err)
	if string(content) != "incomplete canary" {
		t.Fatal("inprogress modified")
	}
}

func TestRetentionRejectsUnsafeEntriesAndPreservesExternalSentinel(t *testing.T) {
	for _, kind := range []string{"unknown-root", "unknown-file", "symlink-dir", "symlink-file", "hardlink", "fifo", "nested", "public-file", "metadata", "deleting-unknown"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			data := fixture(t)
			deps, _ := fakeDependencies(data)
			prior, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			sentinel := filepath.Join(t.TempDir(), "external")
			must(t, os.WriteFile(sentinel, []byte("EXTERNAL_SENTINEL"), 0600))
			path := filepath.Join(cfg.BackupDir, prior.Name)
			deps, _ = fakeDependencies(data)
			baseSync := deps.syncRoot
			deps.syncRoot = func(r *storage.Root) error {
				if err := baseSync(r); err != nil {
					return err
				}
				switch kind {
				case "unknown-root":
					must(t, os.Mkdir(filepath.Join(cfg.BackupDir, "unknown"), 0700))
				case "unknown-file":
					must(t, os.WriteFile(filepath.Join(path, "unknown"), []byte("keep"), 0600))
				case "symlink-dir":
					moved := filepath.Join(filepath.Dir(sentinel), "moved-backup")
					must(t, os.Rename(path, moved))
					must(t, os.Symlink(moved, path))
				case "symlink-file", "hardlink", "fifo", "nested":
					file := filepath.Join(path, "snapshot.db")
					must(t, os.Remove(file))
					switch kind {
					case "symlink-file":
						must(t, os.Symlink(sentinel, file))
					case "hardlink":
						must(t, os.Link(sentinel, file))
					case "fifo":
						must(t, unix.Mkfifo(file, 0600))
					case "nested":
						must(t, os.Mkdir(file, 0700))
						must(t, os.WriteFile(filepath.Join(file, "sentinel"), []byte("keep"), 0600))
					}
				case "public-file":
					must(t, os.Chmod(filepath.Join(path, "snapshot.db"), 0644))
				case "metadata":
					must(t, os.WriteFile(filepath.Join(path, "metadata.json"), []byte("bad"), 0600))
				case "deleting-unknown":
					name, err := r.Quarantine(prior.Name)
					must(t, err)
					path = filepath.Join(cfg.BackupDir, name)
					must(t, os.WriteFile(filepath.Join(path, "unknown"), []byte("keep"), 0600))
				}
				return nil
			}
			deps.unlink = func(*storage.Dir, string) error { t.Fatal("unsafe inventory reached unlink"); return nil }
			result, err := run(context.Background(), cfg, "test", deps)
			if err == nil || result.Name == "" || !result.RetentionPending || len(result.Removed) != 0 {
				t.Fatalf("unsafe retention accepted: %+v %v", result, err)
			}
			content, err := os.ReadFile(sentinel)
			must(t, err)
			if string(content) != "EXTERNAL_SENTINEL" {
				t.Fatal("external sentinel changed")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unsafe entry removed")
			}
		})
	}
}

func TestRetentionOrderingAndOriginalExclusiveLock(t *testing.T) {
	cfg := testConfig(t)
	data := fixture(t)
	deps, _ := fakeDependencies(data)
	_, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	deps, _ = fakeDependencies(data)
	var events []string
	rootSync, quarantine, unlink, dirSync, remove := deps.syncRoot, deps.quarantine, deps.unlink, deps.syncDir, deps.removeDir
	deps.syncRoot = func(r *storage.Root) error { events = append(events, "root:sync"); return rootSync(r) }
	deps.quarantine = func(r *storage.Root, name string) (string, error) {
		events = append(events, "quarantine")
		return quarantine(r, name)
	}
	deps.unlink = func(d *storage.Dir, file string) error {
		// Neither reader nor another writer can acquire during cleanup.
		if r, err := storage.OpenExisting(cfg.BackupDir); err == nil {
			r.Close()
			t.Fatal("exclusive lock released before retention")
		}
		if r, err := storage.Open(cfg.BackupDir); err == nil {
			r.Close()
			t.Fatal("concurrent writer acquired during retention")
		}
		events = append(events, "unlink:"+file)
		return unlink(d, file)
	}
	deps.syncDir = func(d *storage.Dir) error { events = append(events, "dir:sync"); return dirSync(d) }
	deps.removeDir = func(r *storage.Root, name string) error { events = append(events, "rmdir"); return remove(r, name) }
	r, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	want := "dir:sync,root:sync,quarantine,root:sync,unlink:snapshot.db,unlink:metadata.json,unlink:SHA256SUMS,dir:sync,rmdir,root:sync"
	if strings.Join(events, ",") != want || r.RetentionPending {
		t.Fatalf("wrong retention ordering: %v %+v", events, r)
	}
}

func TestAllSafeDeletingSubsetsRecover(t *testing.T) {
	data := fixture(t)
	for mask := 0; mask < 8; mask++ {
		t.Run(string(rune('0'+mask)), func(t *testing.T) {
			cfg := testConfig(t)
			deps, _ := fakeDependencies(data)
			prior, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			r, err := storage.Open(cfg.BackupDir)
			must(t, err)
			name, err := r.Quarantine(prior.Name)
			must(t, err)
			must(t, r.Sync())
			r.Close()
			for i, file := range []string{"snapshot.db", "metadata.json", "SHA256SUMS"} {
				if mask&(1<<i) == 0 {
					must(t, os.Remove(filepath.Join(cfg.BackupDir, name, file)))
				}
			}
			result, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			if result.RetentionPending || len(result.Removed) != 1 || result.Removed[0] != prior.Name || len(names(t, cfg.BackupDir, "deleting-")) != 0 {
				t.Fatalf("safe subset did not recover: %+v", result)
			}
		})
	}
}

func TestFullDeletingMetadataMustBeStrictBeforeAnyDeletion(t *testing.T) {
	cfg := testConfig(t)
	data := fixture(t)
	deps, _ := fakeDependencies(data)
	prior, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	r, err := storage.Open(cfg.BackupDir)
	must(t, err)
	name, err := r.Quarantine(prior.Name)
	must(t, err)
	must(t, r.Sync())
	r.Close()
	path := filepath.Join(cfg.BackupDir, name)
	must(t, os.WriteFile(filepath.Join(path, "metadata.json"), []byte("invalid"), 0600))
	before := treeBytes(t, path)
	deps.unlink = func(*storage.Dir, string) error { t.Fatal("invalid deleting metadata reached unlink"); return nil }
	result, err := run(context.Background(), cfg, "test", deps)
	if err == nil || result.Name == "" || !result.RetentionPending {
		t.Fatalf("invalid full deleting tree accepted: %+v %v", result, err)
	}
	assertPrior(t, path, before)
}
