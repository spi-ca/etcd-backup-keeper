package storage

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// CheckPath inspects only the path's directory descriptors and permissions.
// It does not create the final root, open .lock, list entries, or test writes.
// A missing final component returns false, nil; missing ancestors are unsafe.
// This is not a substitute for Open or a guarantee of backup writability.
func CheckPath(path string) (bool, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || filepath.Clean(path) != path || path == "/" {
		return false, safeError()
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, safeError()
	}
	defer func() { unix.Close(fd) }()
	for i, part := range parts {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return false, safeError()
		}
		// Root-owned sticky ancestors (e.g. /tmp) protect existing owned
		// entries. Other group/world-writable ancestors are not controlled.
		if st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&unix.S_ISVTX != 0) {
			return false, safeError()
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if openErr == unix.ENOENT && i == len(parts)-1 {
			return false, nil
		}
		if openErr != nil {
			return false, safeError()
		}
		unix.Close(fd)
		fd = next
	}
	if !private(fd, true) {
		return false, safeError()
	}
	return true, nil
}
