// Package check performs limited read-only checks, offline unless explicitly
// asked to connect. No check invokes snapshot, storage.Open, or local helpers.
package check

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
	"etcd-backup-keeper/internal/storage"
)

type State string

const (
	Confirmed  State = "confirmed"
	Unverified State = "unverified"
	Failed     State = "failed"
)

// Row labels/messages are static; Candidate is a one-based index, never a URL.
type Row struct {
	Label     string `json:"label"`
	State     State  `json:"state"`
	Message   string `json:"message"`
	Candidate int    `json:"candidate,omitempty"`
}

type Result struct {
	Connect bool  `json:"connect"`
	Rows    []Row `json:"rows"`
}

type status struct {
	header              bool
	clusterID, memberID uint64
	version             string
}

type connection interface {
	Status(context.Context) (status, error)
	Close()
}

type factory func(context.Context, context.Context, config.Config, int) (connection, error)

// Run returns all observed rows even on failure. Exit success means no observed
// failures, not full backup readiness. Every unavailable candidate is a failure
// with --connect, even if another candidate succeeds.
func Run(ctx context.Context, cfg config.Config, connect bool) (Result, error) {
	return run(ctx, cfg, connect, newConnection)
}

func run(ctx context.Context, cfg config.Config, connect bool, open factory) (Result, error) {
	result := Result{Connect: connect, Rows: make([]Row, 0)}
	add := func(label string, state State, message string, candidate int) {
		result.Rows = append(result.Rows, Row{Label: label, State: state, Message: message, Candidate: candidate})
	}
	// Always include the assurance boundary, including when an earlier check
	// fails. These operations are deliberately never attempted by this command.
	deferLimits := func() {
		add("backup_operations", Unverified, "Writability, locking, fsync, no-replace publication and durability are not tested.", 0)
		add("snapshot_permissions", Unverified, "Status does not establish permission to request a snapshot.", 0)
		add("previous_cluster_pin", Unverified, "Previous completed backup cluster pins are not inspected.", 0)
		add("snapshot_integrity", Unverified, "Snapshot contents, integrity and restorability are not checked.", 0)
	}
	if err := cfg.Validate(); err != nil {
		add("configuration", Failed, "Configuration is invalid.", 0)
		deferLimits()
		return result, err
	}
	add("configuration", Confirmed, "Configuration structure is valid.", 0)
	ctx, cancel := cfg.RequestContext(ctx)
	defer cancel()
	var firstErr error
	record := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	// Build only: this reads explicit TLS/password files with the existing
	// bounded readers, without client.New, DNS resolution or dialing.
	_, materialErr := cfg.ClientConfig(ctx, 0)
	if materialErr != nil {
		add("client_material", Failed, "Client configuration or explicit TLS/password material could not be validated.", 0)
		record(materialErr)
	} else {
		add("client_material", Confirmed, "Client configuration and any explicit TLS/password material can be loaded; remote trust and authentication are not tested.", 0)
	}
	if err := ctx.Err(); err != nil {
		add("storage_path", Unverified, "Storage path check was not completed.", 0)
		record(err)
	} else if exists, err := storage.CheckPath(cfg.BackupDir); err != nil {
		add("storage_path", Failed, "Storage path is inaccessible or has unsafe directory ownership, permissions or components.", 0)
		record(err)
	} else if !exists {
		add("storage_path", Unverified, "Final backup root is absent; ancestors were checked but root creation and permissions are unverified.", 0)
	} else {
		add("storage_path", Confirmed, "Existing backup root path is no-symlink, owned by the execution user and private; contents are not inspected.", 0)
	}
	var cluster uint64
	var expected uint64
	if cfg.ExpectedClusterID != "" {
		expected, _ = strconv.ParseUint(strings.TrimPrefix(cfg.ExpectedClusterID, "0x"), 16, 64)
	}
	for index := range cfg.Endpoints {
		if !connect {
			add("candidate_status", Unverified, "Remote candidate was not contacted; use --connect to request Status probes.", index+1)
			continue
		}
		if materialErr != nil || ctx.Err() != nil {
			add("candidate_status", Unverified, "Remote candidate was not contacted because material validation failed or the operation ended.", index+1)
			continue
		}
		info, err := probe(ctx, cfg, index, open)
		if err != nil {
			add("candidate_status", Failed, "Remote candidate Status could not be obtained.", index+1)
			record(err)
			continue
		}
		if !info.header || info.clusterID == 0 || info.memberID == 0 {
			add("candidate_status", Failed, "Remote candidate Status has an invalid identity header.", index+1)
			record(apperr.New("remote_invalid", "candidate Status has an invalid identity header"))
			continue
		}
		if cluster == 0 {
			cluster = info.clusterID
		}
		if info.clusterID != cluster || (expected != 0 && info.clusterID != expected) {
			add("candidate_status", Failed, "Remote candidate cluster identity does not match the configured pin or other reachable candidates.", index+1)
			record(apperr.New("cluster_mismatch", "candidate cluster identities do not match"))
			continue
		}
		if !supportedVersion.MatchString(info.version) {
			add("candidate_status", Failed, "Remote candidate server version is not supported; etcd 3.7 is required.", index+1)
			record(apperr.New("server_unsupported", "only etcd 3.7 servers are supported"))
			continue
		}
		add("candidate_status", Confirmed, "Status returned a valid identity and supported etcd 3.7 version, consistent with the configured pin and other observed candidate identities.", index+1)
	}
	deferLimits()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, firstErr
}

var supportedVersion = regexp.MustCompile(`^3\.7\.[0-9]+$`)

func probe(ctx context.Context, cfg config.Config, index int, open factory) (status, error) {
	dialCtx, cancel := cfg.DialContext(ctx)
	defer cancel()
	client, err := open(ctx, dialCtx, cfg, index)
	if client != nil {
		defer client.Close()
	}
	if err == nil && client != nil {
		var info status
		info, err = client.Status(dialCtx)
		if err == nil && dialCtx.Err() == nil {
			return info, nil
		}
	}
	if err := dialCtx.Err(); err != nil {
		return status{}, err
	}
	if errors.Is(err, context.Canceled) {
		return status{}, context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status{}, context.DeadlineExceeded
	}
	return status{}, apperr.New("remote_unavailable", "candidate Status is unavailable")
}
