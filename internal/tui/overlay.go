package tui

import (
	"fmt"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/term"
)

// OverlayKind is which picker, if any, is open above the conversation.
type OverlayKind int

// The overlays Talon supports.
const (
	OverlayNone OverlayKind = iota
	// OverlayFiles is the "@" file picker.
	OverlayFiles
	// OverlayCommands is the "/" command palette.
	OverlayCommands
)

// Overlay is a list picker drawn above the body.
type Overlay struct {
	Kind  OverlayKind
	Query string
	Items []string
	// Descriptions are shown next to items when present.
	Descriptions map[string]string
	Index        int
	// Prompt is the label shown in the box title.
	Prompt string
	// ranked remembers the query the items were last ranked for, so moving the
	// selection does not re-run the ranker and reset the cursor.
	ranked string
}

// Move moves the selection by delta, clamped to the visible window.
func (o *Overlay) Move(delta int) {
	if len(o.Items) == 0 {
		return
	}
	o.Index += delta
	if o.Index < 0 {
		o.Index = 0
	}
	if o.Index >= len(o.Items) {
		o.Index = len(o.Items) - 1
	}
}

// Selected returns the highlighted item, if any.
func (o *Overlay) Selected() (string, bool) {
	if o.Index < 0 || o.Index >= len(o.Items) {
		return "", false
	}
	return o.Items[o.Index], true
}

// Handle applies a key press to the overlay. It returns true when the overlay
// consumed the key; false means the caller should treat it as normal input.
func (o *Overlay) Handle(ev term.Event) (handled bool, accepted bool, value string) {
	switch ev.Key {
	case term.KeyRune:
		if ev.Alt {
			return true, false, ""
		}
		o.Query += string(ev.Rune)
		o.Index = 0
		return true, false, ""
	case term.KeyBackspace:
		if o.Query == "" {
			return false, false, ""
		}
		runes := []rune(o.Query)
		o.Query = string(runes[:len(runes)-1])
		o.Index = 0
		return true, false, ""
	case term.KeyUp:
		o.Move(-1)
		return true, false, ""
	case term.KeyDown:
		o.Move(1)
		return true, false, ""
	case term.KeyEnter:
		sel, ok := o.Selected()
		if !ok {
			return true, false, ""
		}
		return true, true, sel
	case term.KeyEsc:
		return true, false, ""
	case term.KeyCtrlC:
		return true, false, ""
	}
	return true, false, ""
}

// Lines renders the overlay as a boxed panel.
func (o *Overlay) Lines(p Palette, width, height int) []Line {
	inner := width - 4
	if inner < 20 {
		inner = 20
	}
	visible := height - 8
	if visible < 3 {
		visible = 3
	}
	if len(o.Items) > visible {
		visible = min(visible, len(o.Items))
	}
	start := 0
	if o.Index >= visible {
		start = o.Index - visible + 1
	}
	body := make([]Line, 0, visible+1)
	query := Line{
		{Text: "  " + o.Prompt, Style: p.Muted},
		{Text: o.Query, Style: p.Text},
		{Text: "▏", Style: p.Accent},
	}
	body = append(body, query)
	if len(o.Items) == 0 {
		body = append(body, Line{{Text: "  no matches", Style: p.Muted}})
	}
	for i := start; i < len(o.Items) && i < start+visible; i++ {
		item := o.Items[i]
		line := Highlight(p, o.Query, item, i == o.Index)
		if desc, ok := o.Descriptions[item]; ok && desc != "" {
			line = append(line, Span{Text: "  " + desc, Style: p.Muted})
		}
		body = append(body, line)
	}
	return Box(p, width, "", body)
}

// FilesOverlay builds the "@" picker over a list of project paths.
func FilesOverlay(paths []string) *Overlay {
	return &Overlay{
		Kind:   OverlayFiles,
		Items:  paths,
		Index:  0,
		Prompt: "file",
	}
}

// CommandsOverlay builds the "/" palette.
func CommandsOverlay(names []string, descriptions map[string]string) *Overlay {
	items := make([]string, 0, len(names))
	for _, n := range names {
		items = append(items, "/"+n)
	}
	return &Overlay{
		Kind:         OverlayCommands,
		Items:        items,
		Descriptions: descriptions,
		Index:        0,
		Prompt:       "command",
	}
}

// ApplyQuery re-ranks the items when the query changed since the last call.
// It reports whether the ranking changed.
func (o *Overlay) ApplyQuery() bool {
	if o.Query == o.ranked {
		return false
	}
	previous := ""
	if o.Index >= 0 && o.Index < len(o.Items) {
		previous = o.Items[o.Index]
	}
	o.Items = Rank(o.Query, o.Items)
	o.ranked = o.Query
	o.Index = 0
	// Keep the highlighted item selected when it survived the re-ranking.
	if previous != "" {
		for i, item := range o.Items {
			if item == previous {
				o.Index = i
				break
			}
		}
	}
	return true
}

// InsertAt returns the string to insert into the editor for the given item,
// with the right decoration for the kind of overlay.
func (o *Overlay) InsertAt(item string) string {
	if o.Kind == OverlayFiles {
		return "@" + item + " "
	}
	// Commands run as they are; a trailing space would be sent to the handler.
	return strings.TrimSpace(item)
}

// Describe renders the hint line shown under the editor.
func (o *Overlay) Describe() string {
	switch o.Kind {
	case OverlayFiles:
		return fmt.Sprintf("%d file(s) · enter insert · esc close", len(o.Items))
	case OverlayCommands:
		return fmt.Sprintf("%d command(s) · enter run · esc close", len(o.Items))
	}
	return ""
}
