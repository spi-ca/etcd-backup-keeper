// Package backup creates verified, atomically published local etcd snapshots.
package backup

import (
	"context"
	"errors"
	"os"
	"time"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
	"etcd-backup-keeper/internal/storage"
)

type Result struct {
	Name             string   `json:"name"`
	Metadata         Metadata `json:"metadata"`
	RetentionPending bool     `json:"retention_pending"`
	Removed          []string `json:"removed"`
	// Pending names need cleanup or a durability barrier retry. A name may
	// already be absent when the final root sync failed.
	Pending []string `json:"pending"`
}

type dependencies struct {
	observe     func(Progress)
	progressNow func() time.Time
	progress    *progressReporter
	connect     func(context.Context, context.Context, config.Config, int) (connection, error)
	inspect     func(context.Context, *os.File) (Inspection, error)
	write       func(*os.File, []byte) (int, error)
	syncFile    func(*os.File) error
	syncDir     func(*storage.Dir) error
	publish     func(*storage.Root, string, string) error
	syncRoot    func(*storage.Root) error
	quarantine  func(*storage.Root, string) (string, error)
	unlink      func(*storage.Dir, string) error
	removeDir   func(*storage.Root, string) error
	now         func() time.Time
}

func defaults() dependencies {
	return dependencies{
		connect:    connect,
		inspect:    Inspect,
		write:      func(f *os.File, p []byte) (int, error) { return f.Write(p) },
		syncFile:   func(f *os.File) error { return f.Sync() },
		syncDir:    func(d *storage.Dir) error { return d.Sync() },
		publish:    func(r *storage.Root, a, b string) error { return r.Publish(a, b) },
		syncRoot:   func(r *storage.Root) error { return r.Sync() },
		quarantine: func(r *storage.Root, name string) (string, error) { return r.Quarantine(name) },
		unlink:     func(d *storage.Dir, name string) error { return d.Unlink(name) },
		removeDir:  func(r *storage.Root, name string) error { return r.RemoveEmpty(name) },
		now:        time.Now,
	}
}

// Run bounds all candidates, streaming, inspection and publication by the
// configured total timeout, including retention under the same exclusive lock.
func Run(ctx context.Context, cfg config.Config, toolVersion string) (Result, error) {
	return run(ctx, cfg, toolVersion, defaults())
}

// RunWithProgress is Run with an optional synchronous observer. Observers must
// return promptly and must not panic. They cannot return an error or change the
// publication/retention protocol; output failures belong at the caller boundary.
func RunWithProgress(ctx context.Context, cfg config.Config, toolVersion string, observe func(Progress)) (Result, error) {
	deps := defaults()
	deps.observe = observe
	return run(ctx, cfg, toolVersion, deps)
}

func run(parent context.Context, cfg config.Config, toolVersion string, deps dependencies) (Result, error) {
	var result Result
	if deps.observe != nil {
		now := deps.progressNow
		if now == nil {
			now = time.Now
		}
		deps.progress = &progressReporter{observe: deps.observe, now: now, started: now()}
	}
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := cfg.RequestContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := storage.Open(cfg.BackupDir)
	if err != nil {
		return result, err
	}
	defer root.Close()
	pin, err := clusterPin(root, cfg.ExpectedClusterID)
	if err != nil {
		return result, err
	}
	// Probe ALL reachable candidates before streaming any. An unavailable
	// candidate has unknown identity on a first run; it cannot certify the pin.
	var candidates []int
	for i := range cfg.Endpoints {
		deps.emit(Progress{Stage: ProbeCandidate, Candidate: i + 1, Total: len(cfg.Endpoints)})
		p, err := probeCandidate(ctx, cfg, i, deps)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err != nil {
			if isRemote(err) {
				deps.emit(Progress{Stage: CandidateUnavailable, Candidate: i + 1, Total: len(cfg.Endpoints)})
				continue
			}
			return result, err
		}
		if err = checkProbe(p, pin); err != nil {
			return result, err
		}
		pin = p.clusterID
		candidates = append(candidates, i)
	}
	for position, index := range candidates {
		if position > 0 {
			deps.emit(Progress{Stage: Failover, Candidate: index + 1, Total: len(cfg.Endpoints)})
		}
		deps.emit(Progress{Stage: AttemptSnapshot, Candidate: index + 1, Total: len(cfg.Endpoints)})
		result, err = attempt(ctx, cfg, index, pin, root, toolVersion, deps)
		if err != nil {
			deps.emit(Progress{Stage: AttemptFailed, Candidate: index + 1, Total: len(cfg.Endpoints)})
		} else {
			deps.emit(Progress{Stage: Complete})
		}
		if ctx.Err() != nil && isRemote(err) {
			return Result{}, incompleteContext(ctx)
		}
		if err == nil || !isRemote(err) {
			return result, err
		}
	}
	return Result{}, apperr.New("remote_unavailable", "no candidate could complete a snapshot; any incomplete staging is preserved")
}

func isRemote(err error) bool { var remote remoteFailure; return errors.As(err, &remote) }
func incompleteContext(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return apperr.New("timeout", "operation timed out; any incomplete staging is preserved")
	}
	return apperr.New("canceled", "operation canceled; any incomplete staging is preserved")
}
func localError() error {
	return apperr.New("backup_incomplete", "local backup operation failed; incomplete staging preserved and existing backups unchanged")
}
func checkProbe(p probe, pin uint64) error {
	if p.clusterID == 0 || p.memberID == 0 || (pin != 0 && pin != p.clusterID) {
		return identityError()
	}
	if !supportedServer(p.serverVersion) {
		return apperr.New("server_unsupported", "only etcd 3.7 servers are supported")
	}
	if p.dbBytes <= 0 {
		return apperr.New("remote_invalid", "candidate reported an invalid database size")
	}
	return nil
}
func probeCandidate(ctx context.Context, cfg config.Config, index int, deps dependencies) (probe, error) {
	dialCtx, cancel := cfg.DialContext(ctx)
	defer cancel()
	conn, err := deps.connect(ctx, dialCtx, cfg, index)
	if err != nil {
		return probe{}, err
	}
	defer conn.Close()
	return conn.Probe(dialCtx)
}

func preflight(root *storage.Root, minimum uint64, dbBytes int64) error {
	if dbBytes <= 0 {
		return localError()
	}
	// Reserve database size + 10% + 64 MiB for trailer/metadata/page growth,
	// in addition to the operator's free-space floor. Never wrap arithmetic.
	size := uint64(dbBytes)
	extra := size/10 + 64*1024*1024
	if size > ^uint64(0)-extra || minimum > ^uint64(0)-(size+extra) {
		return apperr.New("space_insufficient", "free-space requirement exceeds representable capacity")
	}
	available, err := root.Available()
	if err != nil {
		return err
	}
	if available < minimum+size+extra {
		return apperr.New("space_insufficient", "insufficient available disk space for snapshot and configured reserve")
	}
	return nil
}
