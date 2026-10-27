package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"etcd-backup-keeper/internal/apperr"
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
	}
	if writeErr != nil {
		return 1
	}
	switch safe.Code {
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

const usage = `Usage:
  etcd-backup-keeper [--config PATH] [--json] COMMAND [OPTIONS]

Commands:
  backup  [--config PATH] [--json] [--verbose]
  status  [--config PATH] [--json]
  verify  [--config PATH] [--json] [--backup NAME]
  version [--json]

Default config: /etc/etcd-backup-keeper/config.toml
backup publishes a verified snapshot, then applies keep and retries pending deletion.
status reads local inventory and metadata without hashing snapshots.
verify checks ALL completed backups by default, or one exact --backup name.
Local verification is not a restoration guarantee.
Passwords are accepted only through a private password_file in TOML.
`
