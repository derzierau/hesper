package wire

// agents.screen: an agent's terminal as plain text (no escape codes), from
// the daemon's copy of its screen. For scripts and agents that read what
// a person would see (hesperctl screen).

// ScreenParams are agents.screen's: Rows, the screen's last rows (0: all);
// Scrollback, that many lines of scrollback before them (0: none).
type ScreenParams struct {
	ID         string `json:"id"`
	Rows       int    `json:"rows,omitempty"`
	Scrollback int    `json:"scrollback,omitempty"`
}

// MaxScreenScrollback caps ScreenParams.Scrollback.
const MaxScreenScrollback = 10000

// ScreenResult is agents.screen's: Text, the lines (scrollback first,
// trailing blanks of each dropped) joined by "\n"; Rows and Cols, the
// screen's size; Cursor, where the cursor is on the screen (0-based) when
// it is visible; Alt, the alternate screen is on (a full-screen program).
type ScreenResult struct {
	Text   string  `json:"text"`
	Rows   int     `json:"rows"`
	Cols   int     `json:"cols"`
	Cursor *Cursor `json:"cursor,omitempty"`
	Alt    bool    `json:"alt,omitempty"`
}

// Cursor is a position on the screen.
type Cursor struct {
	Col int `json:"col"`
	Row int `json:"row"`
}
