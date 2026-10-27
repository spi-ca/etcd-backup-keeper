package cli

import (
	"flag"
	"io"
	"strings"
	"unicode"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
)

type options struct {
	command string
	config  string
	json    bool
	verbose bool
	connect bool
	backup  string
	help    bool
}

func commonFlags(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.config, "config", opts.config, "TOML configuration path")
	fs.BoolVar(&opts.json, "json", opts.json, "machine-readable output")
}

func parse(args []string) (options, error) {
	opts := options{config: config.DefaultPath}
	root := flag.NewFlagSet("etcd-backup-keeper", flag.ContinueOnError)
	root.SetOutput(io.Discard) // flag errors may contain supplied secret values.
	commonFlags(root, &opts)
	root.BoolVar(&opts.help, "help", false, "show help")
	root.BoolVar(&opts.help, "h", false, "show help")
	if err := root.Parse(args); err != nil {
		return opts, apperr.New("usage", "invalid command-line arguments; use --help")
	}
	if opts.help {
		return opts, nil
	}
	rest := root.Args()
	if len(rest) == 0 {
		return opts, apperr.New("usage", "a command is required; use --help")
	}
	switch rest[0] {
	case "backup", "status", "verify", "check", "version":
		opts.command = rest[0]
	default:
		return opts, apperr.New("usage", "unknown command; use --help")
	}
	fs := flag.NewFlagSet(opts.command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	commonFlags(fs, &opts)
	fs.BoolVar(&opts.help, "help", false, "show help")
	fs.BoolVar(&opts.help, "h", false, "show help")
	if opts.command == "backup" {
		fs.BoolVar(&opts.verbose, "verbose", false, "safe stage diagnostics on stderr")
	}
	if opts.command == "check" {
		fs.BoolVar(&opts.connect, "connect", false, "opt in to remote Status probes")
	}
	if opts.command == "verify" {
		fs.StringVar(&opts.backup, "backup", "", "local backup directory name")
	}
	if err := fs.Parse(rest[1:]); err != nil || fs.NArg() != 0 {
		return opts, apperr.New("usage", "invalid command-line arguments; use --help")
	}
	if opts.config == "" {
		return opts, apperr.New("usage", "--config requires a nonempty path")
	}
	if opts.backup != "" && (opts.backup == "." || opts.backup == ".." ||
		strings.ContainsAny(opts.backup, "/\\") || strings.ContainsFunc(opts.backup, unicode.IsControl)) {
		return opts, apperr.New("usage", "--backup requires a local backup name, not a path")
	}
	return opts, nil
}

// requestedJSON permits a safe JSON usage error even when parsing stops before
// --json. Skip string flag values, which may themselves look like flags.
func requestedJSON(args []string) bool {
	result := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-config", "--backup", "-backup":
			i++
		case "--json", "-json", "--json=true", "-json=true":
			result = true
		case "--json=false", "-json=false":
			result = false
		case "--":
			return result
		}
	}
	return result
}
