package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"etcd-backup-keeper/internal/apperr"
)

// These are synthetic bbolt fixtures, not evidence of real server compatibility.
// All four combinations are intentional: do not equate server and storage minor.
func TestSupportedVersionsBackupAndOfflineVerify(t *testing.T) {
	for _, server := range []string{"3.6.8", "3.7.3"} {
		for _, storageVersion := range []string{"3.6.0", "3.7.0"} {
			t.Run(server+"/"+storageVersion, func(t *testing.T) {
				data := fixtureWithStorage(t, storageVersion)
				cfg := testConfig(t)
				deps, candidates := fakeDependencies(data)
				candidates[0].info.serverVersion = server
				// Leave candidate 2 at 3.7.3 to also exercise mixed-version probes.
				candidates[0].snapshot = func(context.Context) (stream, error) {
					return stream{body: io.NopCloser(bytes.NewReader(data)), storageVersion: storageVersion}, nil
				}
				result, err := run(context.Background(), cfg, "test", deps)
				must(t, err)
				if result.Metadata.ServerVersion != server || result.Metadata.StorageVersion != storageVersion {
					t.Fatalf("versions not preserved: %+v", result.Metadata)
				}
				// No remote server or credential material exists for these commands.
				status, err := Status(context.Background(), cfg)
				must(t, err)
				if len(status.Completed) != 1 || status.Completed[0].Metadata != result.Metadata {
					t.Fatalf("metadata reader mismatch: %+v", status)
				}
				for _, name := range []string{"", result.Name} {
					verified, err := Verify(context.Background(), cfg, name)
					must(t, err)
					if len(verified.Verified) != 1 || verified.Verified[0].Metadata != result.Metadata {
						t.Fatalf("offline verification mismatch: %+v", verified)
					}
				}
			})
		}
	}
}

func TestUnsupportedVersionsFailClosed(t *testing.T) {
	for _, kind := range []string{"server", "storage"} {
		for _, version := range []string{"", "garbage", "3.5.0", "3.8.0", "4.0.0", "3.6.8-rc.1", "3.7.0+build"} {
			t.Run(kind+"/"+version, func(t *testing.T) {
				data := fixture(t)
				cfg := testConfig(t)
				deps, candidates := fakeDependencies(data)
				prior, err := run(context.Background(), cfg, "test", deps)
				must(t, err)
				before := treeBytes(t, cfg.BackupDir+"/"+prior.Name)
				for _, c := range candidates {
					c.snapshots = 0
				}
				code := "server_unsupported"
				if kind == "server" {
					candidates[0].info.serverVersion = version
				} else {
					code = "storage_unsupported"
					candidates[0].snapshot = func(context.Context) (stream, error) {
						return stream{body: io.NopCloser(bytes.NewReader(data)), storageVersion: version}, nil
					}
				}
				_, err = run(context.Background(), cfg, "test", deps)
				if err == nil || apperr.Safe(err).Code != code || candidates[1].snapshots != 0 {
					t.Fatalf("unsupported version not blocked: %v", err)
				}
				if len(names(t, cfg.BackupDir, "backup-")) != 1 {
					t.Fatal("unsupported version published a backup")
				}
				assertPrior(t, cfg.BackupDir+"/"+prior.Name, before)
				m := prior.Metadata
				if kind == "server" {
					m.ServerVersion = version
				} else {
					m.StorageVersion = version
				}
				encoded, err := json.Marshal(m)
				must(t, err)
				if _, err := ParseMetadata(encoded); err == nil {
					t.Fatal("metadata accepted unsupported version")
				}
			})
		}
	}
}

func TestStorageInspectionMustMatchStream(t *testing.T) {
	for _, stored := range []string{"3.6.0", "3.8.0"} {
		t.Run(stored, func(t *testing.T) {
			// Stream claims supported 3.7.0; actual DB is different or unsupported.
			cfg := testConfig(t)
			deps, candidates := fakeDependencies(fixtureWithStorage(t, stored))
			_, err := run(context.Background(), cfg, "test", deps)
			if err == nil || apperr.Safe(err).Code != "snapshot_invalid" || candidates[1].snapshots != 0 || len(names(t, cfg.BackupDir, "backup-")) != 0 {
				t.Fatalf("storage mismatch published or failed over: %v", err)
			}
		})
	}
}
