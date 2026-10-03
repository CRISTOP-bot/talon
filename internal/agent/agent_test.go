package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/talon-cli/talon/internal/git"
	"github.com/talon-cli/talon/internal/index"
	"github.com/talon-cli/talon/internal/journal"
	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/perm"
	"github.com/talon-cli/talon/internal/project"
	"github.com/talon-cli/talon/internal/shell"
	"github.com/talon-cli/talon/internal/tools"
)

// stubApprover records approval requests and answers from a script.
type stubApprover struct {
	answers []Approval
	calls   []perm.Request
}

func (s *stubApprover) Approve(ctx context.Context, req perm.Request, call llm.ToolCall) (Approval, error) {
	s.calls = append(s.calls, req)
	if len(s.answers) == 0 {
		return Approval{Allow: true}, nil
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}

func newAgentFixture(t *testing.T, level perm.Level) (*Agent, *tools.Context, *llm.Mock, *stubApprover, string, *[]Event) {
	t.Helper()
	dir := t.TempDir()
	must := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n")
	must("main.go", "package main\n\nfunc main() {\n\tprintln(1)\n}\n")
	must("broken.go", "package main\n\nfunc broken( {\n")

	limits := tools.DefaultLimits()
	toolCtx := &tools.Context{
		Ctx:       context.Background(),
		Workspace: dir,
		Limits:    limits,
		Policy:    perm.New(perm.Config{Level: level, Workspace: dir}),
		Shell:     shell.NewRunner(),
		Git:       git.New(dir),
		Index:     index.New(dir),
		Journal:   journal.New(""),
		Log:       logger.Discard,
	}
	if err := toolCtx.Index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	toolCtx.Project = project.Detect(context.Background(), dir)

	mock := llm.NewMock(llm.Options{})
	registry := tools.NewRegistry()
	registry.Register(tools.All()...)

	var events []Event
	a := New(Options{
		Provider: mock,
		Tools:    registry,
		ToolCtx:  toolCtx,
		System:   "You are Talon.",
		MaxSteps: 6,
		Mode:     ModeAuto,
		Approver: &stubApprover{},
		Log:      logger.Discard,
		OnEvent:  func(ev Event) { events = append(events, ev) },
	})
	return a, toolCtx, mock, a.opts.Approver.(*stubApprover), dir, &events
}

func toolTurn(name string, args map[string]any) MockToolCallShim {
	return MockToolCallShim{Name: name, Arguments: args}
}

// MockToolCallShim keeps the test readable.
type MockToolCallShim = llm.MockToolCall

func TestAgentRunsToolThenFinishes(t *testing.T) {
	a, _, mock, _, _, events := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{
		{Text: "Let me read the file.", Tools: []llm.MockToolCall{toolTurn("read_file", map[string]any{"path": "main.go"})}},
		{Text: "The file calls println(1)."},
	}
	msg, err := a.Run(context.Background(), "what does main.go do?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if msg.Content != "The file calls println(1)." {
		t.Errorf("final message = %q", msg.Content)
	}
	if a.Steps() != 2 {
		t.Errorf("steps = %d, want 2", a.Steps())
	}
	kinds := eventKinds(*events)
	for _, want := range []EventKind{EventTurnStart, EventText, EventToolStart, EventToolResult, EventTurnEnd} {
		if !containsKind(kinds, want) {
			t.Errorf("missing event %s in %v", want, kinds)
		}
	}
}

func TestAgentExecutesMultipleToolsInOneStep(t *testing.T) {
	a, _, mock, _, _, _ := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{
			toolTurn("read_file", map[string]any{"path": "main.go"}),
			toolTurn("list_directory", map[string]any{"path": "."}),
		}},
		{Text: "done"},
	}
	if _, err := a.Run(context.Background(), "survey the project"); err != nil {
		t.Fatal(err)
	}
	if a.Steps() != 2 {
		t.Errorf("steps = %d", a.Steps())
	}
	msgs := a.Conversation().Messages()
	toolResults := 0
	for _, m := range msgs {
		if m.Role == llm.RoleTool {
			toolResults++
		}
	}
	if toolResults != 2 {
		t.Errorf("expected two tool results, got %d", toolResults)
	}
}

func TestAgentWritesFileAndRecordsUndo(t *testing.T) {
	a, toolCtx, mock, _, dir, _ := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{toolTurn("write_file", map[string]any{
			"path": "notes.md", "content": "# Notes\n",
		})}},
		{Text: "Created the notes file."},
	}
	if _, err := a.Run(context.Background(), "create notes.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Errorf("file not written: %v", err)
	}
	if toolCtx.Journal.UndoCount() != 1 {
		t.Errorf("journal entries = %d", toolCtx.Journal.UndoCount())
	}
}

func TestAgentAsksBeforeDangerousTool(t *testing.T) {
	a, _, mock, approver, _, _ := newAgentFixture(t, perm.FullAccess)
	approver.answers = []Approval{{Allow: true, Always: false}}
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{toolTurn("delete_file", map[string]any{"path": "broken.go"})}},
		{Text: "Deleted."},
	}
	if _, err := a.Run(context.Background(), "delete broken.go"); err != nil {
		t.Fatal(err)
	}
	if len(approver.calls) != 1 {
		t.Fatalf("approver called %d times", len(approver.calls))
	}
	if approver.calls[0].Tool != "delete_file" {
		t.Errorf("approved request = %+v", approver.calls[0])
	}
	if approver.calls[0].Description == "" {
		t.Error("approval prompt should carry a description")
	}
}

func TestAgentRespectsRejection(t *testing.T) {
	a, _, mock, approver, dir, _ := newAgentFixture(t, perm.FullAccess)
	approver.answers = []Approval{{Allow: false, Reason: "keep that file"}}
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{toolTurn("delete_file", map[string]any{"path": "broken.go"})}},
		{Text: "Understood, I left the file alone."},
	}
	msg, err := a.Run(context.Background(), "delete broken.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "broken.go")); err != nil {
		t.Error("file was deleted despite rejection")
	}
	joined := conversationText(a.Conversation().Messages())
	if !strings.Contains(joined, "keep that file") {
		t.Errorf("model was not told about the rejection:\n%s", joined)
	}
	if !strings.Contains(msg.Content, "left the file alone") {
		t.Errorf("final message = %q", msg.Content)
	}
}

func TestReadOnlyModeBlocksWrites(t *testing.T) {
	a, _, mock, _, dir, _ := newAgentFixture(t, perm.ReadOnly)
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{toolTurn("write_file", map[string]any{"path": "new.txt", "content": "x"})}},
		{Text: "I could not write the file."},
	}
	if _, err := a.Run(context.Background(), "write a file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Error("read-only mode allowed a write")
	}
	if !strings.Contains(conversationText(a.Conversation().Messages()), "Permission denied") {
		t.Error("model was not informed of the denial")
	}
}

func TestPlanModeBlocksChanges(t *testing.T) {
	a, _, mock, _, dir, events := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{
			toolTurn("read_file", map[string]any{"path": "main.go"}),
			toolTurn("write_file", map[string]any{"path": "planned.txt", "content": "x"}),
		}},
		{Text: "1. Fix the broken function\n2. Run the tests"},
	}
	plan, err := a.PlanMode(context.Background(), "how do I fix the broken file?")
	if err != nil {
		t.Fatalf("PlanMode: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("plan = %+v", plan.Steps)
	}
	if plan.Steps[0].Text != "Fix the broken function" {
		t.Errorf("step 1 = %q", plan.Steps[0].Text)
	}
	if _, err := os.Stat(filepath.Join(dir, "planned.txt")); !os.IsNotExist(err) {
		t.Error("plan mode wrote a file")
	}
	joined := conversationText(a.Conversation().Messages())
	if !strings.Contains(joined, "Plan mode is active") {
		t.Errorf("model was not told about plan mode:\n%s", joined)
	}
	// Read-only tools still work in plan mode.
	if !strings.Contains(joined, "package main") {
		t.Error("plan mode blocked read_file")
	}
	kinds := eventKinds(*events)
	if !containsKind(kinds, EventPlan) {
		t.Errorf("no plan event emitted: %v", kinds)
	}
}

func TestPlanModeFiltersTools(t *testing.T) {
	a, _, _, _, _, _ := newAgentFixture(t, perm.FullAccess)
	a.SetMode(ModePlan)
	specs := a.buildRequest().Tools
	if len(specs) == 0 {
		t.Fatal("plan mode must still expose read-only tools")
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
	}
	for _, want := range []string{"read_file", "search_text", "git_status", "inspect_project", "list_directory"} {
		if !names[want] {
			t.Errorf("plan mode dropped the read-only tool %s", want)
		}
	}
	for _, unwanted := range []string{"write_file", "edit_file", "delete_file",
		"run_command", "run_tests", "build_project", "git_commit", "git_checkout", "apply_patch"} {
		if names[unwanted] {
			t.Errorf("plan mode exposed %s", unwanted)
		}
	}
}

func TestMaxStepsStops(t *testing.T) {
	a, _, mock, _, _, _ := newAgentFixture(t, perm.FullAccess)
	mock.MaxTurns = 0
	mock.Script = nil
	// A tool call on every turn would loop forever without the limit.
	a.opts.MaxSteps = 2
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{toolTurn("read_file", map[string]any{"path": "main.go"})}},
		{Tools: []llm.MockToolCall{toolTurn("read_file", map[string]any{"path": "go.mod"})}},
	}
	_, err := a.Run(context.Background(), "loop forever")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.Steps() != 2 {
		t.Errorf("steps = %d, want 2", a.Steps())
	}
	if !strings.Contains(conversationText(a.Conversation().Messages()), "Stopped after 2 steps") {
		t.Error("model was not told the loop stopped")
	}
}

func TestUnknownToolIsReportedToModel(t *testing.T) {
	a, _, mock, _, _, _ := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{
		{Text: "trying", Tools: []llm.MockToolCall{{Name: "nope", Arguments: map[string]any{}}}},
		{Text: "ok"},
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	joined := conversationText(a.Conversation().Messages())
	if !strings.Contains(joined, "unknown tool") {
		t.Errorf("model was not told about the unknown tool:\n%s", joined)
	}
}

func TestToolFailureIsReportedToModel(t *testing.T) {
	a, _, mock, _, _, _ := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{toolTurn("read_file", map[string]any{"path": "missing.go"})}},
		{Text: "That file does not exist."},
	}
	msg, err := a.Run(context.Background(), "read missing.go")
	if err != nil {
		t.Fatalf("a failing tool must not abort the turn: %v", err)
	}
	if !strings.Contains(msg.Content, "does not exist") {
		t.Errorf("final message = %q", msg.Content)
	}
	joined := conversationText(a.Conversation().Messages())
	if !strings.Contains(joined, "error:") {
		t.Errorf("tool error not fed back:\n%s", joined)
	}
}

func TestStreamingEmitsEveryTextDelta(t *testing.T) {
	a, _, mock, _, _, events := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{{Text: "one two three four"}}
	if _, err := a.Run(context.Background(), "say something"); err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	for _, ev := range *events {
		if ev.Kind == EventText {
			streamed.WriteString(ev.Text)
		}
	}
	if streamed.String() != "one two three four" {
		t.Errorf("streamed = %q", streamed.String())
	}
}

func TestProviderErrorIsPropagated(t *testing.T) {
	a, _, mock, _, _, _ := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{{Error: "provider exploded"}}
	_, err := a.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestContextCancellationStopsTurn(t *testing.T) {
	a, _, _, _, _, _ := newAgentFixture(t, perm.FullAccess)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx, "anything"); err == nil {
		t.Error("expected a cancellation error")
	}
}

func TestCompactReplacesOldHistory(t *testing.T) {
	a, _, mock, _, _, _ := newAgentFixture(t, perm.FullAccess)
	a.memory.MaxTokens = 200
	for i := 0; i < 6; i++ {
		mock.Script = append(mock.Script, llm.MockTurn{
			Text:  fmt.Sprintf("answer %d with a reasonably long sentence to fill the context", i),
			Tools: []llm.MockToolCall{toolTurn("read_file", map[string]any{"path": "main.go"})},
		})
		if _, err := a.Run(context.Background(), fmt.Sprintf("question %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	before := a.Conversation().Len()
	dropped, err := a.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if dropped == 0 {
		t.Fatal("expected messages to be dropped")
	}
	if a.Conversation().Len() >= before {
		t.Errorf("history did not shrink: %d -> %d", before, a.Conversation().Len())
	}
	if a.Conversation().Summary() == "" {
		t.Error("summary is empty")
	}
	if !strings.Contains(conversationText(a.Conversation().Messages()), "Summary of the earlier conversation") {
		t.Error("digest not present in the message list")
	}
}

func TestExecutePlanRunsEachStep(t *testing.T) {
	a, _, mock, _, _, _ := newAgentFixture(t, perm.FullAccess)
	mock.Script = []llm.MockTurn{
		{Text: "step one done"},
		{Text: "step two done"},
	}
	plan := &Plan{Steps: []PlanStep{{Number: 1, Text: "read the file"}, {Number: 2, Text: "summarize it"}}}
	var done []string
	err := a.ExecutePlan(context.Background(), plan, ExecuteOptions{
		AfterStep: func(step PlanStep, index int, reply string) {
			done = append(done, fmt.Sprintf("%d:%s", index, reply))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 {
		t.Fatalf("completed steps = %v", done)
	}
	if !strings.Contains(done[0], "step one done") || !strings.Contains(done[1], "step two done") {
		t.Errorf("step replies = %v", done)
	}
}

func TestParsePlanVariants(t *testing.T) {
	cases := []struct {
		text  string
		steps []string
	}{
		{"1. first\n2. second", []string{"first", "second"}},
		{"1) first\n2) second", []string{"first", "second"}},
		{"- first\n- second", []string{"first", "second"}},
		{"* first\n* second", []string{"first", "second"}},
		{"[ ] first\n[ ] second", []string{"first", "second"}},
	}
	for _, c := range cases {
		p := ParsePlan(c.text)
		if len(p.Steps) != len(c.steps) {
			t.Errorf("ParsePlan(%q) = %d steps, want %d", c.text, len(p.Steps), len(c.steps))
			continue
		}
		for i, want := range c.steps {
			if p.Steps[i].Text != want {
				t.Errorf("ParsePlan(%q)[%d] = %q, want %q", c.text, i, p.Steps[i].Text, want)
			}
		}
	}
}

func TestParsePlanKeepsIntroAsSummary(t *testing.T) {
	p := ParsePlan("Here is what I found.\n\n1. do the thing\n2. verify")
	if !strings.Contains(p.Summary, "what I found") {
		t.Errorf("summary = %q", p.Summary)
	}
	if len(p.Steps) != 2 {
		t.Errorf("steps = %+v", p.Steps)
	}
}

func TestConversationBudgetHelpers(t *testing.T) {
	c := NewConversation("system prompt", 100)
	c.Append(llm.Message{Role: llm.RoleUser, Content: "hello"})
	if c.ApproxTokens() <= 0 {
		t.Error("token estimate should be positive")
	}
	c.Append(llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat("x", 3000)})
	if !c.NeedsCompaction(0.75) {
		t.Error("long history should need compaction")
	}
	c.SetSummary("earlier work", 1)
	if len(c.Summary()) == 0 || c.CompactedCount() != 1 {
		t.Errorf("summary=%q compacted=%d", c.Summary(), c.CompactedCount())
	}
	if !strings.Contains(c.Messages()[0].Content, "earlier work") {
		t.Error("digest should be the first message")
	}
	c.Reset()
	if c.Len() != 0 || c.Summary() != "" {
		t.Error("Reset should clear everything")
	}
}

func TestSliceUntilPreservesWholeTurns(t *testing.T) {
	c := NewConversation("", 1000)
	c.Append(llm.Message{Role: llm.RoleUser, Content: "u1"})
	c.Append(llm.Message{Role: llm.RoleAssistant, Content: "a1"})
	c.Append(llm.Message{Role: llm.RoleTool, Content: "t1"})
	c.Append(llm.Message{Role: llm.RoleUser, Content: "u2"})
	c.Append(llm.Message{Role: llm.RoleAssistant, Content: "a2"})
	slice := c.SliceUntil(3)
	if len(slice) != 3 || slice[0].Content != "u1" {
		t.Errorf("slice = %+v", slice)
	}
}

func eventKinds(events []Event) []EventKind {
	out := make([]EventKind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func containsKind(kinds []EventKind, want EventKind) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

func conversationText(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
		for _, c := range m.ToolCalls {
			encoded, _ := json.Marshal(c)
			b.Write(encoded)
			b.WriteString("\n")
		}
	}
	return b.String()
}
