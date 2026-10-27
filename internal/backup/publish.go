package backup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
	"etcd-backup-keeper/internal/etcdcompat"
	"etcd-backup-keeper/internal/storage"
)

func attempt(ctx context.Context, cfg config.Config, index int, pin uint64, root *storage.Root, toolVersion string, deps dependencies) (Result, error) {
	var result Result
	dialCtx, stopProbe := cfg.DialContext(ctx)
	conn, err := deps.connect(ctx, dialCtx, cfg, index)
	if err != nil {
		stopProbe()
		return result, err
	}
	defer conn.Close()
	p, err := conn.Probe(dialCtx)
	stopProbe()
	if err != nil {
		return result, err
	}
	if err = checkProbe(p, pin); err != nil {
		return result, err
	}
	if err = preflight(root, cfg.MinFreeBytes, p.dbBytes); err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, incompleteContext(ctx)
	}
	created := deps.now().UTC()
	name, dir, err := root.CreateStage(created)
	if err != nil {
		return result, localError()
	}
	defer dir.Close()
	snapshotFile, err := dir.OpenFile("snapshot.db", true)
	if err != nil {
		return result, localError()
	}
	defer snapshotFile.Close()
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	snapshot, err := conn.Snapshot(streamCtx)
	if err != nil {
		return result, err
	}
	defer snapshot.body.Close()
	if (snapshot.clusterID != 0 || snapshot.memberID != 0) && (snapshot.clusterID != p.clusterID || snapshot.memberID != p.memberID) {
		return result, identityError()
	}
	if !etcdcompat.Storage(snapshot.storageVersion) {
		return result, apperr.New("storage_unsupported", "snapshot storage version is not supported; incomplete staging preserved")
	}
	if err = copySnapshot(ctx, snapshotFile, snapshot.body, deps); err != nil {
		return result, err
	}
	// End the per-attempt stream before starting CPU/local inspection.
	snapshot.body.Close()
	stopStream()
	postCtx, stopPost := cfg.DialContext(ctx)
	post, err := conn.Probe(postCtx)
	stopPost()
	if err != nil {
		return result, err
	}
	if err = checkProbe(post, pin); err != nil {
		return result, err
	}
	if post.memberID != p.memberID {
		return result, identityError()
	}
	if err = deps.syncFile(snapshotFile); err != nil {
		return result, localError()
	}
	deps.emit(Progress{Stage: ChecksumSnapshot})
	digest, size, err := Checksum(ctx, snapshotFile)
	if err != nil {
		if ctx.Err() != nil {
			return result, incompleteContext(ctx)
		}
		return result, err
	}
	if err = snapshotFile.Close(); err != nil {
		return result, localError()
	}
	// Reopen read-only using the pinned directory for the inherited helper FD.
	readonly, err := dir.OpenFile("snapshot.db", false)
	if err != nil {
		return result, localError()
	}
	deps.emit(Progress{Stage: InspectSnapshot})
	inspection, err := deps.inspect(ctx, readonly)
	closeErr := readonly.Close()
	if err != nil {
		if ctx.Err() != nil {
			return result, incompleteContext(ctx)
		}
		return result, verificationError()
	}
	if closeErr != nil {
		return result, localError()
	}
	if inspection.StorageVersion != snapshot.storageVersion || inspection.DBBytes <= 0 || inspection.DBBytes > size-sha256.Size || inspection.Revision < 0 || inspection.Keys < 0 {
		return result, verificationError()
	}
	m := Metadata{
		FormatVersion: FormatVersion, ToolVersion: toolVersion,
		ServerVersion: p.serverVersion, StorageVersion: inspection.StorageVersion,
		ClusterID: strconv.FormatUint(p.clusterID, 16), MemberID: strconv.FormatUint(p.memberID, 16),
		CreatedAt: created, CompletedAt: deps.now().UTC(),
		Revision: inspection.Revision, Keys: inspection.Keys,
		DBBytes: inspection.DBBytes, Bytes: size, SHA256: digest,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return result, localError()
	}
	data = append(data, '\n')
	// Check our own writer against the strict reader before publication.
	if _, err = ParseMetadata(data); err != nil {
		return result, localError()
	}
	deps.emit(Progress{Stage: PublishSnapshot})
	if err = writeSynced(dir, "metadata.json", data, deps); err != nil {
		return result, err
	}
	if err = writeSynced(dir, "SHA256SUMS", []byte(makeManifest(digest, data)), deps); err != nil {
		return result, err
	}
	if err = deps.syncDir(dir); err != nil {
		return result, localError()
	}
	if ctx.Err() != nil {
		return result, incompleteContext(ctx)
	}
	completed := "backup-" + strings.TrimPrefix(name, "inprogress-")
	if err = deps.publish(root, name, completed); err != nil {
		return result, localError()
	}
	result = Result{Name: completed, Metadata: m, RetentionPending: true, Removed: []string{}, Pending: []string{}}
	// Never roll back a completed rename or run retention after root-sync failure.
	deps.emit(Progress{Stage: SyncRoot})
	if err = deps.syncRoot(root); err != nil {
		return result, apperr.New("durability_uncertain", "completed backup preserved but root sync failed; durability is uncertain and retention was not run; run status")
	}
	if ctx.Err() != nil {
		code := "canceled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		return result, apperr.New(code, "operation ended after publication; completed backup preserved and retention was not run; run status")
	}
	deps.emit(Progress{Stage: Retention})
	if err := retain(ctx, root, cfg.Keep, &result, deps); err != nil {
		return result, err
	}
	return result, nil
}

func writeSynced(dir *storage.Dir, name string, data []byte, deps dependencies) error {
	f, err := dir.OpenFile(name, true)
	if err != nil {
		return localError()
	}
	defer f.Close()
	n, err := deps.write(f, data)
	if err != nil || n != len(data) {
		return localError()
	}
	if err = deps.syncFile(f); err != nil {
		return localError()
	}
	if err = f.Close(); err != nil {
		return localError()
	}
	return nil
}
