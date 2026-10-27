package backup

import (
	"context"
	"io"
	"os"
	"regexp"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
)

type probe struct {
	clusterID, memberID uint64
	dbBytes             int64
	serverVersion       string
}
type stream struct {
	body                io.ReadCloser
	clusterID, memberID uint64
	storageVersion      string
}
type connection interface {
	Probe(context.Context) (probe, error)
	Snapshot(context.Context) (stream, error)
	Close()
}
type etcdConnection struct {
	client   *clientv3.Client
	endpoint string
}

// remoteFailure is deliberately opaque: raw client diagnostics are not retained.
type remoteFailure struct{}

func (remoteFailure) Error() string { return "remote candidate unavailable" }

func connect(ctx, dialCtx context.Context, cfg config.Config, index int) (connection, error) {
	clientConfig, err := cfg.ClientConfig(ctx, index)
	if err != nil {
		return nil, err
	}
	// New authenticates synchronously in 3.7.3. Keep Context at total lifetime
	// but explicitly retain DialTimeout, which bounds getToken during New.
	// A probe's short context must not become the client's lifetime context.
	if err := dialCtx.Err(); err != nil {
		return nil, remoteFailure{}
	}
	deadline, ok := dialCtx.Deadline()
	if !ok {
		return nil, remoteFailure{}
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, remoteFailure{}
	}
	clientConfig.DialTimeout = remaining
	client, err := clientv3.New(clientConfig)
	if err != nil {
		return nil, remoteFailure{}
	}
	return &etcdConnection{client: client, endpoint: cfg.Endpoints[index]}, nil
}
func (c *etcdConnection) Close() { c.client.Close() }
func (c *etcdConnection) Probe(ctx context.Context) (probe, error) {
	status, err := c.client.Status(ctx, c.endpoint)
	if err != nil {
		return probe{}, remoteFailure{}
	}
	if status == nil || status.Header == nil {
		return probe{}, identityError()
	}
	return probe{clusterID: status.Header.ClusterId, memberID: status.Header.MemberId, dbBytes: status.DbSize, serverVersion: status.Version}, nil
}
func (c *etcdConnection) Snapshot(ctx context.Context) (stream, error) {
	response, err := c.client.SnapshotWithVersion(ctx)
	if err != nil {
		return stream{}, remoteFailure{}
	}
	if response == nil {
		return stream{}, identityError()
	}
	if response.Snapshot == nil {
		if response.Snapshot != nil {
			response.Snapshot.Close()
		}
		return stream{}, identityError()
	}
	result := stream{body: response.Snapshot, storageVersion: response.Version}
	// etcd 3.7.3 does not populate SnapshotResponse.Header. Check an identity
	// header if a server supplies one; otherwise publication relies on the
	// selected endpoint's pre/post Status checks and trusted TLS/endpoint.
	if response.Header != nil {
		if response.Header.ClusterId == 0 || response.Header.MemberId == 0 {
			response.Snapshot.Close()
			return stream{}, identityError()
		}
		result.clusterID = response.Header.ClusterId
		result.memberID = response.Header.MemberId
	}
	return result, nil
}

var serverVersion = regexp.MustCompile(`^3\.7\.[0-9]+$`)

func supportedServer(version string) bool { return serverVersion.MatchString(version) }
func identityError() error {
	return apperr.New("cluster_mismatch", "candidate or snapshot identity does not match the cluster pin; no backup published and any incomplete staging is preserved")
}

func copySnapshot(ctx context.Context, dst *os.File, src io.Reader, deps dependencies) error {
	buffer := make([]byte, 128*1024)
	emptyReads := 0
	var received int64
	var lastProgress time.Time
	if deps.progress != nil {
		lastProgress = deps.progress.now()
		deps.emit(Progress{Stage: Receiving})
		// Report the final received count even for a small or failed stream.
		defer func() { deps.emit(Progress{Stage: Received, Bytes: received}) }()
	}
	for {
		if ctx.Err() != nil {
			return incompleteContext(ctx)
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			received += int64(n)
			written, writeErr := deps.write(dst, buffer[:n])
			if writeErr != nil || written != n {
				return localError()
			}
			emptyReads = 0
			if deps.progress != nil {
				now := deps.progress.now()
				if now.Sub(lastProgress) >= time.Second {
					deps.emit(Progress{Stage: Receiving, Bytes: received})
					lastProgress = now
				}
			}
		} else {
			emptyReads++
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil || emptyReads >= 100 {
			if ctx.Err() != nil {
				return incompleteContext(ctx)
			}
			return remoteFailure{}
		}
	}
}
