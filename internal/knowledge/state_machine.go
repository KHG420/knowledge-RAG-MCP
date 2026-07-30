// Package knowledge — task state machine for index operations.
//
// The state machine tracks every document-level index operation (upload,
// rebuild, delete) through a sequence of states. Each transition is
// idempotent: re-running a step that already completed is safe.
//
// States for upload / rebuild:
//
//	pending → parsing → chunking → embedding → writing → verifying → active
//	                ↘          ↘          ↘          ↘         ↘
//	                 failed (retryable from last completed step)
//
// States for delete:
//
//	pending → tombstoning → tombstoned → cleaning → cleaned
//	              ↘             ↘           ↘
//	              failed
//
// Persistence: task state is serialized to TASK.json in the document
// directory so the state machine survives process restarts. On restart,
// any task not in a terminal state (active, cleaned, failed_permanent)
// is resumed from its last completed step.
package knowledge

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── Task state definitions ────────────────────────────────────────────────────

// TaskState is a discrete state in the index task lifecycle.
type TaskState string

const (
	// upload / rebuild states
	TaskPending    TaskState = "pending"    // task created, not yet started
	TaskParsing    TaskState = "parsing"    // parsing source file to text
	TaskChunking   TaskState = "chunking"   // splitting text into chunks
	TaskEmbedding  TaskState = "embedding"  // generating embedding vectors
	TaskWriting    TaskState = "writing"    // writing chunks + index to storage
	TaskVerifying  TaskState = "verifying"  // verifying chunk checksums
	TaskActive     TaskState = "active"     // index is live and searchable

	// delete states
	TaskTombstoning  TaskState = "tombstoning"  // adding tombstone record
	TaskTombstoned   TaskState = "tombstoned"   // tombstone active, search hidden
	TaskCleaning     TaskState = "cleaning"     // physically removing files
	TaskCleaned      TaskState = "cleaned"       // fully removed

	// terminal error states
	TaskFailed         TaskState = "failed"          // retryable failure
	TaskFailedPermanent TaskState = "failed_permanent" // non-retryable (e.g. parse error)
)

// IsTerminal reports whether this state is terminal (no further transitions).
func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskActive, TaskCleaned, TaskFailedPermanent:
		return true
	}
	return false
}

// IsRetryable reports whether a failed task can be retried.
func (s TaskState) IsRetryable() bool {
	return s == TaskFailed
}

// CanTransitionTo checks whether the state machine allows transition to target.
func (s TaskState) CanTransitionTo(target TaskState) bool {
	// Allow retrying from failed states back to any earlier state.
	if s == TaskFailed && target != TaskFailed && target != TaskFailedPermanent {
		return true
	}

	transitions := map[TaskState][]TaskState{
		TaskPending:     {TaskParsing, TaskFailed, TaskFailedPermanent},
		TaskParsing:     {TaskChunking, TaskFailed, TaskFailedPermanent},
		TaskChunking:    {TaskEmbedding, TaskWriting, TaskFailed, TaskFailedPermanent}, // Writing can be skipped if no embedder
		TaskEmbedding:   {TaskWriting, TaskFailed, TaskFailedPermanent},
		TaskWriting:     {TaskVerifying, TaskFailed, TaskFailedPermanent},
		TaskVerifying:   {TaskActive, TaskFailed, TaskFailedPermanent},
		TaskActive:      {TaskTombstoning}, // only delete can move away from active

		TaskTombstoning: {TaskTombstoned, TaskFailed, TaskFailedPermanent},
		TaskTombstoned:  {TaskCleaning},
		TaskCleaning:    {TaskCleaned, TaskFailed},
		TaskCleaned:     {},
	}

	for _, allowed := range transitions[s] {
		if target == allowed {
			return true
		}
	}
	return false
}

// ── Task record ───────────────────────────────────────────────────────────────

// TaskRecord is the persistent representation of an index task.
// It is serialized to TASK.json in the document directory.
type TaskRecord struct {
	DocSlug     string    `json:"doc_slug"`
	State       TaskState `json:"state"`
	Attempt     int       `json:"attempt"`      // retry count (0-based first attempt)
	MaxAttempts int       `json:"max_attempts"` // 0 = unlimited
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Checkpoint data: each step records its output so retries can skip
	// already-completed work.
	ParsedText    string `json:"parsed_text,omitempty"`    // result of parsing step
	SourceHash    string `json:"source_hash,omitempty"`    // hash of source file
	TextHash      string `json:"text_hash,omitempty"`      // hash of parsed text
	ManifestJSON  string `json:"manifest_json,omitempty"`  // serialized ChunkManifest
	NewVersion    int    `json:"new_version,omitempty"`    // index version being built
	TombstoneTTL  int64  `json:"tombstone_ttl,omitempty"`  // TTL for tombstone (0 = default)
}

// DefaultMaxAttempts is the default retry limit for index tasks.
const DefaultMaxAttempts = 3

// NewTaskRecord creates a new task record in pending state.
func NewTaskRecord(docSlug string) *TaskRecord {
	now := time.Now().Truncate(time.Second)
	return &TaskRecord{
		DocSlug:     docSlug,
		State:       TaskPending,
		Attempt:     0,
		MaxAttempts: DefaultMaxAttempts,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// Transition attempts to move the task to a new state. Returns an error if
// the transition is not allowed.
func (t *TaskRecord) Transition(target TaskState) error {
	if !t.State.CanTransitionTo(target) {
		return fmt.Errorf("task %q: cannot transition from %s to %s", t.DocSlug, t.State, target)
	}
	t.State = target
	t.UpdatedAt = time.Now().Truncate(time.Second)
	return nil
}

// RecordError logs an error and transitions to failed state. If attempts
// exceed MaxAttempts, transitions to failed_permanent instead.
// Passing a nil error is a no-op.
func (t *TaskRecord) RecordError(err error) {
	if err == nil {
		return
	}
	t.LastError = err.Error()
	if t.MaxAttempts > 0 && t.Attempt >= t.MaxAttempts {
		t.State = TaskFailedPermanent
	} else {
		t.State = TaskFailed
	}
	t.UpdatedAt = time.Now().Truncate(time.Second)
}

// Retry increments the attempt counter and transitions back to pending.
// Returns an error if the task has exceeded MaxAttempts or is not in a
// retryable state.
func (t *TaskRecord) Retry() error {
	if !t.State.IsRetryable() {
		return fmt.Errorf("task %q: cannot retry from state %s", t.DocSlug, t.State)
	}
	if t.MaxAttempts > 0 && t.Attempt >= t.MaxAttempts {
		return fmt.Errorf("task %q: max attempts (%d) exceeded", t.DocSlug, t.MaxAttempts)
	}
	t.Attempt++
	t.LastError = ""
	t.State = TaskPending
	t.UpdatedAt = time.Now().Truncate(time.Second)
	return nil
}

// ResumePoint returns the state from which the task should resume.
// If the task is in a failed state, it determines the last successfully
// completed step and returns the state that step transitions to.
//
// This enables "resume from last checkpoint" semantics.
func (t *TaskRecord) ResumePoint() TaskState {
	if t.State != TaskFailed {
		return t.State
	}
	// Determine what was completed based on available checkpoint data.
	if t.ManifestJSON != "" {
		return TaskVerifying // manifest was written, just need to verify & activate
	}
	if t.ParsedText != "" {
		return TaskChunking // text was parsed, need to chunk
	}
	return TaskParsing // start from parsing
}

// MarshalJSON serializes the task record to indented JSON.
func (t *TaskRecord) MarshalJSON() ([]byte, error) {
	type Alias TaskRecord
	return json.MarshalIndent((*Alias)(t), "", "  ")
}

// UnmarshalTaskRecord deserializes a TaskRecord from JSON bytes.
func UnmarshalTaskRecord(data []byte) (*TaskRecord, error) {
	var t TaskRecord
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// TaskFilename is the conventional name for the task state file.
const TaskFilename = "TASK.json"
