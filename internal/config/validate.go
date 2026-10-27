package config

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"etcd-backup-keeper/internal/apperr"
)

// Validate checks structure without touching certificate/password files or a
// network. It also protects ClientConfig callers constructing Config directly.
func (c Config) Validate() error {
	if len(c.Endpoints) == 0 {
		return apperr.New("config_invalid", "endpoints must contain at least one candidate")
	}
	var scheme string
	seen := make(map[string]bool)
	for _, endpoint := range c.Endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") ||
			u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
			u.Fragment != "" || strings.Contains(endpoint, "#") || u.Opaque != "" ||
			(u.Path != "" && u.Path != "/") || u.RawPath != "" ||
			strings.ContainsFunc(endpoint, unicode.IsSpace) || strings.Contains(u.Host, "%") ||
			strings.HasSuffix(u.Host, ":") {
			return apperr.New("config_invalid", "endpoints must be HTTP(S) URLs without credentials, query, fragment or non-root path")
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return apperr.New("config_invalid", "endpoint port is invalid")
			}
		}
		if scheme != "" && scheme != u.Scheme {
			return apperr.New("config_invalid", "endpoints must use the same transport scheme")
		}
		scheme = u.Scheme
		if seen[endpoint] {
			return apperr.New("config_invalid", "endpoints must not contain duplicate candidates")
		}
		seen[endpoint] = true
	}
	if !filepath.IsAbs(c.BackupDir) || strings.ContainsRune(c.BackupDir, 0) {
		return apperr.New("config_invalid", "backup_dir must be an absolute path")
	}
	if c.Keep < 1 {
		return apperr.New("config_invalid", "keep must be at least one")
	}
	if c.DialTimeout <= 0 || c.RequestTimeout <= 0 {
		return apperr.New("config_invalid", "timeouts must be positive")
	}
	if c.ExpectedClusterID != "" {
		id := strings.TrimPrefix(c.ExpectedClusterID, "0x")
		if len(id) == 0 || len(id) > 16 || strings.ContainsAny(id, "+-_") {
			return apperr.New("config_invalid", "expected_cluster_id must be a nonzero 64-bit hexadecimal string")
		}
		value, err := strconv.ParseUint(id, 16, 64)
		if err != nil || value == 0 {
			return apperr.New("config_invalid", "expected_cluster_id must be a nonzero 64-bit hexadecimal string")
		}
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return apperr.New("config_invalid", "cert_file and key_file must be supplied together")
	}
	for _, path := range []string{c.CAFile, c.CertFile, c.KeyFile, c.PasswordFile} {
		if path != "" && (!filepath.IsAbs(path) || strings.ContainsRune(path, 0)) {
			return apperr.New("config_invalid", "TLS and password file paths must be absolute")
		}
	}
	if scheme != "https" && (c.CAFile != "" || c.CertFile != "" || c.KeyFile != "") {
		return apperr.New("config_invalid", "TLS files require HTTPS endpoints")
	}
	if (c.Username == "") != (c.PasswordFile == "") {
		return apperr.New("config_invalid", "username and password_file must be supplied together")
	}
	if c.Username != "" {
		if strings.TrimSpace(c.Username) == "" || strings.ContainsFunc(c.Username, unicode.IsControl) {
			return apperr.New("config_invalid", "username is invalid")
		}
		if scheme != "https" {
			return apperr.New("config_invalid", "password authentication requires HTTPS endpoints")
		}
	}
	return nil
}
