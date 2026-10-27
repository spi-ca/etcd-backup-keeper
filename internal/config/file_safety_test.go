package config

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNonregularConfigAndCertificates(t *testing.T) {
	for _, field := range []string{"config", "ca_file", "cert_file"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input-fifo")
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			check := func() error {
				if field == "config" {
					_, err := Load(path)
					return err
				}
				cfg, err := Parse([]byte(validTOML))
				if err != nil {
					return err
				}
				if field == "ca_file" {
					cfg.CAFile = path
				} else {
					cfg.CertFile = path
					cfg.KeyFile = filepath.Join(filepath.Dir(path), "unused-key")
				}
				_, err = cfg.ClientConfig(context.Background(), 0)
				return err
			}
			done := make(chan error, 1)
			go func() { done <- check() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("accepted FIFO input")
				}
			case <-time.After(time.Second):
				// Unblock a regressed blocking open before failing the test.
				fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
				if err == nil {
					syscall.Close(fd)
				}
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				t.Fatal("FIFO input blocked instead of being rejected")
			}
		})
	}
	if _, err := Load(os.DevNull); err == nil {
		t.Fatal("accepted device as configuration")
	}
	if _, err := readTLSFile(os.DevNull); err == nil {
		t.Fatal("accepted device as certificate")
	}
}
