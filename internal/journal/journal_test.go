package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordAndUndo(t *testing.T) {
	j := New("")
	seq := j.Record(Entry{Path: "a.go", Before: "old", BeforeExisted: true, After: "new", Tool: "edit_file"})
	if seq != 1 {
		t.Errorf("seq = %d", seq)
	}
	if j.UndoCount() != 1 || j.RedoCount() != 0 {
		t.Errorf("counts = %d/%d", j.UndoCount(), j.RedoCount())
	}
	entry, ok := j.Peek()
	if !ok || entry.Path != "a.go" {
		t.Errorf("peek = %+v", entry)
	}
	undone, ok := j.Undo()
	if !ok || undone.After != "new" {
		t.Errorf("undo = %+v", undone)
	}
	if j.UndoCount() != 0 || j.RedoCount() != 1 {
		t.Errorf("counts after undo = %d/%d", j.UndoCount(), j.RedoCount())
	}
	redone, ok := j.Redo()
	if !ok || redone.Path != "a.go" {
		t.Errorf("redo = %+v", redone)
	}
	if _, ok := j.Undo(); !ok {
		t.Error("a second undo should succeed")
	}
	if _, ok := j.Redo(); !ok {
		t.Error("a second redo should succeed")
	}
	if _, ok := j.Undo(); !ok {
		t.Error("undo should be possible again")
	}
	if _, ok := j.Redo(); !ok {
		t.Error("redo should be possible again")
	}
	if _, ok := j.Undo(); !ok {
		t.Error("undo/redo should be stable")
	}
	if _, ok := j.Redo(); !ok {
		t.Error("undo/redo should be stable")
	}
}

func TestUndoOnEmptyJournal(t *testing.T) {
	j := New("")
	if _, ok := j.Undo(); ok {
		t.Error("empty journal should not undo")
	}
	if _, ok := j.Redo(); ok {
		t.Error("empty journal should not redo")
	}
}

func TestNewEntryTruncatesRedo(t *testing.T) {
	j := New("")
	j.Record(Entry{Path: "a", After: "1"})
	j.Record(Entry{Path: "b", After: "2"})
	j.Undo()
	if j.RedoCount() != 1 {
		t.Fatalf("redo count = %d", j.RedoCount())
	}
	j.Record(Entry{Path: "c", After: "3"})
	if j.RedoCount() != 0 {
		t.Errorf("a new change must discard the redo stack, got %d", j.RedoCount())
	}
	// "a" and the new "c" remain; the undone "b" is gone.
	if j.Len() != 2 {
		t.Errorf("len = %d, want 2", j.Len())
	}
	if paths := j.Summary(0); len(paths) != 2 || paths[1] != "create c" {
		t.Errorf("summary = %v", paths)
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := New(path)
	j.Record(Entry{Path: "a.go", Before: "x", BeforeExisted: true, After: "y", Tool: "edit_file"})
	j.Record(Entry{Path: "b.go", After: "new file"})

	reloaded := New(path)
	entries := reloaded.Entries()
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[0].Before != "x" || entries[1].Path != "b.go" {
		t.Errorf("entries = %+v", entries)
	}
	if reloaded.UndoCount() != 2 {
		t.Errorf("undo count = %d", reloaded.UndoCount())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(data)), "\n") != 1 {
		t.Errorf("expected JSON Lines:\n%s", data)
	}
}

func TestDescriptions(t *testing.T) {
	cases := map[string]Entry{
		"create a.go": {Path: "a.go", After: "x"},
		"delete a.go": {Path: "a.go", Before: "x", BeforeExisted: true},
		"modify a.go": {Path: "a.go", Before: "x", BeforeExisted: true, After: "y"},
	}
	for want, e := range cases {
		if got := e.Description(); got != want {
			t.Errorf("Description() = %q, want %q", got, want)
		}
	}
}

func TestSummaryAndClear(t *testing.T) {
	j := New("")
	for i := 0; i < 3; i++ {
		j.Record(Entry{Path: string(rune('a' + i)), After: "x"})
	}
	if len(j.Summary(0)) != 3 {
		t.Errorf("summary = %v", j.Summary(0))
	}
	if len(j.Summary(2)) != 2 {
		t.Errorf("limited summary = %v", j.Summary(2))
	}
	j.Clear()
	if j.Len() != 0 {
		t.Error("Clear did not empty the journal")
	}
}

func TestCorruptJournalLineIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("garbage\n{\"seq\":1,\"path\":\"a\",\"after\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j := New(path)
	if j.Len() != 1 {
		t.Errorf("entries = %d", j.Len())
	}
}
