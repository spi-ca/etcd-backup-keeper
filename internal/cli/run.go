// Package cli provides a testable CLI boundary without creating network clients.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"etcd-backup-keeper/internal/backup"
	"etcd-backup-keeper/internal/config"
)

// Run returns a process exit code. Signals are converted into context
// cancellation by main, not hidden in command implementations.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	opts, err := parse(args)
	if err != nil {
		opts.json = requestedJSON(args)
		return failure(opts, err, stdout, stderr)
	}
	if opts.help {
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: opts.command, OK: true, Data: struct {
				Usage string `json:"usage"`
			}{usage}})
		} else {
			_, err = io.WriteString(stdout, usage)
		}
		if err != nil {
			return 1
		}
		return 0
	}
	if err := ctx.Err(); err != nil {
		return failure(opts, err, stdout, stderr)
	}
	if opts.command == "version" {
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: "version", OK: true, Data: struct {
				Version string `json:"version"`
			}{version}})
		} else {
			_, err = fmt.Fprintln(stdout, "etcd-backup-keeper", version)
		}
		if err != nil {
			return 1
		}
		return 0
	}
	cfg, err := config.Load(opts.config)
	if err != nil {
		return failure(opts, err, stdout, stderr)
	}
	requestCtx, cancel := cfg.RequestContext(ctx)
	defer cancel()
	if err := requestCtx.Err(); err != nil {
		return failure(opts, err, stdout, stderr)
	}
	if opts.verbose {
		if _, err := fmt.Fprintln(stderr, "stage=config_validated"); err != nil {
			return 1
		}
	}
	if opts.command == "backup" {
		result, err := backup.Run(requestCtx, cfg, version)
		if err != nil {
			if result.Name != "" {
				if !opts.json {
					if _, writeErr := fmt.Fprintf(stderr, "Published backup %s; retention pending. Run status before retrying a backup.\n", result.Name); writeErr != nil {
						return 1
					}
				}
				return failureData(opts, err, result, stdout, stderr)
			}
			return failure(opts, err, stdout, stderr)
		}
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: "backup", OK: true, Data: result})
		} else {
			_, err = fmt.Fprintf(stdout, "Backup %s completed (revision %d, %d bytes); retention complete, %d removed.\n", result.Name, result.Metadata.Revision, result.Metadata.Bytes, len(result.Removed))
		}
		if err != nil {
			return 1
		}
		return 0
	}
	// Local commands never construct a client or read credential files.
	if opts.command == "status" {
		result, statusErr := backup.Status(requestCtx, cfg)
		if statusErr != nil {
			return failure(opts, statusErr, stdout, stderr)
		}
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: opts.command, OK: true, Data: result})
		} else if !result.Initialized {
			_, err = fmt.Fprintln(stdout, "Backup storage is uninitialized; no completed local backups.")
		} else {
			_, err = fmt.Fprintf(stdout, "Local backups: %d completed, %d inprogress, %d deleting; %d bytes.\n", len(result.Completed), len(result.InProgress), len(result.Deleting), result.TotalBytes)
			if err == nil && result.LatestSuccess != nil {
				_, err = fmt.Fprintf(stdout, "Latest success: %s (revision %d).\n", result.LatestSuccess.Name, result.LatestSuccess.Metadata.Revision)
			}
			for _, entry := range result.Completed {
				if err != nil {
					break
				}
				_, err = fmt.Fprintf(stdout, "%s revision=%d bytes=%d completed=%s\n", entry.Name, entry.Metadata.Revision, entry.Metadata.Bytes, entry.Metadata.CompletedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
			}
			for _, name := range append(result.InProgress, result.Deleting...) {
				if err != nil {
					break
				}
				_, err = fmt.Fprintln(stdout, name)
			}
		}
	} else {
		result, verifyErr := backup.Verify(requestCtx, cfg, opts.backup)
		if verifyErr != nil {
			return failure(opts, verifyErr, stdout, stderr)
		}
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: opts.command, OK: true, Data: result})
		} else {
			_, err = fmt.Fprintf(stdout, "Verified %d completed local backup(s), scope=%s. %s\n", len(result.Verified), result.Scope, result.Assurance)
			for _, entry := range result.Verified {
				if err != nil {
					break
				}
				_, err = fmt.Fprintln(stdout, entry.Name)
			}
		}
	}
	if err != nil {
		return 1
	}
	return 0
}
