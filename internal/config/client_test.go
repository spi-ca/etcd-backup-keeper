package config

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	for _, content := range []string{"SECRET_CANARY", "SECRET_CANARY\n", "SECRET_CANARY\r\n", "  SECRET_CANARY  \n"} {
		writeFixture(t, path, []byte(content), 0600)
		password, err := readPassword(path)
		if err != nil || password != strings.TrimSuffix(strings.TrimSuffix(content, "\n"), "\r") {
			t.Fatal("failed to preserve password semantics", err)
		}
	}
	for _, content := range []string{"", "\n", "SECRET_CANARY\nextra", "SECRET_CANARY\x00", strings.Repeat("x", maxPasswordBytes+1)} {
		writeFixture(t, path, []byte(content), 0600)
		_, err := readPassword(path)
		if err == nil || strings.Contains(err.Error(), "SECRET_CANARY") {
			t.Fatal("unsafe password validation", err)
		}
	}
	writeFixture(t, path, []byte("SECRET_CANARY"), 0644)
	if _, err := readPassword(path); err == nil {
		t.Fatal("accepted public password file")
	}
	link := path + "-symlink"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPassword(link); err == nil {
		t.Fatal("accepted password symlink")
	}
	if _, err := readPassword(filepath.Dir(path)); err == nil {
		t.Fatal("accepted directory as password")
	}
	fifo := path + "-fifo"
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPassword(fifo); err == nil {
		t.Fatal("accepted FIFO as password")
	}
}

func TestClientConfigDefaultsAndCandidate(t *testing.T) {
	cfg, _ := Parse([]byte(validTOML))
	ctx := context.Background()
	client, err := cfg.ClientConfig(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.Endpoints) != 1 || client.Endpoints[0] != cfg.Endpoints[1] || client.Context != ctx || client.DialTimeout != cfg.DialTimeout {
		t.Fatal("client not scoped to selected candidate/context")
	}
	if client.TLS == nil || client.TLS.InsecureSkipVerify || client.TLS.MinVersion != tls.VersionTLS12 || client.TLS.RootCAs != nil || client.Logger == nil || client.AutoSyncInterval != 0 {
		t.Fatal("unsafe client defaults")
	}
	for _, index := range []int{-1, 2} {
		if _, err := cfg.ClientConfig(ctx, index); err == nil {
			t.Fatal("accepted invalid candidate")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cfg.ClientConfig(canceled, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("client build ignored cancellation")
	}
	cfg.Endpoints = []string{"http://localhost:2379"}
	client, err = cfg.ClientConfig(ctx, 0)
	if err != nil || client.TLS != nil {
		t.Fatal("HTTP client should not configure TLS", err)
	}
}

func TestClientConfigMTLSAndPassword(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-only"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg, _ := Parse([]byte(validTOML))
	cfg.CAFile, cfg.CertFile, cfg.KeyFile = filepath.Join(dir, "ca"), filepath.Join(dir, "cert"), filepath.Join(dir, "key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	writeFixture(t, cfg.CAFile, certPEM, 0644)
	writeFixture(t, cfg.CertFile, certPEM, 0644)
	writeFixture(t, cfg.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600)
	cfg.Username, cfg.PasswordFile = "test-user", filepath.Join(dir, "password")
	writeFixture(t, cfg.PasswordFile, []byte("SECRET_CANARY\n"), 0600)
	client, err := cfg.ClientConfig(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if client.Username != "test-user" || client.Password != "SECRET_CANARY" || len(client.TLS.Certificates) != 1 || client.TLS.RootCAs == nil || client.TLS.InsecureSkipVerify {
		t.Fatal("mTLS/auth material not loaded safely")
	}
	writeFixture(t, cfg.KeyFile, []byte("SECRET_CANARY"), 0600)
	if _, err := cfg.ClientConfig(context.Background(), 0); err == nil || strings.Contains(err.Error(), "SECRET_CANARY") {
		t.Fatal("unsafe key parsing diagnostic", err)
	}
	writeFixture(t, cfg.CAFile, []byte("SECRET_CANARY"), 0644)
	if _, err := cfg.ClientConfig(context.Background(), 0); err == nil || strings.Contains(err.Error(), "SECRET_CANARY") {
		t.Fatal("unsafe CA parsing diagnostic", err)
	}
}
