package backup

import (
	"context"
	"errors"
	"sort"
	"strings"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/storage"
)

func retentionError(ctx context.Context) error {
	code := "retention_incomplete"
	if ctx.Err() != nil {
		code = "canceled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
	}
	return apperr.New(code, "new completed backup is published; retention cleanup is pending; run status and retry with a successful backup")
}

// retain is called ONLY after the new backup's publication and root fsync,
// while run still holds its original exclusive lock. All inventories and
// strict completed metadata are validated before any old backup is touched.
func retain(ctx context.Context, root *storage.Root, keep int, result *Result, deps dependencies) error {
	names, err := root.Inventory()
	if err != nil {
		return retentionError(ctx)
	}
	// A duplicate backup/deleting suffix cannot result from our atomic
	// quarantine. Never erase a destination collision as "recovery" first.
	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
	}
	for _, name := range names {
		if strings.HasPrefix(name, "backup-") && present["deleting-"+strings.TrimPrefix(name, "backup-")] {
			return retentionError(ctx)
		}
	}
	var completed []Entry
	var deleting []string
	for _, name := range names {
		if ctx.Err() != nil {
			return retentionError(ctx)
		}
		if strings.HasPrefix(name, "deleting-") {
			d, err := root.OpenDir(name)
			if err != nil {
				return retentionError(ctx)
			}
			err = validateDeleting(d)
			d.Close()
			if err != nil {
				return retentionError(ctx)
			}
			deleting = append(deleting, name)
			continue
		}
		if !strings.HasPrefix(name, "backup-") {
			continue
		}
		d, err := root.OpenDir(name)
		if err != nil {
			return retentionError(ctx)
		}
		m, err := ReadMetadata(d)
		d.Close()
		if err != nil {
			return retentionError(ctx)
		}
		if name != result.Name {
			completed = append(completed, Entry{Name: name, Metadata: m})
		}
	}
	sort.Slice(completed, func(i, j int) bool { return newer(completed[i], completed[j]) })
	sort.Strings(deleting)
	// The newly published backup is pinned even when the wall clock rolls
	// back. Keep includes it, not just the historical candidates.
	var victims []string
	for i := keep - 1; i < len(completed); i++ {
		victims = append(victims, completed[i].Name)
	}
	result.Pending = append(result.Pending, deleting...)
	result.Pending = append(result.Pending, victims...)
	for len(result.Pending) > 0 {
		if ctx.Err() != nil {
			return retentionError(ctx)
		}
		name := result.Pending[0]
		if strings.HasPrefix(name, "backup-") {
			// Revalidate immediately before quarantine, including strict
			// metadata and the full allowlisted tree.
			d, err := root.OpenDir(name)
			if err != nil {
				return retentionError(ctx)
			}
			err = d.CheckLayout(true)
			if err == nil {
				_, err = ReadMetadata(d)
			}
			d.Close()
			if err != nil {
				return retentionError(ctx)
			}
			name, err = deps.quarantine(root, name)
			if err != nil {
				return retentionError(ctx)
			}
			result.Pending[0] = name
		}
		if err := cleanup(ctx, root, name, result, deps); err != nil {
			return retentionError(ctx)
		}
		result.Pending = result.Pending[1:]
	}
	if ctx.Err() != nil {
		return retentionError(ctx)
	}
	result.RetentionPending = false
	return nil
}

func cleanup(ctx context.Context, root *storage.Root, name string, result *Result, deps dependencies) error {
	d, err := root.OpenDir(name)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := validateDeleting(d); err != nil {
		return err
	}
	// This barrier is mandatory even for a recovered deleting entry: an
	// earlier process may have stopped immediately after the rename.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := deps.syncRoot(root); err != nil {
		return err
	}
	for _, file := range []string{"snapshot.db", "metadata.json", "SHA256SUMS"} {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := deps.unlink(d, file); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := deps.syncDir(d); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := deps.removeDir(root, name); err != nil {
		return err
	}
	result.Removed = append(result.Removed, "backup-"+strings.TrimPrefix(name, "deleting-"))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := deps.syncRoot(root); err != nil {
		return err
	}
	return ctx.Err()
}

func validateDeleting(d *storage.Dir) error {
	files, err := d.FileNames()
	if err != nil {
		return err
	}
	// A full tree must still have valid metadata. Once deletion has removed
	// files, only the remaining private regular allowlist can be validated.
	if len(files) == 3 {
		_, err = ReadMetadata(d)
	}
	return err
}
