package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
	"etcd-backup-keeper/internal/storage"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == inspectArgument && os.Getenv("EBK_TEST_INSPECT_MODE") == "hang" {
		time.Sleep(time.Minute)
		os.Exit(1)
	}
	if handled, code := InspectionHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := bolt.Open(path, 0600, nil)
	must(t, err)
	must(t, db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket([]byte("meta"))
		if err != nil {
			return err
		}
		if err = meta.Put([]byte("storageVersion"), []byte("3.7.0")); err != nil {
			return err
		}
		key, err := tx.CreateBucket([]byte("key"))
		if err != nil {
			return err
		}
		value, err := proto.Marshal(&mvccpb.KeyValue{Key: []byte("test-key"), Value: []byte("SECRET_DB_CANARY"), CreateRevision: 2, ModRevision: 2, Version: 1})
		if err != nil {
			return err
		}
		return key.Put(fixtureRevisionKey(), value)
	}))
	must(t, db.Close())
	data, err := os.ReadFile(path)
	must(t, err)
	sum := sha256.Sum256(data)
	return append(data, sum[:]...)
}
func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Endpoints: []string{"http://candidate-one.invalid:2379", "http://candidate-two.invalid:2379"}, BackupDir: filepath.Join(t.TempDir(), "backups"), Keep: 1, DialTimeout: time.Second, RequestTimeout: 10 * time.Second}
}

type fakeCandidate struct {
	info                             probe
	probeErr                         error
	snapshot                         func(context.Context) (stream, error)
	probes, snapshots, opens, closes int
}
type fakeConnection struct{ candidate *fakeCandidate }

func (c *fakeConnection) Probe(context.Context) (probe, error) {
	c.candidate.probes++
	return c.candidate.info, c.candidate.probeErr
}
func (c *fakeConnection) Snapshot(ctx context.Context) (stream, error) {
	c.candidate.snapshots++
	return c.candidate.snapshot(ctx)
}
func (c *fakeConnection) Close() { c.candidate.closes++ }
func fakeDependencies(data []byte) (dependencies, []*fakeCandidate) {
	deps := defaults()
	candidates := []*fakeCandidate{{info: probe{clusterID: 11, memberID: 21, dbBytes: 32768, serverVersion: "3.7.3"}}, {info: probe{clusterID: 11, memberID: 22, dbBytes: 32768, serverVersion: "3.7.3"}}}
	for _, c := range candidates {
		c.snapshot = func(context.Context) (stream, error) {
			return stream{body: io.NopCloser(bytes.NewReader(data)), clusterID: c.info.clusterID, memberID: c.info.memberID, storageVersion: "3.7.0"}, nil
		}
	}
	deps.connect = func(total, dial context.Context, cfg config.Config, index int) (connection, error) {
		if total.Err() != nil {
			return nil, remoteFailure{}
		}
		candidates[index].opens++
		return &fakeConnection{candidates[index]}, nil
	}
	return deps, candidates
}
func names(t *testing.T, path, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	must(t, err)
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	return names
}
func treeBytes(t *testing.T, path string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range []string{"snapshot.db", "metadata.json", "SHA256SUMS"} {
		b, err := os.ReadFile(filepath.Join(path, name))
		must(t, err)
		result[name] = string(b)
	}
	return result
}
func assertPrior(t *testing.T, path string, prior map[string]string) {
	t.Helper()
	for name, want := range prior {
		got, err := os.ReadFile(filepath.Join(path, name))
		must(t, err)
		if string(got) != want {
			t.Fatalf("prior %s changed", name)
		}
	}
}
func TestBackupPublishesAndPinsMetadata(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	deps, candidates := fakeDependencies(data)
	candidates[0].snapshot = func(context.Context) (stream, error) {
		if candidates[1].probes == 0 {
			t.Fatal("snapshot started before all probes")
		}
		return stream{body: io.NopCloser(bytes.NewReader(data)), clusterID: 11, memberID: 21, storageVersion: "3.7.0"}, nil
	}
	result, err := run(context.Background(), cfg, "test-version", deps)
	must(t, err)
	if result.RetentionPending || result.Metadata.Revision != 2 || result.Metadata.Keys != 1 || result.Metadata.ServerVersion != "3.7.3" || result.Metadata.StorageVersion != "3.7.0" {
		t.Fatalf("bad result: %+v", result)
	}
	if len(names(t, cfg.BackupDir, "inprogress-")) != 0 {
		t.Fatal("successful staging left behind")
	}
	root, err := storage.Open(cfg.BackupDir)
	must(t, err)
	defer root.Close()
	dir, err := root.OpenDir(result.Name)
	must(t, err)
	defer dir.Close()
	m, err := ReadMetadata(dir)
	must(t, err)
	if m.SHA256 != result.Metadata.SHA256 || m.Bytes != int64(len(data)) {
		t.Fatal("metadata mismatch")
	}
	digest := sha256.Sum256(data)
	if result.Metadata.SHA256 != strings.ToLower(strings.TrimSpace(result.Metadata.SHA256)) || result.Metadata.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("not whole-file digest")
	}
	for _, c := range candidates {
		if c.opens != c.closes {
			t.Fatal("client leak")
		}
	}
	for file := range treeBytes(t, filepath.Join(cfg.BackupDir, result.Name)) {
		st, err := os.Stat(filepath.Join(cfg.BackupDir, result.Name, file))
		must(t, err)
		if st.Mode().Perm() != 0600 {
			t.Fatal("file not private")
		}
	}
}

// etcd's MVCC fixture key is main revision, '_', subrevision in big endian.
func fixtureRevisionKey() []byte {
	key := make([]byte, 17)
	binary.BigEndian.PutUint64(key, 2)
	key[8] = '_'
	return key
}

func TestPublicationFailureBoundariesPreservePrior(t *testing.T) {
	data := fixture(t)
	cases := []string{"snapshot-write", "metadata-write", "manifest-write", "snapshot-sync", "metadata-sync", "manifest-sync", "directory-sync", "rename", "unsupported-rename", "collision", "root-sync", "inspection"}
	for _, boundary := range cases {
		t.Run(boundary, func(t *testing.T) {
			cfg := testConfig(t)
			deps, _ := fakeDependencies(data)
			prior, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			priorPath := filepath.Join(cfg.BackupDir, prior.Name)
			priorBytes := treeBytes(t, priorPath)
			deps, candidates := fakeDependencies(data)
			fault := errors.New("SECRET_INTERNAL_CANARY")
			baseWrite := deps.write
			if strings.HasSuffix(boundary, "-write") {
				target := map[string]string{"snapshot-write": "snapshot.db", "metadata-write": "metadata.json", "manifest-write": "SHA256SUMS"}[boundary]
				deps.write = func(f *os.File, p []byte) (int, error) {
					if f.Name() == target {
						return 0, fault
					}
					return baseWrite(f, p)
				}
			}
			baseSync := deps.syncFile
			if strings.HasSuffix(boundary, "-sync") {
				target := map[string]string{"snapshot-sync": "snapshot.db", "metadata-sync": "metadata.json", "manifest-sync": "SHA256SUMS"}[boundary]
				deps.syncFile = func(f *os.File) error {
					if f.Name() == target {
						return fault
					}
					return baseSync(f)
				}
			}
			if boundary == "directory-sync" {
				deps.syncDir = func(*storage.Dir) error { return fault }
			}
			if boundary == "rename" {
				deps.publish = func(*storage.Root, string, string) error { return fault }
			}
			if boundary == "unsupported-rename" {
				deps.publish = func(*storage.Root, string, string) error { return unix.ENOSYS }
			}
			if boundary == "collision" {
				deps.publish = func(r *storage.Root, stage, _ string) error { return r.Publish(stage, prior.Name) }
			}
			if boundary == "root-sync" {
				deps.syncRoot = func(*storage.Root) error { return fault }
			}
			if boundary == "inspection" {
				deps.inspect = func(context.Context, *os.File) (Inspection, error) { return Inspection{}, fault }
			}
			_, err = run(context.Background(), cfg, "test", deps)
			if err == nil || strings.Contains(apperr.Safe(err).Message, "CANARY") {
				t.Fatalf("unsafe/missing error: %v", err)
			}
			assertPrior(t, priorPath, priorBytes)
			wantComplete, wantStage := 1, 1
			if boundary == "root-sync" {
				wantComplete, wantStage = 2, 0
				if apperr.Safe(err).Code != "durability_uncertain" {
					t.Fatalf("wrong durability error: %v", err)
				}
			}
			if len(names(t, cfg.BackupDir, "backup-")) != wantComplete || len(names(t, cfg.BackupDir, "inprogress-")) != wantStage {
				t.Fatal("incorrect preservation")
			}
			if candidates[1].snapshots != 0 {
				t.Fatal("local error triggered failover")
			}
		})
	}
}

func TestIdentityMismatchAndUnsupportedServerFailClosed(t *testing.T) {
	data := fixture(t)
	for _, kind := range []string{"candidate", "expected", "header", "member", "existing", "server", "storage"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			deps, c := fakeDependencies(data)
			if kind == "existing" {
				_, err := run(context.Background(), cfg, "test", deps)
				must(t, err)
				for _, candidate := range c {
					candidate.info.clusterID = 12
					candidate.snapshots = 0
				}
			}
			switch kind {
			case "candidate":
				c[1].info.clusterID = 12
			case "expected":
				cfg.ExpectedClusterID = "c"
			case "server":
				c[1].info.serverVersion = "3.8.0"
			case "header", "member", "storage":
				c[0].snapshot = func(context.Context) (stream, error) {
					s := stream{body: io.NopCloser(bytes.NewReader(data)), clusterID: 11, memberID: 21, storageVersion: "3.7.0"}
					if kind == "header" {
						s.clusterID = 12
					}
					if kind == "member" {
						s.memberID = 23
					}
					if kind == "storage" {
						s.storageVersion = "3.8.0"
					}
					return s, nil
				}
			}
			_, err := run(context.Background(), cfg, "test", deps)
			if err == nil {
				t.Fatal("mismatch accepted")
			}
			if c[1].snapshots != 0 {
				t.Fatal("identity failure triggered failover")
			}
			if kind != "existing" && len(names(t, cfg.BackupDir, "backup-")) != 0 {
				t.Fatal("published mismatched snapshot")
			}
		})
	}
}

func TestHeaderlessSnapshotChecksPostStreamIdentity(t *testing.T) {
	data := fixture(t)
	for _, change := range []string{"none", "cluster", "member"} {
		t.Run(change, func(t *testing.T) {
			cfg := testConfig(t)
			deps, candidates := fakeDependencies(data)
			candidates[0].snapshot = func(context.Context) (stream, error) {
				if change == "cluster" {
					candidates[0].info.clusterID = 99
				}
				if change == "member" {
					candidates[0].info.memberID = 99
				}
				return stream{body: io.NopCloser(bytes.NewReader(data)), storageVersion: "3.7.0"}, nil
			}
			_, err := run(context.Background(), cfg, "test", deps)
			if change == "none" {
				must(t, err)
				if candidates[0].probes != 3 {
					t.Fatal("missing preflight, selected pre-stream or post-stream probe")
				}
			} else if err == nil || len(names(t, cfg.BackupDir, "backup-")) != 0 || candidates[1].snapshots != 0 {
				t.Fatal("post-stream identity mismatch did not fail closed")
			}
		})
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
func TestRemoteFailureAloneAdvancesCandidate(t *testing.T) {
	data := fixture(t)
	for _, kind := range []string{"probe", "connect", "open-stream", "read-stream"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			deps, c := fakeDependencies(data)
			if kind == "probe" {
				c[0].probeErr = remoteFailure{}
			}
			if kind == "connect" {
				original := deps.connect
				deps.connect = func(total, dial context.Context, cfg config.Config, i int) (connection, error) {
					if i == 0 {
						return nil, remoteFailure{}
					}
					return original(total, dial, cfg, i)
				}
			}
			if kind == "open-stream" {
				c[0].snapshot = func(context.Context) (stream, error) { return stream{}, remoteFailure{} }
			}
			if kind == "read-stream" {
				c[0].snapshot = func(context.Context) (stream, error) {
					return stream{body: io.NopCloser(errorReader{errors.New("SECRET_REMOTE_CANARY")}), clusterID: 11, memberID: 21, storageVersion: "3.7.0"}, nil
				}
			}
			_, err := run(context.Background(), cfg, "test", deps)
			must(t, err)
			if c[1].snapshots != 1 {
				t.Fatal("did not fail over")
			}
			stages := 0
			if kind == "open-stream" || kind == "read-stream" {
				stages = 1
			}
			if len(names(t, cfg.BackupDir, "inprogress-")) != stages {
				t.Fatal("failed staging not preserved")
			}
		})
	}
}
func TestCanceledStreamPreservesPriorAndDoesNotFailOver(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	deps, _ := fakeDependencies(data)
	prior, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	priorPath := filepath.Join(cfg.BackupDir, prior.Name)
	priorBytes := treeBytes(t, priorPath)
	deps, c := fakeDependencies(data)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c[0].snapshot = func(context.Context) (stream, error) {
		cancel()
		return stream{body: io.NopCloser(errorReader{context.Canceled}), clusterID: 11, memberID: 21, storageVersion: "3.7.0"}, nil
	}
	_, err = run(ctx, cfg, "test", deps)
	if err == nil || apperr.Safe(err).Code != "canceled" {
		t.Fatalf("wrong cancel: %v", err)
	}
	assertPrior(t, priorPath, priorBytes)
	if c[1].snapshots != 0 || len(names(t, cfg.BackupDir, "inprogress-")) != 1 {
		t.Fatal("cancel failed preservation")
	}
}
func TestInternalChecksumCorruptionIsNotFailover(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	data[100] ^= 1
	deps, c := fakeDependencies(data)
	_, err := run(context.Background(), cfg, "test", deps)
	if err == nil || apperr.Safe(err).Code != "snapshot_invalid" || c[1].snapshots != 0 {
		t.Fatalf("corrupt snapshot accepted/failover: %v", err)
	}
	if len(names(t, cfg.BackupDir, "backup-")) != 0 {
		t.Fatal("published corruption")
	}
}
func TestSpaceFloorOverflow(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	cfg.MinFreeBytes = ^uint64(0)
	deps, c := fakeDependencies(data)
	_, err := run(context.Background(), cfg, "test", deps)
	if err == nil || apperr.Safe(err).Code != "space_insufficient" || c[0].snapshots != 0 || c[1].snapshots != 0 {
		t.Fatalf("space overflow: %v", err)
	}
}
func TestStrictMetadata(t *testing.T) {
	data := fixture(t)
	cfg := testConfig(t)
	deps, _ := fakeDependencies(data)
	result, err := run(context.Background(), cfg, "test", deps)
	must(t, err)
	original, err := json.Marshal(result.Metadata)
	must(t, err)
	cases := [][]byte{
		bytes.Replace(original, []byte(`"format_version":1`), []byte(`"format_version":2`), 1),
		bytes.Replace(original, []byte(`"format_version":1`), []byte(`"Format_version":1`), 1),
		bytes.Replace(original, []byte(`"format_version":1`), []byte(`"format_version":null`), 1),
		bytes.Replace(original, []byte(`"format_version":1`), []byte(`"format_version":1,"format_version":1`), 1),
		bytes.Replace(original, []byte(`"format_version":1,`), nil, 1),
		append(append([]byte{}, original...), []byte(` {}`)...),
	}
	for _, input := range cases {
		if _, err := ParseMetadata(input); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	// A metadata edit without a matching manifest must prevent future backups.
	path := filepath.Join(cfg.BackupDir, result.Name, "metadata.json")
	must(t, os.WriteFile(path, bytes.Replace(original, []byte(`"tool_version":"test"`), []byte(`"tool_version":"edit"`), 1), 0600))
	_, err = run(context.Background(), cfg, "test", deps)
	if err == nil {
		t.Fatal("accepted altered metadata")
	}
}
