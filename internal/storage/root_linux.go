// Package storage confines local backup operations to pinned Linux directory
// descriptors. Ancestors must be operator-controlled; hostile same-UID/root
// processes and privileged mount changes are outside this boundary.
package storage

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"etcd-backup-keeper/internal/apperr"
)

var backupName = regexp.MustCompile(`^(backup|inprogress|deleting)-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[a-f0-9]{32}$`)
var files = map[string]bool{"snapshot.db": true, "metadata.json": true, "SHA256SUMS": true}

type Root struct {
	dir, lock *os.File
	readOnly  bool
}
type Dir struct {
	file     *os.File
	readOnly bool
}

func safeError() error {
	return apperr.New("storage_unsafe", "backup storage is inaccessible or unsafe")
}

// Open creates only the final component. It never follows symlinks, including
// ancestor symlinks. Existing roots must already be private and owned by euid.
func Open(path string) (*Root, error) { return openRoot(path, false) }

// OpenExisting takes a nonblocking shared lock without creating, writing or
// syncing anything. A missing root (including an absent ancestor) returns
// os.ErrNotExist; an existing root without its permanent lock is unsafe.
func OpenExisting(path string) (*Root, error) { return openRoot(path, true) }

func openRoot(path string, readOnly bool) (*Root, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || filepath.Clean(path) != path || path == "/" {
		return nil, safeError()
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, safeError()
	}
	for i, part := range parts {
		if i == len(parts)-1 && !readOnly {
			err = unix.Mkdirat(fd, part, 0700)
			if err == nil {
				err = unix.Fsync(fd)
			} else if err == unix.EEXIST {
				err = nil
			}
			if err != nil {
				unix.Close(fd)
				return nil, safeError()
			}
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		unix.Close(fd)
		if openErr != nil {
			if readOnly && openErr == unix.ENOENT {
				return nil, os.ErrNotExist
			}
			return nil, safeError()
		}
		fd = next
	}
	dir := os.NewFile(uintptr(fd), "backup-root")
	if !private(fd, true) {
		dir.Close()
		return nil, safeError()
	}
	r := &Root{dir: dir, readOnly: readOnly}
	flags, lockMode := unix.O_RDWR|unix.O_CREAT, unix.LOCK_EX
	if readOnly {
		flags, lockMode = unix.O_RDONLY, unix.LOCK_SH
	}
	lockFD, err := unix.Openat(fd, ".lock", flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		r.Close()
		return nil, safeError()
	}
	r.lock = os.NewFile(uintptr(lockFD), ".lock")
	if !private(lockFD, false) {
		r.Close()
		return nil, safeError()
	}
	if err = unix.Flock(lockFD, lockMode|unix.LOCK_NB); err != nil {
		r.Close()
		return nil, apperr.New("storage_locked", "backup storage is locked by another operation")
	}
	// The lock is permanent: never unlink it, including on failure.
	if readOnly {
		return r, nil
	}
	if err = r.lock.Sync(); err == nil {
		err = dir.Sync()
	}
	if err != nil {
		r.Close()
		return nil, safeError()
	}
	return r, nil
}

func private(fd int, directory bool) bool {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) {
		return false
	}
	if directory {
		return st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&07777 == 0700
	}
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Nlink == 1
}

func (r *Root) Close() error {
	if r.lock != nil {
		r.lock.Close()
	}
	if r.dir != nil {
		return r.dir.Close()
	}
	return nil
}
func (d *Dir) Close() error { return d.file.Close() }
func (r *Root) Sync() error {
	if r.readOnly {
		return safeError()
	}
	return r.dir.Sync()
}
func (d *Dir) Sync() error {
	if d.readOnly {
		return safeError()
	}
	return d.file.Sync()
}

func ValidName(name string) bool { return backupName.MatchString(name) }
func (r *Root) OpenDir(name string) (*Dir, error) {
	if !ValidName(name) {
		return nil, safeError()
	}
	fd, err := unix.Openat(int(r.dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, safeError()
	}
	if !private(fd, true) {
		unix.Close(fd)
		return nil, safeError()
	}
	return &Dir{file: os.NewFile(uintptr(fd), name), readOnly: r.readOnly}, nil
}

func (r *Root) CreateStage(now time.Time) (string, *Dir, error) {
	if r.readOnly {
		return "", nil, safeError()
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, safeError()
	}
	name := "inprogress-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random[:])
	if err := unix.Mkdirat(int(r.dir.Fd()), name, 0700); err != nil {
		return "", nil, safeError()
	}
	d, err := r.OpenDir(name)
	if err != nil {
		return name, nil, err
	}
	// Persist the staging entry early so failures leave inspectable state.
	if err = r.Sync(); err != nil {
		d.Close()
		return name, nil, safeError()
	}
	return name, d, nil
}

func (d *Dir) OpenFile(name string, create bool) (*os.File, error) {
	if !files[name] || (create && d.readOnly) {
		return nil, safeError()
	}
	flags := unix.O_RDONLY
	if create {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(int(d.file.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, safeError()
	}
	if !private(fd, false) {
		unix.Close(fd)
		return nil, safeError()
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Publish fails closed if the filesystem/kernel does not implement atomic
// no-replace rename. There is deliberately no stat-then-rename fallback.
func (r *Root) Publish(stage, completed string) error {
	if r.readOnly || !ValidName(stage) || !strings.HasPrefix(stage, "inprogress-") || !ValidName(completed) || !strings.HasPrefix(completed, "backup-") {
		return safeError()
	}
	return unix.Renameat2(int(r.dir.Fd()), stage, int(r.dir.Fd()), completed, unix.RENAME_NOREPLACE)
}

// Inventory rejects unexpected names and unsafe trees, including incomplete
// ones. It does not recurse or hash snapshots. Complete layouts are exact.
func (r *Root) Inventory() ([]string, error) {
	fd, err := unix.Openat(int(r.dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, safeError()
	}
	f := os.NewFile(uintptr(fd), "inventory")
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, safeError()
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			var st, held unix.Stat_t
			if unix.Fstatat(int(r.dir.Fd()), ".lock", &st, unix.AT_SYMLINK_NOFOLLOW) != nil || unix.Fstat(int(r.lock.Fd()), &held) != nil || st.Ino != held.Ino || st.Dev != held.Dev || !private(int(r.lock.Fd()), false) {
				return nil, safeError()
			}
			continue
		}
		d, err := r.OpenDir(entry.Name())
		if err != nil {
			return nil, err
		}
		err = d.checkLayout(strings.HasPrefix(entry.Name(), "backup-"))
		d.Close()
		if err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func (d *Dir) checkLayout(complete bool) error {
	names, err := d.FileNames()
	if err != nil {
		return err
	}
	if complete && len(names) != len(files) {
		return safeError()
	}
	return nil
}

// FileNames returns only validated allowlisted private files. It never recurses.
func (d *Dir) FileNames() ([]string, error) {
	// Open a fresh directory description: repeated validation must not reuse
	// the directory stream's EOF offset.
	fd, err := unix.Openat(int(d.file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, safeError()
	}
	f := os.NewFile(uintptr(fd), "layout")
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, safeError()
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		f, err := d.OpenFile(entry.Name(), false)
		if err != nil {
			return nil, err
		}
		f.Close()
		names = append(names, entry.Name())
	}
	return names, nil
}

// Available uses unprivileged available blocks, not superuser-reserved space.
func (r *Root) Available() (uint64, error) {
	var st unix.Statfs_t
	if unix.Fstatfs(int(r.dir.Fd()), &st) != nil || st.Bsize <= 0 {
		return 0, safeError()
	}
	size := uint64(st.Bsize)
	if st.Bavail > ^uint64(0)/size {
		return ^uint64(0), nil
	}
	return st.Bavail * size, nil
}

// CheckLayout validates the entire fixed file allowlist before destructive work.
// Partial layouts are allowed only for already quarantined deletion recovery.
func (d *Dir) CheckLayout(complete bool) error { return d.checkLayout(complete) }

// Quarantine atomically moves a validated completed tree out of the completed
// namespace. The caller MUST sync the root before unlinking anything.
func (r *Root) Quarantine(name string) (string, error) {
	if r.readOnly || !ValidName(name) || !strings.HasPrefix(name, "backup-") {
		return "", safeError()
	}
	target := "deleting-" + strings.TrimPrefix(name, "backup-")
	if err := unix.Renameat2(int(r.dir.Fd()), name, int(r.dir.Fd()), target, unix.RENAME_NOREPLACE); err != nil {
		return "", safeError()
	}
	return target, nil
}

// Unlink removes only an expected private regular single-link file, never a
// directory or symlink. Missing files are safe during quarantine recovery.
func (d *Dir) Unlink(name string) error {
	if d.readOnly || !strings.HasPrefix(d.file.Name(), "deleting-") || !files[name] {
		return safeError()
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(d.file.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err == unix.ENOENT {
		return nil
	} else if err != nil {
		return safeError()
	}
	f, err := d.OpenFile(name, false)
	if err != nil {
		return err
	}
	f.Close()
	if err := unix.Unlinkat(int(d.file.Fd()), name, 0); err != nil {
		return safeError()
	}
	return nil
}

// RemoveEmpty never recurses; unexpected entries prevent removal.
func (r *Root) RemoveEmpty(name string) error {
	if r.readOnly || !ValidName(name) || !strings.HasPrefix(name, "deleting-") {
		return safeError()
	}
	d, err := r.OpenDir(name)
	if err != nil {
		return err
	}
	err = d.checkLayout(false)
	d.Close()
	if err != nil {
		return err
	}
	if err := unix.Unlinkat(int(r.dir.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return safeError()
	}
	return nil
}
