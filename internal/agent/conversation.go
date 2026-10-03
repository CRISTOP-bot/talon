package agent

import (
	"fmt"
	"strings"

	"github.com/talon-cli/talon/internal/llm"
)

// Conversation is the message history handed to the model. It estimates token
// usage and supports compaction so long sessions do not overflow the context.
type Conversation struct {
	// System is the system prompt; it is never compacted away.
	System string
	// MaxTokens is the context budget used for compaction decisions.
	MaxTokens int

	msgs []llm.Message
	// summarized holds the digest that replaced compacted messages.
	summarized string
	// compacted counts how many messages were dropped by compaction.
	compacted int
}

// NewConversation creates an empty conversation.
func NewConversation(system string, maxTokens int) *Conversation {
	if maxTokens <= 0 {
		maxTokens = 128000
	}
	return &Conversation{System: system, MaxTokens: maxTokens}
}

// Append adds a message.
func (c *Conversation) Append(m llm.Message) { c.msgs = append(c.msgs, m) }

// Messages returns the history sent to the model.
func (c *Conversation) Messages() []llm.Message {
	out := make([]llm.Message, 0, len(c.msgs)+1)
	if c.summarized != "" {
		out = append(out, llm.Message{
			Role: llm.RoleUser,
			Content: "Summary of the earlier conversation (use it as background, not as the " +
				"user's current request):\n" + c.summarized,
		})
	}
	return append(out, c.msgs...)
}

// Len returns the number of raw messages.
func (c *Conversation) Len() int { return len(c.msgs) }

// LastAssistant returns the most recent assistant message.
func (c *Conversation) LastAssistant() llm.Message {
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if c.msgs[i].Role == llm.RoleAssistant {
			return c.msgs[i]
		}
	}
	return llm.Message{}
}

// Reset clears the history.
func (c *Conversation) Reset() {
	c.msgs = nil
	c.summarized = ""
	c.compacted = 0
}

// Summary returns the compaction digest, if any.
func (c *Conversation) Summary() string { return c.summarized }

// CompactedCount returns how many messages were replaced by the digest.
func (c *Conversation) CompactedCount() int { return c.compacted }

// SetSummary installs a digest and drops the given number of leading messages.
func (c *Conversation) SetSummary(summary string, dropped int) {
	c.summarized = summary
	if dropped > len(c.msgs) {
		dropped = len(c.msgs)
	}
	if dropped > 0 {
		c.msgs = append([]llm.Message(nil), c.msgs[dropped:]...)
		c.compacted += dropped
	}
}

// Drop removes and returns the first n messages (used to slice off turns that
// were already summarised elsewhere).
func (c *Conversation) Drop(n int) []llm.Message {
	if n <= 0 || n > len(c.msgs) {
		return nil
	}
	out := append([]llm.Message(nil), c.msgs[:n]...)
	c.msgs = append([]llm.Message(nil), c.msgs[n:]...)
	return out
}

// charsPerToken is a conservative estimate for mixed source code and prose.
const charsPerToken = 3.6

// ApproxTokens estimates the size of the conversation in tokens.
func (c *Conversation) ApproxTokens() int {
	total := float64(len(c.System)) + float64(len(c.summarized))
	for _, m := range c.Messages()[0:] {
		total += float64(len(m.Content)) + float64(len(m.Reasoning)) + 20
		for _, call := range m.ToolCalls {
			total += float64(len(call.Name) + len(call.Arguments))
		}
	}
	return int(total / charsPerToken)
}

// NeedsCompaction reports whether the history exceeds the configured fraction
// of the budget.
func (c *Conversation) NeedsCompaction(fraction float64) bool {
	if fraction <= 0 || fraction >= 1 {
		fraction = 0.75
	}
	return float64(c.ApproxTokens()) > float64(c.MaxTokens)*fraction
}

// SliceUntil returns the messages before index, rounded down so that whole user
// turns are kept: compaction never splits a request from its answer.
func (c *Conversation) SliceUntil(index int) []llm.Message {
	if index <= 0 || index > len(c.msgs) {
		return nil
	}
	for index > 1 && c.msgs[index].Role != llm.RoleUser {
		index--
	}
	if index == 1 && c.msgs[0].Role != llm.RoleUser {
		return nil
	}
	return append([]llm.Message(nil), c.msgs[:index]...)
}

// Render renders the conversation as plain text (used by /context and sessions).
func (c *Conversation) Render() string {
	var b strings.Builder
	for _, m := range c.Messages() {
		switch m.Role {
		case llm.RoleUser:
			fmt.Fprintf(&b, "\nuser: %s\n", m.Content)
		case llm.RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&b, "\nassistant: %s\n", m.Content)
			}
			for _, c := range m.ToolCalls {
				fmt.Fprintf(&b, "  tool call: %s(%s)\n", c.Name, c.Arguments)
			}
		case llm.RoleTool:
			body := m.Content
			if len(body) > 400 {
				body = body[:400] + "…"
			}
			fmt.Fprintf(&b, "  tool result (%s): %s\n", m.Name, body)
		}
	}
	return strings.TrimSpace(b.String())
}

// Summarize renders a digest of the given messages without calling a model. It
// is the fallback when the provider is unavailable; otherwise the agent asks
// the model for a better summary.
func Summarize(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			line := oneLine(m.Content, 160)
			if line != "" {
				fmt.Fprintf(&b, "- user asked: %s\n", line)
			}
		case llm.RoleAssistant:
			for _, call := range m.ToolCalls {
				fmt.Fprintf(&b, "- used tool %s(%s)\n", call.Name, oneLine(call.Arguments, 100))
			}
			line := oneLine(m.Content, 160)
			if line != "" {
				fmt.Fprintf(&b, "- assistant: %s\n", line)
			}
		case llm.RoleTool:
			fmt.Fprintf(&b, "- %s returned %d chars\n", m.Name, len(m.Content))
		}
	}
	return strings.TrimSpace(b.String())
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
