package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newRoot(t *testing.T) (*Root, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backups")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, path
}
func TestRootPrivateAndPermanentLock(t *testing.T) {
	r, path := newRoot(t)
	for name, mode := range map[string]os.FileMode{"": 0700, ".lock": 0600} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
	before, err := os.Stat(filepath.Join(path, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(path); err == nil {
		other.Close()
		t.Fatal("accepted contended lock")
	}
	r.Close()
	next, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	after, _ := os.Stat(filepath.Join(path, ".lock"))
	if !os.SameFile(before, after) {
		t.Fatal("lock inode replaced")
	}
}
func TestRejectUnsafeRootAndAncestors(t *testing.T) {
	for _, kind := range []string{"root-symlink", "ancestor-symlink", "public", "fifo", "hard-lock", "symlink-lock", "fifo-lock", "public-lock"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "root")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "root-symlink":
				path = filepath.Join(base, "link")
				must(t, os.Symlink(filepath.Join(base, "root"), path))
			case "ancestor-symlink":
				must(t, os.Symlink(path, filepath.Join(base, "link")))
				path = filepath.Join(base, "link", "child")
			case "public":
				must(t, os.Chmod(path, 0750))
			case "fifo":
				must(t, os.Remove(path))
				must(t, unix.Mkfifo(path, 0600))
			case "hard-lock":
				must(t, os.WriteFile(filepath.Join(base, "other"), nil, 0600))
				must(t, os.Link(filepath.Join(base, "other"), filepath.Join(path, ".lock")))
			case "symlink-lock":
				must(t, os.Symlink("missing", filepath.Join(path, ".lock")))
			case "fifo-lock":
				must(t, unix.Mkfifo(filepath.Join(path, ".lock"), 0600))
			case "public-lock":
				must(t, os.WriteFile(filepath.Join(path, ".lock"), nil, 0600))
				must(t, os.Chmod(filepath.Join(path, ".lock"), 0644))
			}
			if r, err := Open(path); err == nil {
				r.Close()
				t.Fatal("unsafe root accepted")
			}
		})
	}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestUnsafeTreesRejected(t *testing.T) {
	for _, kind := range []string{"symlink-dir", "public-dir", "file-symlink", "file-hardlink", "file-fifo", "file-directory", "file-public", "unknown", "incomplete-completed"} {
		t.Run(kind, func(t *testing.T) {
			r, path := newRoot(t)
			name, d, err := r.CreateStage(time.Now())
			must(t, err)
			d.Close()
			dir := filepath.Join(path, name)
			switch kind {
			case "symlink-dir":
				must(t, os.Remove(dir))
				must(t, os.Symlink(t.TempDir(), dir))
			case "public-dir":
				must(t, os.Chmod(dir, 0755))
			case "file-symlink":
				must(t, os.Symlink("missing", filepath.Join(dir, "snapshot.db")))
			case "file-hardlink":
				must(t, os.WriteFile(filepath.Join(path, "outside"), nil, 0600))
				must(t, os.Link(filepath.Join(path, "outside"), filepath.Join(dir, "snapshot.db")))
				must(t, os.Rename(filepath.Join(path, "outside"), filepath.Join(filepath.Dir(path), "outside")))
			case "file-fifo":
				must(t, unix.Mkfifo(filepath.Join(dir, "snapshot.db"), 0600))
			case "file-directory":
				must(t, os.Mkdir(filepath.Join(dir, "snapshot.db"), 0700))
			case "file-public":
				must(t, os.WriteFile(filepath.Join(dir, "snapshot.db"), nil, 0600))
				must(t, os.Chmod(filepath.Join(dir, "snapshot.db"), 0640))
			case "unknown":
				must(t, os.WriteFile(filepath.Join(dir, "unknown"), nil, 0600))
			case "incomplete-completed":
				must(t, os.Rename(dir, filepath.Join(path, "backup-"+strings.TrimPrefix(name, "inprogress-"))))
			}
			if _, err := r.Inventory(); err == nil {
				t.Fatal("unsafe tree accepted")
			}
		})
	}
}
func TestExclusiveFilesAndNoReplace(t *testing.T) {
	r, path := newRoot(t)
	name, d, err := r.CreateStage(time.Now())
	must(t, err)
	defer d.Close()
	f, err := d.OpenFile("snapshot.db", true)
	must(t, err)
	must(t, f.Close())
	if f, err := d.OpenFile("snapshot.db", true); err == nil {
		f.Close()
		t.Fatal("overwrote file")
	}
	target := "backup-" + strings.TrimPrefix(name, "inprogress-")
	must(t, os.Mkdir(filepath.Join(path, target), 0700))
	must(t, os.WriteFile(filepath.Join(path, target, "marker"), []byte("prior"), 0600))
	if err = r.Publish(name, target); err == nil {
		t.Fatal("overwrote existing directory")
	}
	data, err := os.ReadFile(filepath.Join(path, target, "marker"))
	must(t, err)
	if string(data) != "prior" {
		t.Fatal("destination changed")
	}
	if _, err = os.Stat(filepath.Join(path, name)); err != nil {
		t.Fatal("source not preserved")
	}
}
func TestPinnedRoot(t *testing.T) {
	r, path := newRoot(t)
	moved := path + "-moved"
	must(t, os.Rename(path, moved))
	must(t, os.Mkdir(path, 0700))
	name, d, err := r.CreateStage(time.Now())
	must(t, err)
	d.Close()
	if _, err = os.Stat(filepath.Join(moved, name)); err != nil {
		t.Fatal("lost pinned root")
	}
	if _, err = os.Stat(filepath.Join(path, name)); !os.IsNotExist(err) {
		t.Fatal("used replacement path")
	}
}

func TestExistingSharedLocksAndNoMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "backups")
	if r, err := OpenExisting(path); !os.IsNotExist(err) {
		if r != nil {
			r.Close()
		}
		t.Fatalf("missing root: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("created missing ancestor")
	}
	r, path := newRoot(t)
	if other, err := OpenExisting(path); err == nil {
		other.Close()
		t.Fatal("reader bypassed writer")
	}
	name, d, err := r.CreateStage(time.Now())
	must(t, err)
	d.Close()
	r.Close()
	before, err := os.Stat(path)
	must(t, err)
	lockBefore, err := os.Stat(filepath.Join(path, ".lock"))
	must(t, err)
	a, err := OpenExisting(path)
	must(t, err)
	defer a.Close()
	b, err := OpenExisting(path)
	must(t, err)
	defer b.Close()
	if other, err := Open(path); err == nil {
		other.Close()
		t.Fatal("writer bypassed readers")
	}
	if _, err := a.Inventory(); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err == nil {
		t.Fatal("read-only root permits sync")
	}
	if _, d, err := a.CreateStage(time.Now()); err == nil {
		d.Close()
		t.Fatal("read-only root permits stage")
	}
	d, err = a.OpenDir(name)
	must(t, err)
	defer d.Close()
	if f, err := d.OpenFile("snapshot.db", true); err == nil {
		f.Close()
		t.Fatal("read-only directory permits creation")
	}
	if err := d.Sync(); err == nil {
		t.Fatal("read-only directory permits sync")
	}
	after, err := os.Stat(path)
	must(t, err)
	lockAfter, err := os.Stat(filepath.Join(path, ".lock"))
	must(t, err)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || !os.SameFile(lockBefore, lockAfter) || !lockBefore.ModTime().Equal(lockAfter.ModTime()) {
		t.Fatal("reader mutated storage")
	}
}

func TestExistingRootWithoutLockFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backups")
	must(t, os.Mkdir(path, 0700))
	if r, err := OpenExisting(path); err == nil || os.IsNotExist(err) {
		if r != nil {
			r.Close()
		}
		t.Fatal("existing root without lock must fail closed")
	}
	if _, err := os.Stat(filepath.Join(path, ".lock")); !os.IsNotExist(err) {
		t.Fatal("created lock")
	}
}

func TestQuarantineNoReplaceAndAllowlist(t *testing.T) {
	r, path := newRoot(t)
	stage, d, err := r.CreateStage(time.Now())
	must(t, err)
	d.Close()
	completed := "backup-" + strings.TrimPrefix(stage, "inprogress-")
	must(t, r.Publish(stage, completed))
	target := "deleting-" + strings.TrimPrefix(stage, "inprogress-")
	must(t, os.Mkdir(filepath.Join(path, target), 0700))
	must(t, os.WriteFile(filepath.Join(path, target, "sentinel"), []byte("keep"), 0600))
	if _, err := r.Quarantine(completed); err == nil {
		t.Fatal("replaced quarantine collision")
	}
	data, err := os.ReadFile(filepath.Join(path, target, "sentinel"))
	must(t, err)
	if string(data) != "keep" {
		t.Fatal("collision sentinel changed")
	}
	if _, err := os.Stat(filepath.Join(path, completed)); err != nil {
		t.Fatal("collision lost source")
	}
	d, err = r.OpenDir(target)
	must(t, err)
	defer d.Close()
	if err := d.Unlink("sentinel"); err == nil {
		t.Fatal("removed unexpected file")
	}
	if err := r.RemoveEmpty(target); err == nil {
		t.Fatal("removed nonempty unknown tree")
	}
}
