package sessions

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Parsing: both CLIs write one JSON object per line and only ever append,
// so a transcript is read once and then from its last complete line on.
// Accum is everything the index keeps of a transcript so far; it is
// checkpointed with the file's offset (files table) so a restart goes on
// where it stopped. Only what search and the "where you left off" card
// need is kept: user prompts and assistant text (never tool output),
// truncated and capped.

// Caps on what is kept per session (index size: see the contract).
const (
	maxPiece       = 1000      // runes of one prompt
	maxAnswerPiece = 600       // runes of one answer
	maxPrompts     = 8 << 10   // bytes of prompt text per session
	maxAnswers     = 12 << 10  // bytes of answer text per session
	maxCard        = 2000      // runes of firstPrompt / lastUser / lastAssistant
	maxTitle       = 200       // runes
	maxTodos       = 50        // items
	maxLine        = 256 << 20 // a line longer than this is skipped
)

// pieceSep separates kept prompts (answers) in the index's text columns:
// U+2029 PARAGRAPH SEPARATOR, which FTS5's tokenizer treats as a
// separator and transcripts practically never contain.
const pieceSep = "\n \n"

// Accum is a transcript's state while it is read.
type Accum struct {
	Kind      string `json:"kind"`
	SessionID string `json:"sid"`
	Cwd       string `json:"cwd,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Origin    string `json:"origin,omitempty"`
	// Title sources, best first (Claude): /rename, the Remote Control
	// name, Claude's own title, a summary. Codex: the thread name.
	CustomTitle string `json:"ct,omitempty"`
	AgentName   string `json:"an,omitempty"`
	AITitle     string `json:"at,omitempty"`
	Summary     string `json:"su,omitempty"`
	ThreadName  string `json:"tn,omitempty"`

	FirstPrompt   string             `json:"fp,omitempty"`
	LastUser      string             `json:"lu,omitempty"`
	LastAssistant string             `json:"la,omitempty"`
	Todos         []wire.SessionTodo `json:"todos,omitempty"`
	// Claude's task tools (TaskCreate / TaskUpdate): ids are given in
	// order of creation.
	Tasks []task `json:"tasks,omitempty"`
	Turns int    `json:"turns,omitempty"`
	// Tokens: input (without cache reads) + output; Claude counts each
	// message once (its blocks come as several lines).
	Tokens    int64  `json:"tok,omitempty"`
	LastMsgID string `json:"lm,omitempty"`
	LastMsgT  int64  `json:"lmt,omitempty"`

	StartedAt    int64 `json:"st,omitempty"` // Unix ms
	LastActivity int64 `json:"la_t,omitempty"`

	// Prompts and Answers are the searchable text: pieces, the first
	// prompt kept, the oldest others dropped when over the cap.
	Prompts []string `json:"-"`
	Answers []string `json:"-"`
}

type task struct {
	ID      string `json:"id"`
	Subject string `json:"s"`
	Status  string `json:"st,omitempty"`
}

// Title is the session's title: the best explicit one, else its first
// prompt's first line.
func (a *Accum) Title() string {
	for _, t := range []string{a.CustomTitle, a.AgentName, a.ThreadName, a.AITitle, a.Summary} {
		if t = strings.TrimSpace(t); t != "" {
			return clip(oneLine(t), maxTitle)
		}
	}
	return clip(oneLine(a.FirstPrompt), maxTitle)
}

// todos are the last todo list / plan / task state.
func (a *Accum) todos() []wire.SessionTodo {
	if len(a.Tasks) > 0 {
		out := make([]wire.SessionTodo, 0, len(a.Tasks))
		for _, t := range a.Tasks {
			if t.Status == "deleted" {
				continue
			}
			out = append(out, wire.SessionTodo{Text: t.Subject, Done: t.Status == "completed"})
		}
		return out
	}
	return a.Todos
}

func (a *Accum) seen(ts int64) {
	if ts <= 0 {
		return
	}
	if a.StartedAt == 0 || ts < a.StartedAt {
		a.StartedAt = ts
	}
	if ts > a.LastActivity {
		a.LastActivity = ts
	}
}

func (a *Accum) prompt(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.Turns++
	if a.FirstPrompt == "" {
		a.FirstPrompt = clip(text, maxCard)
	}
	a.LastUser = clip(text, maxCard)
	a.Prompts = addPiece(a.Prompts, clip(text, maxPiece), maxPrompts)
}

func (a *Accum) answer(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.LastAssistant = clip(text, maxCard)
	a.Answers = addPiece(a.Answers, clip(text, maxAnswerPiece), maxAnswers)
}

// addPiece appends p; over max bytes the oldest pieces after the first go.
func addPiece(list []string, p string, max int) []string {
	p = strings.ReplaceAll(p, " ", " ")
	list = append(list, p)
	total := 0
	for _, s := range list {
		total += len(s) + len(pieceSep)
	}
	for total > max && len(list) > 2 {
		total -= len(list[1]) + len(pieceSep)
		list = append(list[:1], list[2:]...)
	}
	return list
}

func clip(s string, runes int) string {
	if len(s) <= runes {
		return s
	}
	n := 0
	for i := range s {
		if n == runes {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func parseTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// --- Claude ---

type claudeLine struct {
	Type             string          `json:"type"`
	SessionID        string          `json:"sessionId"`
	Cwd              string          `json:"cwd"`
	GitBranch        string          `json:"gitBranch"`
	Entrypoint       string          `json:"entrypoint"`
	Timestamp        string          `json:"timestamp"`
	IsMeta           bool            `json:"isMeta"`
	IsSidechain      bool            `json:"isSidechain"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	Message          *claudeMessage  `json:"message"`
	CustomTitle      string          `json:"customTitle"`
	AITitle          string          `json:"aiTitle"`
	AgentName        string          `json:"agentName"`
	Summary          string          `json:"summary"`
	RelocatedCwd     string          `json:"relocatedCwd"`
	ToolUseResult    json.RawMessage `json:"-"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		Input         int64 `json:"input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		Output        int64 `json:"output_tokens"`
	} `json:"usage"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// claudeSkip are line types without anything the index keeps; they are
// recognized without decoding when "type" comes first.
var claudeSkip = [][]byte{
	[]byte(`{"type":"file-history-snapshot"`), []byte(`{"type":"file-history-delta"`), []byte(`{"type":"queue-operation"`),
	[]byte(`{"type":"mode"`), []byte(`{"type":"permission-mode"`), []byte(`{"type":"atis-latch"`), []byte(`{"type":"frame-link"`),
	[]byte(`{"type":"bridge-session"`), []byte(`{"type":"artifact-`), []byte(`{"type":"last-prompt"`), []byte(`{"type":"cost-state"`),
	[]byte(`{"type":"pr-link"`), []byte(`{"type":"worktree-state"`),
}

// claudeLineIn reads one Claude transcript line.
func (a *Accum) claudeLineIn(line []byte) {
	for _, p := range claudeSkip {
		if bytes.HasPrefix(line, p) {
			return
		}
	}
	// Attachments (hook output, file mentions) and tool results are the
	// bulk of a transcript: decode only what is needed of them.
	var l claudeLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	if a.SessionID == "" && l.SessionID != "" {
		a.SessionID = l.SessionID
	}
	switch l.Type {
	case "custom-title":
		a.CustomTitle = l.CustomTitle
		return
	case "agent-name":
		a.AgentName = l.AgentName
		return
	case "ai-title":
		a.AITitle = l.AITitle
		return
	case "summary":
		a.Summary = l.Summary
		return
	case "relocated":
		if l.RelocatedCwd != "" {
			a.Cwd = l.RelocatedCwd
		}
		return
	case "user", "assistant", "system", "attachment":
	default:
		return
	}
	if l.IsSidechain {
		return
	}
	a.seen(parseTime(l.Timestamp))
	if l.Cwd != "" && (a.Cwd == "" || l.Type == "user") {
		a.Cwd = l.Cwd
	}
	if l.GitBranch != "" && l.GitBranch != "HEAD" {
		a.Branch = l.GitBranch
	}
	if l.Entrypoint != "" && a.Origin == "" {
		a.Origin = l.Entrypoint
	}
	if l.Message == nil {
		return
	}
	switch l.Type {
	case "user":
		if l.IsMeta || l.IsCompactSummary {
			return
		}
		if text, ok := claudeUserText(l.Message.Content); ok {
			a.prompt(text)
		}
	case "assistant":
		m := l.Message
		if m.Usage != nil {
			n := m.Usage.Input + m.Usage.CacheCreation + m.Usage.Output
			if m.ID != "" && m.ID == a.LastMsgID {
				a.Tokens += n - a.LastMsgT // the same message again
			} else {
				a.Tokens += n
			}
			a.LastMsgID, a.LastMsgT = m.ID, n
		}
		var blocks []contentBlock
		if json.Unmarshal(m.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				a.answer(b.Text)
			case "tool_use":
				a.claudeTool(b.Name, b.Input)
			}
		}
	}
}

// claudeUserText is a real prompt's text: typed, not a tool result, a
// command's output, a hook's note or an interruption marker.
func claudeUserText(raw json.RawMessage) (string, bool) {
	var text string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return "", false
		}
	} else {
		var blocks []contentBlock
		if json.Unmarshal(raw, &blocks) != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "tool_result":
				return "", false
			}
		}
		text = strings.Join(parts, "\n")
	}
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "[Request interrupted") || strings.HasPrefix(t, "Caveat:") {
		return "", false
	}
	return t, true
}

func (a *Accum) claudeTool(name string, input json.RawMessage) {
	switch name {
	case "TodoWrite":
		var in struct {
			Todos []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		}
		if json.Unmarshal(input, &in) != nil {
			return
		}
		a.Todos = a.Todos[:0]
		for _, t := range in.Todos {
			if len(a.Todos) == maxTodos {
				break
			}
			a.Todos = append(a.Todos, wire.SessionTodo{Text: clip(oneLine(t.Content), 300), Done: t.Status == "completed"})
		}
		a.Tasks = nil
	case "TaskCreate":
		var in struct {
			Subject string `json:"subject"`
		}
		if json.Unmarshal(input, &in) != nil || len(a.Tasks) >= maxTodos {
			return
		}
		a.Tasks = append(a.Tasks, task{ID: strconv.Itoa(len(a.Tasks) + 1), Subject: clip(oneLine(in.Subject), 300)})
	case "TaskUpdate":
		var in struct {
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		}
		if json.Unmarshal(input, &in) != nil {
			return
		}
		for i := range a.Tasks {
			if a.Tasks[i].ID == in.TaskID && in.Status != "" {
				a.Tasks[i].Status = in.Status
			}
		}
	}
}

// --- Codex ---

type codexMeta struct {
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	Originator string `json:"originator"`
	Source     any    `json:"source"`
	Timestamp  string `json:"timestamp"`
	Git        *struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type codexMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// payloadType finds `"payload":{"type":"X"` near a Codex line's start
// (its keys come in the order timestamp, [ordinal,] type, payload).
func payloadType(line []byte) (top, inner string) {
	if !bytes.HasPrefix(line, []byte(`{"timestamp":"`)) {
		// Not the order Codex writes: decode the two types.
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		json.Unmarshal(line, &l)
		return l.Type, l.Payload.Type
	}
	head := line
	if len(head) > 320 {
		head = head[:320]
	}
	if i := bytes.Index(head, []byte(`"type":"`)); i >= 0 {
		rest := head[i+8:]
		if j := bytes.IndexByte(rest, '"'); j >= 0 {
			top = string(rest[:j])
		}
	}
	if i := bytes.Index(head, []byte(`"payload":{"type":"`)); i >= 0 {
		rest := head[i+19:]
		if j := bytes.IndexByte(rest, '"'); j >= 0 {
			inner = string(rest[:j])
		}
	}
	return top, inner
}

// lineTime reads a Codex line's leading timestamp without decoding it.
func lineTime(line []byte) int64 {
	const key = `{"timestamp":"`
	if !bytes.HasPrefix(line, []byte(key)) {
		var l struct {
			Timestamp string `json:"timestamp"`
		}
		json.Unmarshal(line, &l)
		return parseTime(l.Timestamp)
	}
	rest := line[len(key):]
	if j := bytes.IndexByte(rest, '"'); j > 0 && j < 40 {
		return parseTime(string(rest[:j]))
	}
	return 0
}

// codexLineIn reads one Codex rollout line.
func (a *Accum) codexLineIn(line []byte) {
	a.seen(lineTime(line))
	top, inner := payloadType(line)
	switch top {
	case "session_meta":
		var l struct {
			Payload codexMeta `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil {
			return
		}
		p := l.Payload
		if a.SessionID == "" {
			a.SessionID = p.ID
		}
		if p.Cwd != "" {
			a.Cwd = p.Cwd
		}
		if p.Git != nil && p.Git.Branch != "" {
			a.Branch = p.Git.Branch
		}
		if a.Origin == "" {
			a.Origin = p.Originator
		}
		a.seen(parseTime(p.Timestamp))
	case "response_item":
		switch inner {
		case "message":
			var l struct {
				Payload codexMessage `json:"payload"`
			}
			if json.Unmarshal(line, &l) != nil {
				return
			}
			var parts []string
			for _, c := range l.Payload.Content {
				t := strings.TrimSpace(c.Text)
				if t == "" {
					continue
				}
				if l.Payload.Role == "user" && codexContext(t) {
					continue
				}
				parts = append(parts, t)
			}
			text := strings.Join(parts, "\n")
			switch l.Payload.Role {
			case "user":
				a.prompt(text)
			case "assistant":
				a.answer(text)
			}
		case "function_call":
			if !bytes.Contains(line, []byte(`"update_plan"`)) {
				return
			}
			var l struct {
				Payload struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) != nil || l.Payload.Name != "update_plan" {
				return
			}
			var plan struct {
				Plan []struct {
					Step   string `json:"step"`
					Status string `json:"status"`
				} `json:"plan"`
			}
			if json.Unmarshal([]byte(l.Payload.Arguments), &plan) != nil {
				return
			}
			a.Todos = a.Todos[:0]
			for _, s := range plan.Plan {
				if len(a.Todos) == maxTodos {
					break
				}
				a.Todos = append(a.Todos, wire.SessionTodo{Text: clip(oneLine(s.Step), 300), Done: s.Status == "completed"})
			}
		}
	case "event_msg":
		switch inner {
		case "token_count":
			var l struct {
				Payload struct {
					Info *struct {
						Total struct {
							Input  int64 `json:"input_tokens"`
							Cached int64 `json:"cached_input_tokens"`
							Output int64 `json:"output_tokens"`
						} `json:"total_token_usage"`
					} `json:"info"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Payload.Info != nil {
				t := l.Payload.Info.Total
				a.Tokens = t.Input - t.Cached + t.Output
			}
		case "thread_name_updated":
			var l struct {
				Payload struct {
					Name string `json:"thread_name"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Payload.Name != "" {
				a.ThreadName = l.Payload.Name
			}
		}
	}
}

// codexContext: text Codex adds to user messages (environment, AGENTS.md
// and other instructions), not typed by the user.
func codexContext(t string) bool {
	return strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS.md") || strings.HasPrefix(t, "# Context from my IDE")
}

// validText drops invalid UTF-8 (SQLite and JSON want valid text).
func validText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
