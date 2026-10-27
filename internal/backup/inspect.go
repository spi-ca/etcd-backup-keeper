package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"go.etcd.io/etcd/etcdutl/v3/snapshot"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/etcdcompat"
)

const inspectArgument = "--internal-inspect-snapshot-fd"

type Inspection struct {
	Revision       int64  `json:"revision"`
	Keys           int    `json:"keys"`
	DBBytes        int64  `json:"db_bytes"`
	StorageVersion string `json:"storage_version"`
}

func verificationError() error {
	return apperr.New("snapshot_invalid", "snapshot checksum or local structure verification failed")
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Checksum compares the actual etcd trailing SHA-256 with the database body,
// then includes that trailer in our independent whole-file digest.
func Checksum(ctx context.Context, f *os.File) (string, int64, error) {
	st, err := f.Stat()
	if err != nil || st.Size() <= sha256.Size || st.Size()%512 != sha256.Size {
		return "", 0, verificationError()
	}
	bodySize := st.Size() - sha256.Size
	h := sha256.New()
	if _, err = io.Copy(h, contextReader{ctx, io.NewSectionReader(f, 0, bodySize)}); err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, verificationError()
	}
	tail := make([]byte, sha256.Size)
	if _, err = f.ReadAt(tail, bodySize); err != nil || subtle.ConstantTimeCompare(h.Sum(nil), tail) != 1 {
		return "", 0, verificationError()
	}
	h.Write(tail)
	return hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

// Inspect runs this binary as a bounded child, inheriting only a pinned
// snapshot descriptor. Third-party stderr/errors (possibly containing DB
// values) are discarded. CommandContext kills and Wait reaps before return.
func Inspect(ctx context.Context, f *os.File) (Inspection, error) {
	var result Inspection
	if err := ctx.Err(); err != nil {
		return result, err
	}
	exe, err := os.Executable()
	if err != nil {
		return result, verificationError()
	}
	cmd := exec.CommandContext(ctx, exe, inspectArgument)
	cmd.ExtraFiles = []*os.File{f}
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, verificationError()
	}
	dec := json.NewDecoder(bytes.NewReader(out.buffer.Bytes()))
	dec.DisallowUnknownFields()
	if dec.Decode(&result) != nil {
		return Inspection{}, verificationError()
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || result.Revision < 0 || result.Keys < 0 || result.DBBytes <= 0 || !etcdcompat.Storage(result.StorageVersion) {
		return Inspection{}, verificationError()
	}
	return result, nil
}

// A named field prevents bytes.Buffer.ReadFrom from bypassing Write's cap
// when exec.Cmd drains stdout through io.Copy.
type cappedBuffer struct{ buffer bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > 4096-b.buffer.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(p)
}

// InspectionHelper is called before normal CLI dispatch. No path, endpoint,
// username, or secret is accepted in argv. It is not a public CLI command.
func InspectionHelper(args []string) (handled bool, code int) {
	if len(args) != 1 || args[0] != inspectArgument {
		return false, 0
	}
	return true, runInspectionHelper()
}
func runInspectionHelper() (code int) {
	code = 1
	defer func() {
		if recover() != nil {
			code = 1
		}
	}()
	f := os.NewFile(3, "snapshot")
	if f == nil {
		return 1
	}
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(3, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		return 1
	}
	status, err := snapshot.NewV3(zap.NewNop()).Status("/proc/self/fd/3")
	if err != nil {
		return 1
	}
	result := Inspection{Revision: status.Revision, Keys: status.TotalKey, DBBytes: status.TotalSize, StorageVersion: status.Version}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		return 1
	}
	return 0
}
