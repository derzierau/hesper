package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Agent states come from hooks (Claude Code's settings.json hooks, Codex's
// hooks.json and notify) and process events:
//
//	SessionStart                       idle (starting while the first prompt is on its way)
//	UserPromptSubmit, PreToolUse,
//	PostToolUse, PreCompact            working
//	PermissionRequest                  approval (AskUserQuestion: question)
//	PreToolUse AskUserQuestion         question
//	Notification permission_prompt     approval; elicitation_dialog question;
//	                                   idle_prompt idle (done stays done)
//	Stop, Codex notify turn-complete   done, with the summary
//	StopFailure                        error
//	process exit                       exited (error when it failed on its own)
//
// Hooks run concurrently (Claude's tool hooks are async), so a waiting
// agent (approval, question) leaves that state only on what answers it:
// the asked tool's PostToolUse, a new prompt, the turn's end, agents.answer.
// Codex runs hooks late: a tool or approval event of a turn that already
// ended is dropped (turn ids).

var approvalOptions = []string{wire.Allow, wire.Always, wire.Deny}

const askTool = "AskUserQuestion"

// lateEvents are the Codex events that can arrive after their turn's Stop.
var lateEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true, "PermissionRequest": true}

// turnEvents only come from a turn that ran (the agent took a prompt).
var turnEvents = map[string]bool{"UserPromptSubmit": true, "PreToolUse": true, "PostToolUse": true, "PermissionRequest": true,
	"Stop": true, "StopFailure": true, "PreCompact": true, "notify": true}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func str(data map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := data[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// payloadSessionID: hooks name it session_id, Codex's notify thread-id.
func payloadSessionID(data map[string]any) string {
	s := str(data, "session_id", "thread-id", "thread_id", "session-id")
	if !sessionIDRe.MatchString(s) {
		return ""
	}
	return s
}

func payloadTurnID(data map[string]any) string { return str(data, "turn_id", "turn-id") }

// toolKey identifies a tool call: its name and input.
func toolKey(data map[string]any) string {
	input, _ := json.Marshal(data["tool_input"])
	return str(data, "tool_name") + "\x00" + string(input)
}

// hookEvent applies one hook event to a (the registry's lock is held).
// summary is the turn's last message for Stop/notify (read outside the
// lock).
func (r *Registry) hookEvent(a *agent, source, event string, data map[string]any, summary string) {
	if source == wire.KindCodex {
		if turn := payloadTurnID(data); turn != "" {
			if lateEvents[event] && turn == a.endedTurn {
				return
			}
			if event == "Stop" {
				a.endedTurn = turn
			}
		}
	}
	if turnEvents[event] {
		a.engaged = true // a turn ran: the task was taken
	}
	waiting := a.State == wire.StateApproval || a.State == wire.StateQuestion
	tool := str(data, "tool_name")
	// The activity line (tile headers): the tool running now; a new
	// prompt, the turn's end or a new session clear it.
	switch event {
	case "PreToolUse":
		if tool != "" && tool != askTool {
			r.setActivity(a, activityOf(tool, data["tool_input"]))
		}
	case "UserPromptSubmit", "Stop", "StopFailure", "SessionStart", "notify", "SessionEnd":
		r.setActivity(a, "")
	}
	switch event {
	case "SessionStart":
		if a.State == wire.StateStarting && a.expectPrompt {
			// The first prompt follows; idle if it does not.
			local, gen := a.local, a.gen
			time.AfterFunc(promptGrace, func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if b := r.agents[local]; b != nil && b.gen == gen && b.State == wire.StateStarting {
					b.expectPrompt = false
					r.setState(b, wire.StateIdle, nil)
				}
			})
			return
		}
		if !waiting {
			r.setState(a, wire.StateIdle, nil)
		}
	case "UserPromptSubmit":
		a.expectPrompt = false
		r.setState(a, wire.StateWorking, nil)
	case "PreToolUse":
		if tool == askTool {
			r.setQuestion(a, data)
			return
		}
		if !waiting {
			r.setState(a, wire.StateWorking, nil)
		}
	case "PostToolUse", "PostToolBatch":
		if waiting {
			if (a.State == wire.StateApproval && toolKey(data) == a.approvalKey) || (a.State == wire.StateQuestion && tool == askTool) {
				r.setState(a, wire.StateWorking, nil)
			}
			return
		}
		r.setState(a, wire.StateWorking, nil)
	case "PreCompact":
		if !waiting {
			r.setState(a, wire.StateWorking, nil)
		}
	case "PermissionRequest":
		if tool == askTool {
			r.setQuestion(a, data)
			return
		}
		if source == wire.KindCodex {
			a.codexHooks = true
		}
		r.setState(a, wire.StateApproval, &wire.Attention{
			Kind: "approval", Title: orDefault(tool, "Permission"), Detail: describeTool(tool, data["tool_input"]), Options: approvalOptions,
		})
		a.approvalKey = toolKey(data)
	case "Notification":
		message := str(data, "message")
		switch str(data, "notification_type") {
		case "permission_prompt":
			if a.State != wire.StateApproval {
				r.setState(a, wire.StateApproval, &wire.Attention{Kind: "approval", Title: "Permission", Detail: message, Options: approvalOptions})
				a.approvalKey = ""
			}
		case "elicitation_dialog":
			r.setState(a, wire.StateQuestion, &wire.Attention{Kind: "question", Title: "Question", Detail: message})
		case "idle_prompt":
			if !waiting && a.State != wire.StateDone {
				r.setState(a, wire.StateIdle, nil)
			}
		}
	case "Stop":
		a.expectPrompt = false
		if summary != "" {
			a.Summary = summary
		}
		r.setState(a, wire.StateDone, nil)
	case "StopFailure":
		detail := str(data, "error", "message", "reason")
		if detail == "" {
			if e, ok := data["error"].(map[string]any); ok {
				detail = str(e, "message", "type")
			}
		}
		r.setState(a, wire.StateError, &wire.Attention{Kind: "error", Title: "Agent error", Detail: firstLine(detail, 300)})
	case "notify":
		// Codex's notify program: {"type":"agent-turn-complete", ...}.
		if str(data, "type") != "agent-turn-complete" {
			return
		}
		if turn := payloadTurnID(data); turn != "" {
			a.endedTurn = turn
		}
		if summary != "" {
			a.Summary = summary
		}
		r.setState(a, wire.StateDone, nil)
	}
}

// setActivity changes an agent's activity line (the lock is held).
func (r *Registry) setActivity(a *agent, activity string) {
	if a.Activity == activity {
		return
	}
	a.Activity = activity
	r.changed(a)
}

// activityOf is one short line about a tool call: "Bash: git push".
func activityOf(tool string, input any) string {
	detail := describeTool(tool, input)
	if detail == "" {
		return firstLine(tool, 80)
	}
	return firstLine(tool+": "+detail, 80)
}

// promptGrace is how long a fresh agent stays starting after SessionStart
// when its first prompt does not arrive.
var promptGrace = 5 * time.Second

func (r *Registry) setQuestion(a *agent, data map[string]any) {
	detail := ""
	if input, ok := data["tool_input"].(map[string]any); ok {
		if qs, ok := input["questions"].([]any); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]any); ok {
				detail = str(q, "question", "header")
			}
		}
		if detail == "" {
			detail = str(input, "question")
		}
	}
	r.setState(a, wire.StateQuestion, &wire.Attention{Kind: "question", Title: "Question", Detail: firstLine(detail, 300)})
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// describeTool is one line about a tool call for an approval's detail: the
// command, the file, the pattern.
func describeTool(name string, input any) string {
	args, _ := input.(map[string]any)
	field := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := args[k]; ok && v != nil {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}
	command := args["command"]
	if command == nil {
		command = args["cmd"]
	}
	if list, ok := command.([]any); ok {
		parts := make([]string, 0, len(list))
		for _, p := range list {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		// Codex wraps commands in bash -lc "…".
		if len(parts) == 3 && (parts[0] == "bash" || parts[0] == "zsh" || parts[0] == "sh") && strings.HasPrefix(parts[1], "-") {
			parts = parts[2:]
		}
		command = strings.Join(parts, " ")
	}
	if s, _ := command.(string); s != "" {
		return firstLine(s, 300)
	}
	switch {
	case field("file_path", "notebook_path", "path") != "":
		return field("file_path", "notebook_path", "path")
	case field("pattern") != "":
		return field("pattern")
	case field("url", "query") != "":
		return field("url", "query")
	case field("description", "prompt") != "":
		return firstLine(field("description", "prompt"), 300)
	}
	if patch := field("input", "patch"); patch != "" {
		if m := patchFile.FindStringSubmatch(patch); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

var patchFile = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)

func firstLine(s string, limit int) string {
	s = strings.TrimSpace(s)
	s, _, _ = strings.Cut(s, "\n")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit-1]) + "…"
	}
	return s
}

// summarize is the last useful line of a turn's final message.
func summarize(message string) string {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		line = strings.TrimLeft(line, "#*->` ")
		line = strings.TrimRight(line, "*` ")
		if line != "" && line != "---" {
			return firstLine(line, 200)
		}
	}
	return ""
}

// lastMessage is the agent's final text of the turn: from the payload, else
// from the end of its transcript.
func lastMessage(data map[string]any) string {
	if s := str(data, "last_assistant_message", "last-assistant-message"); strings.TrimSpace(s) != "" {
		return s
	}
	path := str(data, "transcript_path")
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	const tail = 256 << 10
	off := max(st.Size()-tail, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return ""
	}
	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &entry) != nil || entry.Type != "assistant" {
			continue
		}
		if text := textContent(entry.Message.Content); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func textContent(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if (p.Type == "text" || p.Type == "output_text") && p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}
