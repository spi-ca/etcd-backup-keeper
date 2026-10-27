// Package config loads a strict, flat TOML configuration. It never includes
// supplied values or underlying parser errors in its public diagnostics.
package config

import (
	"context"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"etcd-backup-keeper/internal/apperr"
)

const (
	DefaultPath           = "/etc/etcd-backup-keeper/config.toml"
	DefaultDialTimeout    = 5 * time.Second
	DefaultRequestTimeout = 30 * time.Minute
	maxConfigBytes        = 1024 * 1024
)

// Config contains no password. Preserve Endpoints order when trying candidates;
// a future snapshot attempt must use ClientConfig with exactly one candidate.
type Config struct {
	Endpoints         []string
	ExpectedClusterID string
	BackupDir         string
	Keep              int
	MinFreeBytes      uint64
	DialTimeout       time.Duration
	RequestTimeout    time.Duration
	CAFile            string
	CertFile          string
	KeyFile           string
	Username          string
	PasswordFile      string
}

type document struct {
	Endpoints         []string `toml:"endpoints"`
	ExpectedClusterID string   `toml:"expected_cluster_id"`
	BackupDir         string   `toml:"backup_dir"`
	Keep              int      `toml:"keep"`
	MinFreeBytes      int64    `toml:"min_free_bytes"`
	DialTimeout       string   `toml:"dial_timeout"`
	RequestTimeout    string   `toml:"request_timeout"`
	CAFile            string   `toml:"ca_file"`
	CertFile          string   `toml:"cert_file"`
	KeyFile           string   `toml:"key_file"`
	Username          string   `toml:"username"`
	PasswordFile      string   `toml:"password_file"`
}

var allowedKeys = map[string]bool{
	"endpoints": true, "expected_cluster_id": true, "backup_dir": true,
	"keep": true, "min_free_bytes": true, "dial_timeout": true,
	"request_timeout": true, "ca_file": true, "cert_file": true,
	"key_file": true, "username": true, "password_file": true,
}

// Load reads configuration only; local status/verify must not read credentials
// or construct a network client. File existence/TLS material is checked later
// by ClientConfig, only when a remote operation actually needs it.
func Load(path string) (Config, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Config{}, apperr.New("config_read", "cannot read configuration file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Config{}, apperr.New("config_read", "configuration file must be regular")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return Config{}, apperr.New("config_read", "cannot read configuration file")
	}
	return Parse(data)
}

func Parse(data []byte) (Config, error) {
	if len(data) > maxConfigBytes {
		return Config{}, apperr.New("config_invalid", "invalid TOML configuration")
	}
	doc := document{
		DialTimeout: DefaultDialTimeout.String(), RequestTimeout: DefaultRequestTimeout.String(),
	}
	meta, err := toml.Decode(string(data), &doc)
	if err != nil {
		return Config{}, apperr.New("config_invalid", "invalid TOML configuration")
	}
	// The decoder accepts case-insensitive field matches. Check original keys
	// explicitly, including table keys, rather than relying on Undecoded alone.
	for _, key := range meta.Keys() {
		if len(key) != 1 || !allowedKeys[key[0]] {
			return Config{}, apperr.New("config_invalid", "configuration contains an unknown or miscased field")
		}
	}
	if len(meta.Undecoded()) != 0 {
		return Config{}, apperr.New("config_invalid", "invalid TOML configuration")
	}
	if doc.MinFreeBytes < 0 {
		return Config{}, apperr.New("config_invalid", "min_free_bytes must not be negative")
	}
	dial, err := time.ParseDuration(doc.DialTimeout)
	if err != nil || dial <= 0 {
		return Config{}, apperr.New("config_invalid", "dial_timeout must be a positive Go duration")
	}
	request, err := time.ParseDuration(doc.RequestTimeout)
	if err != nil || request <= 0 {
		return Config{}, apperr.New("config_invalid", "request_timeout must be a positive Go duration")
	}
	cfg := Config{
		Endpoints: doc.Endpoints, ExpectedClusterID: doc.ExpectedClusterID,
		BackupDir: doc.BackupDir, Keep: doc.Keep, MinFreeBytes: uint64(doc.MinFreeBytes),
		DialTimeout: dial, RequestTimeout: request, CAFile: doc.CAFile,
		CertFile: doc.CertFile, KeyFile: doc.KeyFile,
		Username: doc.Username, PasswordFile: doc.PasswordFile,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// RequestContext bounds the whole command after configuration loading,
// including all candidate attempts and snapshot streaming, not idle time.
// It never extends an earlier parent deadline.
func (c Config) RequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, c.RequestTimeout)
}

// DialContext bounds one candidate's initialization/authentication and initial
// remote probe. etcd 3.7 client creation is nonblocking: its DialTimeout field
// alone does NOT bound connection establishment. The engine must use this
// context for the initial probe, then RequestContext for snapshot streaming.
func (c Config) DialContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, c.DialTimeout)
}
