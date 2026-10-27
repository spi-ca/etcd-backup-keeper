package backup

import (
	"context"
	"os"
	"sort"
	"strings"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
	"etcd-backup-keeper/internal/storage"
)

type Entry struct {
	Name     string   `json:"name"`
	Metadata Metadata `json:"metadata"`
}

type StatusResult struct {
	Initialized   bool     `json:"initialized"`
	Completed     []Entry  `json:"completed"`
	InProgress    []string `json:"inprogress"`
	Deleting      []string `json:"deleting"`
	TotalBytes    int64    `json:"total_bytes"`
	LatestSuccess *Entry   `json:"latest_success"`
}

type VerifyResult struct {
	Scope     string  `json:"scope"`
	Verified  []Entry `json:"verified"`
	Assurance string  `json:"assurance"`
}

const verificationAssurance = "Local integrity and inspection checks passed; this is not a restoration guarantee."

// Status is entirely offline. It reads strict metadata and file sizes but does
// not hash snapshot contents. The shared lock spans the complete inventory.
func Status(parent context.Context, cfg config.Config) (StatusResult, error) {
	result := StatusResult{Completed: []Entry{}, InProgress: []string{}, Deleting: []string{}}
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := cfg.RequestContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := storage.OpenExisting(cfg.BackupDir)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer root.Close()
	result.Initialized = true
	names, err := root.Inventory()
	if err != nil {
		return result, err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		switch {
		case strings.HasPrefix(name, "inprogress-"):
			result.InProgress = append(result.InProgress, name)
		case strings.HasPrefix(name, "deleting-"):
			result.Deleting = append(result.Deleting, name)
		default:
			d, err := root.OpenDir(name)
			if err != nil {
				return result, err
			}
			m, err := ReadMetadata(d)
			d.Close()
			if err != nil {
				return result, err
			}
			entry := Entry{Name: name, Metadata: m}
			result.Completed = append(result.Completed, entry)
			if m.Bytes > int64(^uint64(0)>>1)-result.TotalBytes {
				return result, metadataError()
			}
			result.TotalBytes += m.Bytes
			if result.LatestSuccess == nil || newer(entry, *result.LatestSuccess) {
				result.LatestSuccess = &entry
			}
		}
	}
	return result, ctx.Err()
}

// newer orders by successful completion, then name for deterministic ties.
func newer(a, b Entry) bool {
	if !a.Metadata.CompletedAt.Equal(b.Metadata.CompletedAt) {
		return a.Metadata.CompletedAt.After(b.Metadata.CompletedAt)
	}
	return a.Name > b.Name
}

// Verify defaults to ALL completed backups; a selector must be an exact local
// tool-format completed name. No client or credential material is accessed.
func Verify(parent context.Context, cfg config.Config, name string) (VerifyResult, error) {
	return verify(parent, cfg, name, defaults())
}
func verify(parent context.Context, cfg config.Config, name string, deps dependencies) (VerifyResult, error) {
	result := VerifyResult{Scope: "all_completed", Verified: []Entry{}, Assurance: verificationAssurance}
	if name != "" {
		if !storage.ValidName(name) || !strings.HasPrefix(name, "backup-") {
			return result, apperr.New("usage", "--backup requires an exact completed local backup name")
		}
		result.Scope = "named_completed"
	}
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := cfg.RequestContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := storage.OpenExisting(cfg.BackupDir)
	if os.IsNotExist(err) {
		return result, noBackups()
	}
	if err != nil {
		return result, err
	}
	defer root.Close()
	names, err := root.Inventory()
	if err != nil {
		return result, err
	}
	sort.Strings(names)
	for _, candidate := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !strings.HasPrefix(candidate, "backup-") || (name != "" && candidate != name) {
			continue
		}
		d, err := root.OpenDir(candidate)
		if err != nil {
			return result, err
		}
		m, err := verifyDir(ctx, d, deps)
		d.Close()
		if err != nil {
			return result, err
		}
		result.Verified = append(result.Verified, Entry{Name: candidate, Metadata: m})
	}
	if len(result.Verified) == 0 {
		if name != "" {
			return result, apperr.New("backup_not_found", "requested completed backup was not found")
		}
		return result, noBackups()
	}
	return result, ctx.Err()
}
func noBackups() error {
	return apperr.New("no_backups", "no completed local backups are available to verify")
}
func verifyDir(ctx context.Context, d *storage.Dir, deps dependencies) (Metadata, error) {
	m, err := ReadMetadata(d)
	if err != nil {
		return m, err
	}
	f, err := d.OpenFile("snapshot.db", false)
	if err != nil {
		return m, err
	}
	defer f.Close()
	digest, size, err := Checksum(ctx, f)
	if err != nil {
		return m, err
	}
	if digest != m.SHA256 || size != m.Bytes {
		return m, verificationError()
	}
	inspection, err := deps.inspect(ctx, f)
	if ctx.Err() != nil {
		return m, ctx.Err()
	}
	if err != nil || inspection.Revision != m.Revision || inspection.Keys != m.Keys || inspection.DBBytes != m.DBBytes || inspection.StorageVersion != m.StorageVersion {
		return m, verificationError()
	}
	return m, nil
}
