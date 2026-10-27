package config

import (
	"bytes"
	"io"
	"os"
	"syscall"

	"etcd-backup-keeper/internal/apperr"
)

const maxPasswordBytes = 64 * 1024

// readPrivateFile checks the opened descriptor rather than a racy pre-open
// stat. Linux O_NOFOLLOW rejects final-component symlinks; O_NONBLOCK prevents
// a FIFO from hanging before the regular-file check. Parent directories must
// be operator-controlled. No error from this function contains file contents.
func readPrivateFile(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, apperr.New("credentials_invalid", "cannot read private credential file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, apperr.New("credentials_invalid", "credential file must be regular and accessible only by its owner")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, apperr.New("credentials_invalid", "credential file must be owned by the effective user")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, apperr.New("credentials_invalid", "cannot read private credential file")
	}
	return data, nil
}

// readPassword preserves spaces, removes at most one trailing LF/CRLF, and
// refuses empty/multiline/NUL passwords. No plaintext config or argv support.
func readPassword(path string) (string, error) {
	data, err := readPrivateFile(path, maxPasswordBytes)
	if err != nil {
		return "", err
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	data = bytes.TrimSuffix(data, []byte("\r"))
	if len(data) == 0 || bytes.ContainsAny(data, "\r\n\x00") {
		return "", apperr.New("credentials_invalid", "password file must contain one nonempty line")
	}
	return string(data), nil
}
