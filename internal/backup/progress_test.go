package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/storage"
)

func recordProgress(deps *dependencies) *[]Progress {
	var events []Progress
	deps.observe = func(event Progress) { events = append(events, event) }
	deps.progressNow = func() time.Time { return time.Unix(100, 0) }
	return &events
}

func stages(events []Progress) []Stage {
	var result []Stage
	for _, event := range events {
		result = append(result, event.Stage)
	}
	return result
}

func TestProgressSuccessOrderAndBytes(t *testing.T) {
	data := fixture(t)
	deps, _ := fakeDependencies(data)
	events := recordProgress(&deps)
	publish, syncRoot, inspect := deps.publish, deps.syncRoot, deps.inspect
	assertStage := func(want Stage) {
		t.Helper()
		if got := (*events)[len(*events)-1].Stage; got != want {
			t.Fatalf("action preceded by stage %v, want %v", got, want)
		}
	}
	deps.inspect = func(ctx context.Context, f *os.File) (Inspection, error) {
		assertStage(InspectSnapshot)
		return inspect(ctx, f)
	}
	deps.publish = func(root *storage.Root, a, b string) error {
		assertStage(PublishSnapshot)
		return publish(root, a, b)
	}
	deps.syncRoot = func(root *storage.Root) error {
		assertStage(SyncRoot)
		return syncRoot(root)
	}
	result, err := run(context.Background(), testConfig(t), "test", deps)
	must(t, err)
	want := []Stage{ProbeCandidate, ProbeCandidate, AttemptSnapshot, Receiving, Received, ChecksumSnapshot, InspectSnapshot, PublishSnapshot, SyncRoot, Retention, Complete}
	if !reflect.DeepEqual(stages(*events), want) {
		t.Fatalf("events: %+v", *events)
	}
	if (*events)[0].Candidate != 1 || (*events)[1].Candidate != 2 || (*events)[1].Total != 2 {
		t.Fatalf("candidate indexes: %+v", *events)
	}
	if (*events)[4].Bytes != int64(len(data)) || (*events)[4].Bytes != result.Metadata.Bytes {
		t.Fatalf("final byte count: %+v", *events)
	}
}

func TestProgressFailureNeverCompletes(t *testing.T) {
	data := fixture(t)
	for _, boundary := range []string{"identity", "remote", "checksum", "inspection", "publication", "root-sync", "retention", "cancel"} {
		t.Run(boundary, func(t *testing.T) {
			cfg := testConfig(t)
			deps, candidates := fakeDependencies(data)
			if boundary == "retention" {
				_, err := run(context.Background(), cfg, "test", deps)
				must(t, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fault := errors.New("SECRET_PROGRESS_CANARY")
			switch boundary {
			case "identity":
				candidates[1].info.clusterID = 99
			case "remote":
				for _, c := range candidates {
					c.snapshot = func(context.Context) (stream, error) { return stream{}, remoteFailure{} }
				}
			case "checksum":
				bad := append([]byte(nil), data...)
				bad[100] ^= 1
				candidates[0].snapshot = func(context.Context) (stream, error) {
					return stream{body: io.NopCloser(bytes.NewReader(bad)), storageVersion: "3.7.0"}, nil
				}
			case "inspection":
				deps.inspect = func(context.Context, *os.File) (Inspection, error) { return Inspection{}, fault }
			case "publication":
				deps.publish = func(*storage.Root, string, string) error { return fault }
			case "root-sync":
				deps.syncRoot = func(*storage.Root) error { return fault }
			case "retention":
				deps.quarantine = func(*storage.Root, string) (string, error) { return "", fault }
			case "cancel":
				candidates[0].snapshot = func(context.Context) (stream, error) {
					cancel()
					return stream{body: io.NopCloser(bytes.NewReader(data)), storageVersion: "3.7.0"}, nil
				}
			}
			events := recordProgress(&deps)
			_, err := run(ctx, cfg, "test", deps)
			if err == nil {
				t.Fatal("expected failure")
			}
			for _, event := range *events {
				if event.Stage == Complete {
					t.Fatalf("false completion: %+v", *events)
				}
				if event.Stage == Failover && boundary != "remote" {
					t.Fatalf("nonremote failover: %+v", *events)
				}
				if event.Stage == Retention && boundary == "root-sync" {
					t.Fatal("retention reported after failed root sync")
				}
			}
			if boundary != "identity" && (*events)[len(*events)-1].Stage != AttemptFailed {
				t.Fatalf("missing failure fact: %+v", *events)
			}
		})
	}
}

func TestProgressProbeSkipAndRealFailover(t *testing.T) {
	data := fixture(t)
	for _, probeFailure := range []bool{false, true} {
		deps, candidates := fakeDependencies(data)
		if probeFailure {
			candidates[0].probeErr = remoteFailure{}
		} else {
			candidates[0].snapshot = func(context.Context) (stream, error) { return stream{}, remoteFailure{} }
		}
		events := recordProgress(&deps)
		_, err := run(context.Background(), testConfig(t), "test", deps)
		must(t, err)
		var unavailable, failovers, attempts int
		for _, event := range *events {
			switch event.Stage {
			case CandidateUnavailable:
				unavailable++
			case AttemptSnapshot:
				attempts++
			case Failover:
				failovers++
				if event.Candidate != 2 || event.Total != 2 {
					t.Fatalf("wrong failover: %+v", event)
				}
			}
		}
		if probeFailure && (unavailable != 1 || attempts != 1 || failovers != 0) {
			t.Fatalf("unavailable candidate treated as attempted snapshot: %+v", *events)
		}
		if !probeFailure && (unavailable != 0 || attempts != 2 || failovers != 1) {
			t.Fatalf("missing actual failover: %+v", *events)
		}
	}
}

type timedReader struct {
	clock *time.Time
	steps []time.Duration
	final error
}

func (r *timedReader) Read(p []byte) (int, error) {
	if len(r.steps) == 0 {
		return 0, r.final
	}
	*r.clock = r.clock.Add(r.steps[0])
	r.steps = r.steps[1:]
	p[0] = 'x'
	return 1, nil
}

func TestTransferProgressThrottleFinalAndErrorPrecedence(t *testing.T) {
	for _, kind := range []string{"success", "remote", "write", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "snapshot")
			must(t, err)
			defer f.Close()
			clock := time.Unix(100, 0)
			var events []Progress
			deps := defaults()
			deps.progress = &progressReporter{observe: func(e Progress) { events = append(events, e) }, now: func() time.Time { return clock }, started: clock}
			r := &timedReader{clock: &clock, steps: []time.Duration{0, 400 * time.Millisecond, 600 * time.Millisecond, 100 * time.Millisecond}, final: io.EOF}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantCode := ""
			switch kind {
			case "remote":
				r.final = errors.New("SECRET_READ_CANARY")
			case "write", "cancel":
				deps.write = func(*os.File, []byte) (int, error) {
					if kind == "cancel" {
						cancel()
					}
					return 0, errors.New("SECRET_WRITE_CANARY")
				}
				wantCode = "backup_incomplete"
			}
			err = copySnapshot(ctx, f, r, deps)
			if kind == "success" {
				must(t, err)
			} else if kind == "remote" {
				if !isRemote(err) {
					t.Fatalf("remote precedence: %v", err)
				}
			} else if apperr.Safe(err).Code != wantCode {
				t.Fatalf("local write/cancel precedence: %v", err)
			}
			if kind == "success" || kind == "remote" {
				want := []Progress{{Stage: Receiving}, {Stage: Receiving, Bytes: 3, Elapsed: time.Second}, {Stage: Received, Bytes: 4, Elapsed: 1100 * time.Millisecond}}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("throttle/final totals: %+v", events)
				}
			} else if len(events) != 2 || events[1].Stage != Received || events[1].Bytes != 1 {
				t.Fatalf("failed write final received count: %+v", events)
			}
		})
	}
}
