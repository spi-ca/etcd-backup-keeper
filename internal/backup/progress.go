package backup

import "time"

// Stage identifies a backup milestone. Events describe work starting or in
// progress, except CandidateUnavailable, AttemptFailed, Received and Complete,
// which report facts.
type Stage uint8

const (
	ProbeCandidate Stage = iota + 1
	CandidateUnavailable
	AttemptSnapshot
	AttemptFailed
	Failover
	Receiving
	Received
	ChecksumSnapshot
	InspectSnapshot
	PublishSnapshot
	SyncRoot
	Retention
	Complete
)

// Progress contains only bounded operational facts, never endpoint, credential,
// snapshot content, local path or error text. Candidate is a one-based index in
// the configured endpoints; Total is their count. Bytes is cumulative per
// snapshot attempt, not across failovers. Elapsed spans the whole operation.
type Progress struct {
	Stage     Stage
	Candidate int
	Total     int
	Bytes     int64
	Elapsed   time.Duration
}

// progressReporter is synchronous: it needs no worker, timer or synchronization.
type progressReporter struct {
	observe func(Progress)
	now     func() time.Time
	started time.Time
}

func (d dependencies) emit(event Progress) {
	if d.progress == nil {
		return
	}
	event.Elapsed = d.progress.now().Sub(d.progress.started)
	d.progress.observe(event)
}
