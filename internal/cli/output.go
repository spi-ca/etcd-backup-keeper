package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/backup"
	"etcd-backup-keeper/internal/check"
)

type envelope struct {
	Command string        `json:"command,omitempty"`
	OK      bool          `json:"ok"`
	Data    any           `json:"data,omitempty"`
	Error   *apperr.Error `json:"error,omitempty"`
}

func failure(opts options, err error, stdout, stderr io.Writer) int {
	return failureData(opts, err, nil, stdout, stderr)
}

func failureData(opts options, err error, data any, stdout, stderr io.Writer) int {
	safe := apperr.Safe(err)
	var writeErr error
	if opts.json {
		writeErr = json.NewEncoder(stdout).Encode(envelope{Command: opts.command, OK: false, Data: data, Error: safe})
	} else {
		_, writeErr = fmt.Fprintf(stderr, "error [%s]: %s\n", safe.Code, safe.Message)
		if writeErr == nil {
			if hint := recoveryHint(safe.Code); hint != "" {
				line := "hint: " + hint + "\n"
				var n int
				n, writeErr = io.WriteString(stderr, line)
				if writeErr == nil && n != len(line) {
					writeErr = io.ErrShortWrite
				}
			}
		}
	}
	if writeErr != nil {
		return 1
	}
	return failureExitCode(err)
}

// Recovery text is static and allowlisted by code, never derived from error
// messages, arguments, configuration values or external diagnostics.
func recoveryHint(code string) string {
	switch code {
	case "usage":
		return "Use --help to check supported commands and options."
	case "config_read":
		return "Check that the intended configuration file exists and is readable by the execution user."
	case "config_invalid":
		return "Review the configuration against config.example.toml and the configuration contract in docs/operations.md."
	case "credentials_invalid":
		return "Check credential provisioning, ownership and private-file requirements for the execution user."
	case "tls_invalid":
		return "Check CA trust, certificate validity and the client certificate/key pair; keep TLS verification enabled."
	case "storage_locked":
		return "Wait for the active operation to finish; do not remove or replace .lock."
	case "storage_unsafe":
		return "Check backup storage ownership and private-file requirements; preserve existing files for investigation."
	case "storage_unsupported":
		return "Check snapshot storage-version compatibility; preserve the snapshot and existing backups for investigation."
	case "no_backups":
		return "Inspect status and confirm the intended backup storage; verification requires a completed local backup."
	case "backup_not_found":
		return "Use status to find the exact completed backup name for --backup."
	case "snapshot_invalid", "metadata_invalid":
		return "Preserve the affected files and investigate the verification failure; status alone does not establish snapshot integrity."
	case "space_insufficient":
		return "Check free bytes and inodes on the backup volume, including incomplete and pending-deletion items; preserve existing backups."
	case "remote_unavailable", "remote_invalid":
		return "Check endpoint reachability, server health and TLS/authentication provisioning before another attempt."
	case "server_unsupported":
		return "Check server compatibility; only stable etcd 3.6.x and 3.7.x servers are supported."
	case "cluster_mismatch":
		return "Check the intended cluster and configured cluster pin; preserve existing backups and staging."
	case "backup_incomplete":
		return "Inspect status and backup-volume health; preserve incomplete staging for investigation."
	case "durability_uncertain":
		return "Investigate backup-volume sync failures; status cannot establish durability or snapshot integrity."
	case "retention_incomplete":
		return "Inspect status and pending deletion items; investigate cleanup failures before scheduling another backup."
	case "timeout":
		return "Inspect status and preserved staging, then review operation duration and timeout limits before another attempt."
	case "canceled":
		return "Confirm the operation has exited, then inspect status and preserved staging before deciding next steps."
	default:
		return ""
	}
}

func publishedFailureSummary(code string) string {
	switch code {
	case "durability_uncertain":
		return "root durability is uncertain; retention was not run. Inspect status and storage health."
	case "retention_incomplete":
		return "retention cleanup is incomplete. Inspect status before further action."
	default:
		// A named result proves publication, not which stage was interrupted or
		// whether the root durability barrier completed.
		return "operation incomplete. Inspect status before further action."
	}
}

func failureExitCode(err error) int {
	switch apperr.Safe(err).Code {
	case "usage", "config_read", "config_invalid", "credentials_invalid", "tls_invalid":
		return 2
	case "canceled":
		return 130
	case "timeout":
		return 124
	default:
		return 1
	}
}

// progressOutput records the first sink failure and then stops writing. It
// never feeds that failure back into the engine's publication protocol.
type progressOutput struct {
	writer io.Writer
	err    error
}

func (p *progressOutput) write(line string) {
	if p.err != nil {
		return
	}
	var n int
	n, p.err = io.WriteString(p.writer, line)
	if p.err == nil && n != len(line) {
		p.err = io.ErrShortWrite
	}
}

func (p *progressOutput) observe(event backup.Progress) {
	var stage string
	switch event.Stage {
	case backup.ProbeCandidate:
		stage = "candidate_probe"
	case backup.CandidateUnavailable:
		stage = "candidate_unavailable"
	case backup.AttemptSnapshot:
		stage = "snapshot_attempt"
	case backup.AttemptFailed:
		stage = "attempt_failed"
	case backup.Failover:
		stage = "failover"
	case backup.Receiving, backup.Received:
		stage = "snapshot_receiving"
	case backup.ChecksumSnapshot:
		stage = "checksum"
	case backup.InspectSnapshot:
		stage = "inspection"
	case backup.PublishSnapshot:
		stage = "publication"
	case backup.SyncRoot:
		stage = "root_sync"
	case backup.Retention:
		stage = "retention"
	case backup.Complete:
		stage = "complete"
	default:
		return
	}
	line := "stage=" + stage
	if event.Candidate > 0 {
		line += fmt.Sprintf(" candidate=%d/%d", event.Candidate, event.Total)
	}
	if event.Stage == backup.Receiving || event.Stage == backup.Received {
		line += fmt.Sprintf(" bytes=%d", event.Bytes)
		if event.Stage == backup.Received {
			line += " final=true"
		}
	}
	line += fmt.Sprintf(" elapsed=%s\n", event.Elapsed.Truncate(time.Millisecond))
	p.write(line)
}

// writeHumanStatus uses one supplied instant for a consistent, testable age.
// Human formatting must not change the machine-readable StatusResult.
func writeHumanStatus(w io.Writer, result backup.StatusResult, now time.Time) error {
	var out strings.Builder
	if !result.Initialized {
		out.WriteString("Backup storage is uninitialized; no completed local backups.\n")
	}
	fmt.Fprintf(&out, "Local backups: %d completed, %d inprogress, %d deleting.\n", len(result.Completed), len(result.InProgress), len(result.Deleting))
	fmt.Fprintf(&out, "Completed snapshot bytes: %s (%d bytes).\n", humanBytes(result.TotalBytes), result.TotalBytes)
	if result.LatestSuccess == nil {
		out.WriteString("Latest completed: none.\n")
	} else {
		latest := result.LatestSuccess
		fmt.Fprintf(&out, "Latest completed: %s completed=%s (%s).\n", latest.Name, latest.Metadata.CompletedAt.UTC().Format(time.RFC3339Nano), completionAge(latest.Metadata.CompletedAt, now))
	}
	for _, entry := range result.Completed {
		fmt.Fprintf(&out, "%s revision=%d snapshot=%s completed=%s\n", entry.Name, entry.Metadata.Revision, humanBytes(entry.Metadata.Bytes), entry.Metadata.CompletedAt.UTC().Format(time.RFC3339Nano))
	}
	if len(result.InProgress) > 0 || len(result.Deleting) > 0 {
		out.WriteString("ATTENTION: incomplete or pending-deletion items require investigation.\n")
	}
	for _, name := range result.InProgress {
		fmt.Fprintf(&out, "  inprogress (incomplete): %s\n", name)
	}
	for _, name := range result.Deleting {
		fmt.Fprintf(&out, "  deleting (pending deletion): %s\n", name)
	}
	out.WriteString("Integrity not checked by status; run verify.\n")
	text := out.String()
	n, err := io.WriteString(w, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	return err
}

func humanBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		value /= 1024
		if value < 1024 || unit == "EiB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return "" // All int64 sizes fit within the IEC units above.
}

func completionAge(completed, now time.Time) string {
	if completed.After(now) {
		return "future timestamp; check clock skew"
	}
	age := now.Sub(completed).Truncate(time.Second)
	if age >= 24*time.Hour {
		return fmt.Sprintf("%dd %s ago", age/(24*time.Hour), age%(24*time.Hour))
	}
	return age.String() + " ago"
}

func writeHumanCheck(w io.Writer, result check.Result) error {
	var out strings.Builder
	out.WriteString("Limited read-only check; unverified items are not guarantees.\n")
	for _, row := range result.Rows {
		fmt.Fprintf(&out, "[%s] %s", row.State, row.Label)
		if row.Candidate > 0 {
			fmt.Fprintf(&out, " candidate=%d", row.Candidate)
		}
		fmt.Fprintf(&out, ": %s\n", row.Message)
	}
	text := out.String()
	n, err := io.WriteString(w, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	return err
}

func commandUsage(command string) string {
	switch command {
	case "check":
		return `Usage:
  etcd-backup-keeper check [--config PATH] [--json] [--connect]

Limited read-only checks: strict configuration, explicit TLS/password material
and storage path/permissions. Default is strictly offline, but unlike status
and verify it reads configured credential files. Never creates storage or .lock.
Not a full backup preflight or writability guarantee; no snapshots are requested.

Options:
  --config PATH  TOML configuration (default: /etc/etcd-backup-keeper/config.toml)
  --json         Single JSON result on stdout (default: human-readable)
  --connect      Opt in to bounded Status probes of every configured candidate
                 (default: off); any unavailable candidate fails the command
  --help, -h     Show this help without loading configuration or connecting

Examples (after preparing configuration and credential files):
  etcd-backup-keeper check
  etcd-backup-keeper check --connect --json

Rows are confirmed, unverified or failed. Exit 0 means no observed failures,
not readiness. Missing final storage root is unverified, not created. Locking,
writability, fsync, no-replace publication, durability, snapshot permissions,
previous backup cluster pins, integrity and restorability remain unverified.
--connect checks Status identity, stable etcd 3.6.x/3.7.x and cluster coherence/configured pin,
not snapshot authorization. Failures retain available rows in JSON data.
` + operationExitUsage
	case "backup":
		return `Usage:
  etcd-backup-keeper backup [--config PATH] [--json] [--verbose]

Publishes a verified local snapshot, then applies configured keep and retries
pending deletion. Publication may succeed even if later sync or cleanup fails;
inspect status before retrying. Local verification is not a restoration guarantee.

Options:
  --config PATH  TOML configuration (default: /etc/etcd-backup-keeper/config.toml)
  --json         Single JSON result on stdout (default: human-readable)
  --verbose      Safe stage/byte diagnostics on stderr (default: off; no ETA)
  --help, -h     Show this help without loading configuration or connecting

Examples (after preparing configuration, credentials and storage):
  etcd-backup-keeper backup --verbose
  etcd-backup-keeper backup --config /etc/etcd-backup-keeper/config.toml --json

Success means publication, root sync and retention completed. Errors go to
stderr, or to stdout in the JSON envelope (which may include a published backup).
` + operationExitUsage
	case "status":
		return `Usage:
  etcd-backup-keeper status [--config PATH] [--json]

Reads local inventory and metadata offline; does not connect to etcd or read
credential files. Shows completed/inprogress/deleting counts, completed snapshot
bytes (not whole-volume disk usage), latest completion UTC time and age, and
incomplete/pending-deletion items. Integrity not checked by status; run verify.
This is not a persistent record of attempts or verification results.

Options:
  --config PATH  TOML configuration (default: /etc/etcd-backup-keeper/config.toml)
  --json         Single JSON result on stdout (default: human-readable)
  --help, -h     Show this help without loading configuration or connecting

Examples:
  etcd-backup-keeper status
  etcd-backup-keeper status --config /etc/etcd-backup-keeper/config.toml --json

Uninitialized or empty storage is a successful inventory, not an integrity check.
Results go to stdout; errors go to stderr, or to stdout in the JSON envelope.
` + operationExitUsage
	case "verify":
		return `Usage:
  etcd-backup-keeper verify [--config PATH] [--json] [--backup NAME]

Checks ALL completed backups by default, not just the latest, or one exact
--backup name. Checks local integrity and snapshot inspection offline without
connecting to etcd or reading credential files. Inprogress/deleting items are
not verified. Local verification is not a restoration guarantee.

Options:
  --config PATH  TOML configuration (default: /etc/etcd-backup-keeper/config.toml)
  --json         Single JSON result on stdout (default: human-readable)
  --backup NAME  Exact completed backup directory name from status, not a path
                 (default: ALL completed backups)
  --help, -h     Show this help without loading configuration or connecting

Examples (replace NAME with an exact completed name from status):
  etcd-backup-keeper verify --json
  etcd-backup-keeper verify --backup NAME

No completed backups, a missing selected backup, or a failed check is an error.
Results go to stdout; errors go to stderr, or to stdout in the JSON envelope.
` + operationExitUsage
	case "version":
		return `Usage:
  etcd-backup-keeper version [--json] [--config PATH]

Prints the build version without loading configuration or connecting to etcd.

Options:
  --json         Single JSON result on stdout (default: human-readable)
  --config PATH  Accepted for common-flag compatibility; not read by version
  --help, -h     Show this help without loading configuration or connecting

Examples:
  etcd-backup-keeper version
  etcd-backup-keeper version --json

Exit codes: 0 success; 1 output failure; 2 usage error;
            124 timeout; 130 canceled.
Usage errors go to stderr, or to stdout in the JSON envelope.
`
	default:
		return usage
	}
}

const operationExitUsage = `Exit codes: 0 success; 1 operation/output failure; 2 usage/configuration error;
            124 timeout; 130 canceled.
`

const usage = `Usage:
  etcd-backup-keeper [--config PATH] [--json] COMMAND [OPTIONS]

Commands:
  backup  [--config PATH] [--json] [--verbose]
  status  [--config PATH] [--json]
  verify  [--config PATH] [--json] [--backup NAME]
  check   [--config PATH] [--json] [--connect]
  version [--json]

Default config: /etc/etcd-backup-keeper/config.toml
backup publishes a verified snapshot, then applies keep and retries pending deletion.
status reads local inventory and metadata without hashing snapshots.
verify checks ALL completed backups by default, or one exact --backup name.
check validates configuration/material/path offline; --connect opts in to Status.
check is limited: unverified items remain, not full backup readiness.
Local verification is not a restoration guarantee.
Passwords are accepted only through a private password_file in TOML.
Use COMMAND --help (or -h) for exact options, defaults and examples.
--config and --json are accepted before or after the command.
Help and version do not load configuration or connect to etcd.
--json prints one envelope on stdout, including help in data.usage.
`
