package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knowledge-mcp/internal/logging"
)

func TestUploadTaskManager_RestartMarksInterruptedAsError(t *testing.T) {
	dir := t.TempDir()
	logger := logging.NewNopLogger()

	// A persisted done task must be preserved unchanged across a restart.
	doneID := "task-done-manual"
	doneJSON := `{"id":"task-done-manual","fileName":"done.md","kbName":"kb","status":"done","slug":"done-slug","createdAt":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, doneID+".json"), []byte(doneJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// A persisted processing task cannot be resumed and must become a terminal
	// error with its identity intact.
	procID := "task-processing-manual"
	procJSON := `{"id":"task-processing-manual","fileName":"proc.pdf","kbName":"kb2","status":"processing","createdAt":"2026-01-02T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, procID+".json"), []byte(procJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// A persisted error task must be preserved exactly (same message/identity).
	errID := "task-error-manual"
	errMsgPersisted := "previous processing failure: parser exploded"
	errJSON := `{"id":"task-error-manual","fileName":"bad.md","kbName":"kb3","status":"error","error":"previous processing failure: parser exploded","createdAt":"2026-01-03T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, errID+".json"), []byte(errJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a pending task, then simulate a service restart.
	m := NewUploadTaskManager(dir, logger)
	pending := m.Create("report.md", "kb", filepath.Join(dir, "tmp-upload"))

	m2 := NewUploadTaskManager(dir, logger)
	restored := m2.Get(pending.ID)
	if restored == nil {
		t.Fatal("interrupted task record was dropped instead of preserved")
	}
	status, _, errMsg := restored.Snapshot()
	if status != "error" {
		t.Fatalf("interrupted task status=%q, want error", status)
	}
	if errMsg == "" || !strings.Contains(errMsg, "re-upload") {
		t.Fatalf("interrupted task needs explicit re-upload guidance, got %q", errMsg)
	}
	select {
	case <-restored.Done():
	default:
		t.Fatal("Done() must be closed for an interrupted task so waiters do not block")
	}

	// Processing record: preserved as error, identity intact, persisted.
	proc := m2.Get(procID)
	if proc == nil {
		t.Fatal("processing task was dropped on restart")
	}
	if proc.FileName != "proc.pdf" || proc.KBName != "kb2" {
		t.Fatalf("processing task identity changed: fileName=%q kbName=%q", proc.FileName, proc.KBName)
	}
	pStatus, _, pErr := proc.Snapshot()
	if pStatus != "error" {
		t.Fatalf("processing task status=%q, want error", pStatus)
	}
	if !strings.Contains(pErr, "processing") || !strings.Contains(pErr, "re-upload") {
		t.Fatalf("processing task needs interrupted/re-upload guidance, got %q", pErr)
	}
	select {
	case <-proc.Done():
	default:
		t.Fatal("Done() must be closed for a converted processing task")
	}
	rawProc, err := os.ReadFile(filepath.Join(dir, procID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var persistedProc UploadTask
	if err := json.Unmarshal(rawProc, &persistedProc); err != nil {
		t.Fatal(err)
	}
	if persistedProc.Status != "error" || persistedProc.Error == "" {
		t.Fatalf("processing transition not persisted: status=%q error=%q", persistedProc.Status, persistedProc.Error)
	}

	// Existing error record: preserved exactly.
	e := m2.Get(errID)
	if e == nil {
		t.Fatal("error task was dropped on restart")
	}
	if e.FileName != "bad.md" || e.KBName != "kb3" {
		t.Fatalf("error task identity changed: fileName=%q kbName=%q", e.FileName, e.KBName)
	}
	eStatus, _, eErr := e.Snapshot()
	if eStatus != "error" || eErr != errMsgPersisted {
		t.Fatalf("error task mutated on restart: status=%q err=%q", eStatus, eErr)
	}
	select {
	case <-e.Done():
	default:
		t.Fatal("Done() must be closed for a restored error task")
	}

	// Terminal task preserved.
	d := m2.Get(doneID)
	if d == nil {
		t.Fatal("done task was dropped on restart")
	}
	if got, slug, _ := d.Snapshot(); got != "done" || slug != "done-slug" {
		t.Fatalf("done task mutated on restart: status=%q slug=%q", got, slug)
	}

	// The transition must be persisted to disk.
	raw, err := os.ReadFile(filepath.Join(dir, pending.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted UploadTask
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "error" || persisted.Error == "" {
		t.Fatalf("persisted transition missing: status=%q error=%q", persisted.Status, persisted.Error)
	}

	// A second reload must be idempotent: same errors, still terminal.
	m3 := NewUploadTaskManager(dir, logger)
	r3 := m3.Get(pending.ID)
	if r3 == nil {
		t.Fatal("error task was not reloaded")
	}
	status3, _, errMsg3 := r3.Snapshot()
	if status3 != "error" || errMsg3 != errMsg {
		t.Fatalf("reload changed terminal transition: status=%q err=%q (want error/%q)", status3, errMsg3, errMsg)
	}
	select {
	case <-r3.Done():
	default:
		t.Fatal("Done() must stay closed after repeated reload")
	}

	// Idempotence for the processing->error conversion and the existing error.
	proc3 := m3.Get(procID)
	if proc3 == nil {
		t.Fatal("converted processing task was not reloaded")
	}
	pStatus3, _, pErr3 := proc3.Snapshot()
	if pStatus3 != "error" || pErr3 != pErr {
		t.Fatalf("reload changed processing transition: status=%q err=%q (want error/%q)", pStatus3, pErr3, pErr)
	}
	e3 := m3.Get(errID)
	if e3 == nil {
		t.Fatal("error task was not reloaded")
	}
	eStatus3, _, eErr3 := e3.Snapshot()
	if eStatus3 != "error" || eErr3 != errMsgPersisted {
		t.Fatalf("reload changed error task: status=%q err=%q", eStatus3, eErr3)
	}
}

func TestUploadTaskManager_RestartIgnoresCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewUploadTaskManager(dir, logging.NewNopLogger())
	if got := len(m.tasks); got != 0 {
		t.Fatalf("corrupt file should be skipped, loaded %d tasks", got)
	}
}
