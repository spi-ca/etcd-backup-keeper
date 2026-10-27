package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/storage"
)

const FormatVersion = 1
const maxMetadata = 64 * 1024

type Metadata struct {
	FormatVersion  int       `json:"format_version"`
	ToolVersion    string    `json:"tool_version"`
	ServerVersion  string    `json:"server_version"`
	StorageVersion string    `json:"storage_version"`
	ClusterID      string    `json:"cluster_id"`
	MemberID       string    `json:"member_id"`
	CreatedAt      time.Time `json:"created_at"`
	CompletedAt    time.Time `json:"completed_at"`
	Revision       int64     `json:"revision"`
	Keys           int       `json:"keys"`
	DBBytes        int64     `json:"db_bytes"`
	Bytes          int64     `json:"bytes"`
	SHA256         string    `json:"sha256"`
}

func metadataError() error {
	return apperr.New("metadata_invalid", "completed backup metadata or manifest is invalid")
}
func validDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}
func parseID(s string) (uint64, error) {
	id, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil || id == 0 {
		return 0, metadataError()
	}
	return id, nil
}

// ParseMetadata rejects unknown, missing, duplicate and miscased fields. This
// is an untrusted disk format, not the forgiving encoding/json struct default.
func ParseMetadata(data []byte) (Metadata, error) {
	var m Metadata
	if len(data) > maxMetadata {
		return m, metadataError()
	}
	fields := map[string]bool{
		"format_version": true, "tool_version": true,
		"server_version": true, "storage_version": true,
		"cluster_id": true, "member_id": true,
		"created_at": true, "completed_at": true,
		"revision": true, "keys": true, "db_bytes": true,
		"bytes": true, "sha256": true,
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return m, metadataError()
	}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok || !fields[name] {
			return m, metadataError()
		}
		delete(fields, name)
		var value json.RawMessage
		if dec.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return m, metadataError()
		}
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') || len(fields) != 0 {
		return m, metadataError()
	}
	if _, err = dec.Token(); err != io.EOF {
		return m, metadataError()
	}
	if json.Unmarshal(data, &m) != nil {
		return m, metadataError()
	}
	if m.FormatVersion != FormatVersion || !supportedServer(m.ServerVersion) || m.StorageVersion != "3.7.0" {
		return m, metadataError()
	}
	if m.ToolVersion == "" || len(m.ToolVersion) > 256 || strings.ContainsAny(m.ToolVersion, "\n\r\x00") {
		return m, metadataError()
	}
	if m.Revision < 0 || m.Keys < 0 || m.Bytes <= sha256.Size || m.Bytes%512 != sha256.Size {
		return m, metadataError()
	}
	if m.DBBytes <= 0 || m.DBBytes > m.Bytes-sha256.Size || !validDigest(m.SHA256) {
		return m, metadataError()
	}
	for _, id := range []string{m.ClusterID, m.MemberID} {
		value, err := parseID(id)
		if err != nil || strconv.FormatUint(value, 16) != id {
			return m, metadataError()
		}
	}
	if m.CreatedAt.IsZero() || m.CompletedAt.Before(m.CreatedAt) || m.CreatedAt.Location() != time.UTC || m.CompletedAt.Location() != time.UTC {
		return m, metadataError()
	}
	return m, nil
}

func readBounded(d *storage.Dir, name string, limit int64) ([]byte, error) {
	f, err := d.OpenFile(name, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, metadataError()
	}
	return data, nil
}

// ReadMetadata validates the metadata digest in the fixed manifest, and checks
// the snapshot's stat size. Full snapshot hashing belongs to verify, not status.
func ReadMetadata(d *storage.Dir) (Metadata, error) {
	var m Metadata
	data, err := readBounded(d, "metadata.json", maxMetadata)
	if err != nil {
		return m, err
	}
	m, err = ParseMetadata(data)
	if err != nil {
		return m, err
	}
	manifest, err := readBounded(d, "SHA256SUMS", 1024)
	if err != nil {
		return m, err
	}
	if string(manifest) != makeManifest(m.SHA256, data) {
		return Metadata{}, metadataError()
	}
	f, err := d.OpenFile("snapshot.db", false)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() != m.Bytes {
		return Metadata{}, metadataError()
	}
	return m, nil
}

func makeManifest(snapshotHash string, metadata []byte) string {
	sum := sha256.Sum256(metadata)
	return snapshotHash + "  snapshot.db\n" + hex.EncodeToString(sum[:]) + "  metadata.json\n"
}

func clusterPin(r *storage.Root, expected string) (uint64, error) {
	var pin uint64
	if expected != "" {
		var err error
		pin, err = parseID(expected)
		if err != nil {
			return 0, err
		}
	}
	names, err := r.Inventory()
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "backup-") {
			continue
		}
		d, err := r.OpenDir(name)
		if err != nil {
			return 0, err
		}
		m, err := ReadMetadata(d)
		d.Close()
		if err != nil {
			return 0, err
		}
		id, _ := parseID(m.ClusterID)
		if pin != 0 && pin != id {
			return 0, identityError()
		}
		pin = id
	}
	return pin, nil
}
