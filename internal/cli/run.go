// Package cli provides a testable CLI boundary without creating network clients.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/backup"
	"etcd-backup-keeper/internal/check"
	"etcd-backup-keeper/internal/config"
)

// Run returns a process exit code. Signals are converted into context
// cancellation by main, not hidden in command implementations.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	return run(ctx, args, stdout, stderr, version, backup.RunWithProgress)
}

type backupRunner func(context.Context, config.Config, string, func(backup.Progress)) (backup.Result, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, version string, runBackup backupRunner) int {
	return runWithCheck(ctx, args, stdout, stderr, version, runBackup, check.Run)
}

type checkRunner func(context.Context, config.Config, bool) (check.Result, error)

func runWithCheck(ctx context.Context, args []string, stdout, stderr io.Writer, version string, runBackup backupRunner, runCheck checkRunner) int {
	opts, err := parse(args)
	if err != nil {
		opts.json = requestedJSON(args)
		return failure(opts, err, stdout, stderr)
	}
	if opts.help {
		help := commandUsage(opts.command)
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: opts.command, OK: true, Data: struct {
				Usage string `json:"usage"`
			}{help}})
		} else {
			_, err = io.WriteString(stdout, help)
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
	if opts.command == "check" {
		result, checkErr := runCheck(requestCtx, cfg, opts.connect)
		if checkErr != nil {
			if !opts.json {
				if err := writeHumanCheck(stdout, result); err != nil {
					return failureExitCode(checkErr)
				}
			}
			return failureData(opts, checkErr, result, stdout, stderr)
		}
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: opts.command, OK: true, Data: result})
		} else {
			err = writeHumanCheck(stdout, result)
		}
		if err != nil {
			return 1
		}
		return 0
	}
	progress := progressOutput{writer: stderr}
	var observe func(backup.Progress)
	if opts.verbose {
		progress.write("stage=config_validated\n")
		observe = progress.observe
	}
	if opts.command == "backup" {
		result, err := runBackup(requestCtx, cfg, version, observe)
		if err != nil {
			var data any
			if result.Name != "" {
				data = result
				if !opts.json {
					if _, writeErr := fmt.Fprintf(stderr, "Published backup %s; %s\n", result.Name, publishedFailureSummary(apperr.Safe(err).Code)); writeErr != nil && progress.err == nil {
						return 1
					}
				}
			}
			code := failureData(opts, err, data, stdout, stderr)
			if progress.err != nil {
				// A progress sink failure must not hide the engine's error code.
				return failureExitCode(err)
			}
			return code
		}
		if opts.json {
			err = json.NewEncoder(stdout).Encode(envelope{Command: "backup", OK: true, Data: result})
		} else {
			_, err = fmt.Fprintf(stdout, "Backup %s completed (revision %d, %d bytes); retention complete, %d removed.\n", result.Name, result.Metadata.Revision, result.Metadata.Bytes, len(result.Removed))
		}
		if err != nil || progress.err != nil {
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
		} else {
			err = writeHumanStatus(stdout, result, time.Now())
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
