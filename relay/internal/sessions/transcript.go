package sessions

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Transcript items: one agent's transcript as the phone draws it
// (docs/rebuild-contract.md, "Transcript items"). A line is parsed on
// its own with the decode structs of shared history; every item of a
// line carries the line's byte offset as its Seq. What shared history
// drops (thinking, meta and sidechain lines, Codex reasoning and token
// events, injected user text) is dropped here too.

// A result's text is clipped to resultLines lines or resultBytes bytes,
// whichever comes first; a summary to summaryRunes runes.
const (
	resultLines  = 60
	resultBytes  = 4096
	summaryRunes = 80
)

// itemsFrom parses one transcript line at byte offset off. kind is
// wire.KindClaude or wire.KindCodex; a line without anything to show
// (or that does not decode) has no items. Views key items by Seq, so the
// n-th item of a line (from 0) gets off+n: a line is longer than its
// item count, so this stays below the next line's offset.
func itemsFrom(kind string, line []byte, off int64) []wire.TranscriptItem {
	var items []wire.TranscriptItem
	switch kind {
	case wire.KindClaude:
		items = claudeItems(line, off)
	case wire.KindCodex:
		items = codexItems(line, off)
	}
	for i := range items {
		items[i].Seq = off + int64(i)
	}
	return items
}

func claudeItems(line []byte, off int64) []wire.TranscriptItem {
	for _, p := range claudeSkip {
		if bytes.HasPrefix(line, p) {
			return nil
		}
	}
	var l claudeLine
	if json.Unmarshal(line, &l) != nil || l.Message == nil || l.IsSidechain {
		return nil
	}
	at := parseTime(l.Timestamp)
	var items []wire.TranscriptItem
	switch l.Type {
	case "user":
		if l.IsCompactSummary {
			return []wire.TranscriptItem{{Seq: off, At: at, Kind: wire.ItemSystem, Text: "Context compacted"}}
		}
		if l.IsMeta {
			return nil
		}
		raw := l.Message.Content
		if len(raw) > 0 && raw[0] == '[' {
			var blocks []contentBlock
			if json.Unmarshal(raw, &blocks) != nil {
				return nil
			}
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				text, cut := clipResult(resultText(b.Content))
				ok := !b.IsError
				items = append(items, wire.TranscriptItem{Seq: off, At: at, Kind: wire.ItemResult,
					Of: b.ToolUseID, Text: text, Truncated: cut, OK: &ok})
			}
		}
		if text, ok := claudeUserText(raw); ok {
			items = append(items, wire.TranscriptItem{Seq: off, At: at, Kind: wire.ItemUser, Text: text})
		}
	case "assistant":
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return nil
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					items = append(items, wire.TranscriptItem{Seq: off, At: at, Kind: wire.ItemAssistant, Text: t})
				}
			case "tool_use":
				items = append(items, wire.TranscriptItem{Seq: off, At: at, Kind: wire.ItemTool,
					Tool: b.Name, Summary: toolSummary(b.Name, b.Input), ID: b.ID})
			}
		}
	}
	return items
}

// resultText is a tool_result's content: a string, or its text blocks
// joined by newlines with "[image]" for each image.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

func codexItems(line []byte, off int64) []wire.TranscriptItem {
	top, inner := payloadType(line)
	if top != "response_item" {
		return nil
	}
	at := lineTime(line)
	switch inner {
	case "message":
		var l struct {
			Payload codexMessage `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil {
			return nil
		}
		role := l.Payload.Role
		if role != "user" && role != "assistant" {
			return nil
		}
		var parts []string
		for _, c := range l.Payload.Content {
			t := strings.TrimSpace(c.Text)
			if t == "" || role == "user" && codexContext(t) {
				continue
			}
			parts = append(parts, t)
		}
		if len(parts) == 0 {
			return nil
		}
		kind := wire.ItemUser
		if role == "assistant" {
			kind = wire.ItemAssistant
		}
		return []wire.TranscriptItem{{Seq: off, At: at, Kind: kind, Text: strings.Join(parts, "\n")}}
	case "function_call", "function_call_output":
		var l struct {
			Payload codexCall `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil {
			return nil
		}
		p := l.Payload
		if inner == "function_call" {
			return []wire.TranscriptItem{{Seq: off, At: at, Kind: wire.ItemTool,
				Tool: p.Name, Summary: toolSummary(p.Name, json.RawMessage(p.Arguments)), ID: p.CallID}}
		}
		// Codex records no error flag on an output.
		text, cut := clipResult(p.Output)
		ok := true
		return []wire.TranscriptItem{{Seq: off, At: at, Kind: wire.ItemResult,
			Of: p.CallID, Text: text, Truncated: cut, OK: &ok}}
	}
	return nil
}

// toolSummary says in one short line what a tool call works on: the
// file's base name for Read, Edit, Write and NotebookEdit; the command's
// first line for Bash and Codex's shell (whose command is an argv,
// joined by spaces); the pattern for Grep and Glob; the description for
// Agent and Task. Other tools have none.
func toolSummary(name string, input json.RawMessage) string {
	var in struct {
		FilePath     string          `json:"file_path"`
		Path         string          `json:"path"`
		NotebookPath string          `json:"notebook_path"`
		Command      json.RawMessage `json:"command"`
		Pattern      string          `json:"pattern"`
		Description  string          `json:"description"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var s string
	switch name {
	case "Read", "Edit", "Write", "NotebookEdit":
		p := in.FilePath
		if p == "" {
			p = in.Path
		}
		if p == "" {
			p = in.NotebookPath
		}
		if p != "" {
			s = filepath.Base(p)
		}
	case "Bash", "shell":
		var argv []string
		if json.Unmarshal(in.Command, &s) != nil && json.Unmarshal(in.Command, &argv) == nil {
			s = strings.Join(argv, " ")
		}
	case "Grep", "Glob":
		s = in.Pattern
	case "Agent", "Task":
		s = in.Description
	}
	s = oneLine(s)
	if utf8.RuneCountInString(s) > summaryRunes {
		s = clip(s, summaryRunes-1) // and "…": summaryRunes in all
	}
	return s
}

// clipResult clips a result's text to resultLines lines or resultBytes
// bytes, whichever comes first, never inside a rune; the bool is whether
// it was clipped.
func clipResult(text string) (string, bool) {
	cut := false
	i, n := 0, 0
	for n < resultLines {
		j := strings.IndexByte(text[i:], '\n')
		if j < 0 {
			break
		}
		i += j + 1
		n++
	}
	// A newline ending the last kept line is not a line of its own.
	if n == resultLines && i < len(text) {
		text, cut = text[:i-1], true
	}
	if len(text) > resultBytes {
		end := resultBytes
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text, cut = text[:end], true
	}
	return text, cut
}
