package storage

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckPathReadOnly(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if exists, err := CheckPath(root); exists || err != nil {
		t.Fatalf("missing final root: %v %v", exists, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root created: %v", err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if exists, err := CheckPath(root); !exists || err != nil {
		t.Fatalf("private uninitialized root: %v %v", exists, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty root changed: %v %v", entries, err)
	}
	// Arbitrary entries are deliberately not read or walked; even an active
	// permanent lock must not be replaced, acquired, or written by CheckPath.
	lock := filepath.Join(root, ".lock")
	if err := os.WriteFile(lock, []byte("LOCK_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	before, _ := f.Stat()
	if err := unix.Mkfifo(filepath.Join(root, "ignored-fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if exists, err := CheckPath(root); !exists || err != nil {
		t.Fatalf("existing root: %v %v", exists, err)
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("lock changed: %v", err)
	}
	data, err := os.ReadFile(lock)
	if err != nil || string(data) != "LOCK_CANARY" {
		t.Fatalf("lock content changed: %v", err)
	}
}

func TestCheckPathRejectsUnsafeComponents(t *testing.T) {
	base := t.TempDir()
	privateRoot := filepath.Join(base, "private")
	unsafeRoot := filepath.Join(base, "unsafe")
	ancestor := filepath.Join(base, "writable")
	for _, dir := range []string{privateRoot, unsafeRoot, ancestor} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{unsafeRoot, ancestor} {
		if err := os.Chmod(dir, 0777); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(privateRoot, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{unsafeRoot, filepath.Join(ancestor, "missing"), link, filepath.Join(link, "missing"), fifo, filepath.Join(base, "missing", "root"), privateRoot + "/../private", "/", "relative"} {
		if exists, err := CheckPath(path); exists || err == nil {
			t.Fatalf("accepted unsafe path %q: %v %v", path, exists, err)
		}
	}
}
