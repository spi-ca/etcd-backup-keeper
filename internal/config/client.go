package config

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"os"
	"syscall"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"etcd-backup-keeper/internal/apperr"
)

const maxTLSBytes = 4 * 1024 * 1024

// ClientConfig builds but does not connect a client for exactly one ordered
// candidate. Pass the whole-operation context (not the short probe context)
// here; use DialContext separately for the engine's first Status/probe RPC.
// Cluster identity checks and failover decisions belong to that future engine.
// Never log or serialize the returned config: it necessarily holds a password.
func (c Config) ClientConfig(ctx context.Context, candidate int) (clientv3.Config, error) {
	if err := ctx.Err(); err != nil {
		return clientv3.Config{}, err
	}
	if err := c.Validate(); err != nil {
		return clientv3.Config{}, err
	}
	if candidate < 0 || candidate >= len(c.Endpoints) {
		return clientv3.Config{}, apperr.New("config_invalid", "invalid endpoint candidate selection")
	}
	tlsConfig, err := c.clientTLS()
	if err != nil {
		return clientv3.Config{}, err
	}
	var password string
	if c.PasswordFile != "" {
		password, err = readPassword(c.PasswordFile)
		if err != nil {
			return clientv3.Config{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return clientv3.Config{}, err
	}
	return clientv3.Config{
		Endpoints:   []string{c.Endpoints[candidate]},
		DialTimeout: c.DialTimeout, Context: ctx, TLS: tlsConfig,
		Username: c.Username, Password: password,
		// Third-party diagnostics may expose endpoint/user details. Verbose
		// output must use our own safe stage events, never this raw logger.
		Logger: zap.NewNop(),
	}, nil
}

func (c Config) clientTLS() (*tls.Config, error) {
	if len(c.Endpoints[0]) >= 5 && c.Endpoints[0][:5] == "http:" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	// RootCAs nil uses system trust; supplying ca_file replaces system trust.
	if c.CAFile != "" {
		data, err := readTLSFile(c.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(data) {
			return nil, apperr.New("tls_invalid", "cannot load trusted CA certificates")
		}
	}
	if c.CertFile != "" {
		certPEM, err := readTLSFile(c.CertFile)
		if err != nil {
			return nil, err
		}
		keyPEM, err := readPrivateFile(c.KeyFile, maxTLSBytes)
		if err != nil {
			return nil, err
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, apperr.New("tls_invalid", "cannot load client certificate and key")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

func readTLSFile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, apperr.New("tls_invalid", "cannot read TLS certificate file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, apperr.New("tls_invalid", "TLS certificate file must be regular")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTLSBytes+1))
	if err != nil || len(data) > maxTLSBytes {
		return nil, apperr.New("tls_invalid", "cannot read TLS certificate file")
	}
	return data, nil
}
