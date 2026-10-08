package sessions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite" // cgo-free SQLite with FTS5
)

// The index: $STATE/history.db, SQLite (modernc.org/sqlite, no cgo) in
// WAL mode. One writer goroutine owns the write connection and commits
// queued work in batches (one transaction per ≤ 20 ms of work); readers
// use their own pool, so a search never waits for the scanner. FTS5 over
// title, prompts and answers uses the sessions table as its external
// content (the text is stored once).

var sidRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

const schema = `
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (
  rowid INTEGER PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  node TEXT NOT NULL, home TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, sid TEXT NOT NULL,
  cwd TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '', first TEXT NOT NULL DEFAULT '', last_user TEXT NOT NULL DEFAULT '', last_assistant TEXT NOT NULL DEFAULT '',
  todos TEXT NOT NULL DEFAULT '', turns INTEGER NOT NULL DEFAULT 0, tokens INTEGER NOT NULL DEFAULT 0,
  started INTEGER NOT NULL DEFAULT 0, last INTEGER NOT NULL DEFAULT 0,
  origin TEXT NOT NULL DEFAULT '', external INTEGER NOT NULL DEFAULT 0, live TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0, version TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '', user_home TEXT NOT NULL DEFAULT '',
  prompts TEXT NOT NULL DEFAULT '', answers TEXT NOT NULL DEFAULT '',
  meta_s TEXT NOT NULL DEFAULT '',
  archived INTEGER NOT NULL DEFAULT 0, archived_s TEXT NOT NULL DEFAULT '',
  deleted INTEGER NOT NULL DEFAULT 0, deleted_s TEXT NOT NULL DEFAULT '',
  moved_to TEXT NOT NULL DEFAULT '', moved_s TEXT NOT NULL DEFAULT '',
  removed_at INTEGER NOT NULL DEFAULT 0, removed_s TEXT NOT NULL DEFAULT '',
  mirrors TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_last ON sessions(last DESC, rowid DESC);
CREATE INDEX IF NOT EXISTS sessions_seq ON sessions(seq);
CREATE INDEX IF NOT EXISTS sessions_sid ON sessions(sid);
CREATE INDEX IF NOT EXISTS sessions_project ON sessions(project, last DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(title, prompts, answers, content='sessions', content_rowid='rowid', tokenize='unicode61 remove_diacritics 2');
CREATE TABLE IF NOT EXISTS files (path TEXT PRIMARY KEY, size INTEGER NOT NULL, mtime INTEGER NOT NULL, offset INTEGER NOT NULL, key TEXT NOT NULL, state BLOB);
CREATE TABLE IF NOT EXISTS peers (node TEXT PRIMARY KEY, epoch TEXT NOT NULL, seq INTEGER NOT NULL, short TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS mirror (key TEXT PRIMARY KEY, version TEXT NOT NULL, raw INTEGER NOT NULL, bytes INTEGER NOT NULL, path TEXT NOT NULL, complete INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS owned (kind TEXT NOT NULL, sid TEXT NOT NULL, PRIMARY KEY (kind, sid)) WITHOUT ROWID;
`

const cols = `rowid, key, node, home, kind, sid, cwd, project, branch, title, first, last_user, last_assistant, todos, turns, tokens, started, last,
 origin, external, live, size, version, path, user_home, prompts, answers, meta_s, archived, archived_s, deleted, deleted_s, moved_to, moved_s, removed_at, removed_s, mirrors, seq`

// DB is the index.
type DB struct {
	path string
	w    *sql.DB // the writer's one connection
	r    *sql.DB // readers

	node  string
	epoch string // changes when the database is new: peers start over

	seqMu sync.Mutex
	seq   int64

	ops     chan op
	stopped chan struct{}
	closing sync.Once

	stmtMu sync.Mutex
	stmts  map[string]*sql.Stmt
}

type op struct {
	fn   func(tx *sql.Tx) error
	done chan error
}

func dsn(path string, readonly bool) string {
	v := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-8000)"
	if readonly {
		v += "&_pragma=query_only(1)"
	}
	return v
}

// openDB opens (or creates) the index at path.
func openDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxIdleTime(0)
	if _, err := w.Exec(schema); err != nil {
		w.Close()
		return nil, fmt.Errorf("history index: %w", err)
	}
	os.Chmod(path, 0o600)
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	r.SetMaxIdleConns(4)
	d := &DB{path: path, w: w, r: r, ops: make(chan op, 256), stopped: make(chan struct{}), stmts: map[string]*sql.Stmt{}}
	if d.node, err = d.kvInit("node", "n-"); err == nil {
		d.epoch, err = d.kvInit("epoch", "e-")
	}
	if err == nil {
		err = w.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM sessions`).Scan(&d.seq)
	}
	if err != nil {
		d.w.Close()
		d.r.Close()
		return nil, err
	}
	go d.writer()
	return d, nil
}

func (d *DB) kvInit(k, prefix string) (string, error) {
	var v string
	err := d.w.QueryRow(`SELECT v FROM kv WHERE k = ?`, k).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var b [8]byte
	rand.Read(b[:])
	v = prefix + hex.EncodeToString(b[:])
	_, err = d.w.Exec(`INSERT INTO kv (k, v) VALUES (?, ?)`, k, v)
	return v, err
}

func (d *DB) close() {
	d.closing.Do(func() {
		close(d.ops)
		<-d.stopped
		d.stmtMu.Lock()
		for _, s := range d.stmts {
			s.Close()
		}
		d.stmtMu.Unlock()
		d.r.Close()
		d.w.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		d.w.Close()
	})
}

// writer commits queued work: everything waiting (≤ 20 ms of it) in one
// transaction. A failing op rolls back only itself (savepoints).
func (d *DB) writer() {
	defer close(d.stopped)
	for first := range d.ops {
		batch := []op{first}
		deadline := time.After(20 * time.Millisecond)
	more:
		for len(batch) < 512 {
			select {
			case o, ok := <-d.ops:
				if !ok {
					break more
				}
				batch = append(batch, o)
			case <-deadline:
				break more
			default:
				break more
			}
		}
		tx, err := d.w.Begin()
		if err != nil {
			for _, o := range batch {
				o.done <- err
			}
			continue
		}
		errs := make([]error, len(batch))
		for i, o := range batch {
			if _, err := tx.Exec(`SAVEPOINT op`); err != nil {
				errs[i] = err
				continue
			}
			if errs[i] = o.fn(tx); errs[i] != nil {
				tx.Exec(`ROLLBACK TO op`)
			}
			tx.Exec(`RELEASE op`)
		}
		if err := tx.Commit(); err != nil {
			for i := range errs {
				if errs[i] == nil {
					errs[i] = err
				}
			}
		}
		for i, o := range batch {
			o.done <- errs[i]
		}
	}
}

// write runs fn in the writer's transaction and waits for the commit.
func (d *DB) write(fn func(tx *sql.Tx) error) (err error) {
	done := make(chan error, 1)
	defer func() {
		if recover() != nil {
			err = errClosed // Close went ahead of a slow reader (Service.Close)
		}
	}()
	d.ops <- op{fn: fn, done: done}
	return <-done
}

func (d *DB) nextSeq() int64 {
	d.seqMu.Lock()
	defer d.seqMu.Unlock()
	d.seq++
	return d.seq
}

func (d *DB) highSeq() int64 {
	d.seqMu.Lock()
	defer d.seqMu.Unlock()
	return d.seq
}

// stmt is a prepared statement on the reader pool (cached by text).
func (d *DB) stmt(q string) (*sql.Stmt, error) {
	d.stmtMu.Lock()
	defer d.stmtMu.Unlock()
	if s := d.stmts[q]; s != nil {
		return s, nil
	}
	s, err := d.r.Prepare(q)
	if err != nil {
		return nil, err
	}
	if len(d.stmts) > 256 {
		for k, old := range d.stmts {
			old.Close()
			delete(d.stmts, k)
		}
	}
	d.stmts[q] = s
	return s, nil
}

// row is a sessions row.
type row struct {
	rowid int64
	rec   Record
	seq   int64
}

type rowScanner interface{ Scan(dest ...any) error }

func scanRow(s rowScanner) (*row, error) {
	var rw row
	r := &rw.rec
	m := &r.Meta
	var todos, live, mirrors, metaS, archS, delS, movS, remS string
	var external, archived, deleted int
	err := s.Scan(&rw.rowid, new(string), &r.Node, &r.Home, &r.Kind, &r.SID, &m.Cwd, &m.ProjectID, &m.Branch, &m.Title, &m.FirstPrompt,
		&m.LastUser, &m.LastAssistant, &todos, &m.Turns, &m.Tokens, &m.StartedAt, &m.LastActivity, &m.Origin, &external, &live, &m.Size,
		&m.Version, &m.Path, &m.UserHome, &m.Prompts, &m.Answers, &metaS, &archived, &archS, &deleted, &delS, &r.MovedTo, &movS,
		&r.RemovedAt, &remS, &mirrors, &rw.seq)
	if err != nil {
		return nil, err
	}
	m.External, r.Archived, r.Deleted = external != 0, archived != 0, deleted != 0
	r.MetaS, r.ArchivedS, r.DeletedS, r.MovedS, r.RemovedS = parseStamp(metaS), parseStamp(archS), parseStamp(delS), parseStamp(movS), parseStamp(remS)
	if todos != "" {
		json.Unmarshal([]byte(todos), &m.Todos)
	}
	if live != "" {
		m.Live = new(wireLive)
		if json.Unmarshal([]byte(live), m.Live) != nil {
			m.Live = nil
		}
	}
	if mirrors != "" {
		json.Unmarshal([]byte(mirrors), &r.Mirrors)
	}
	return &rw, nil
}

func jsonText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []wireTodo:
		if len(x) == 0 {
			return ""
		}
	case *wireLive:
		if x == nil {
			return ""
		}
	case map[string]MirrorMark:
		if len(x) == 0 {
			return ""
		}
	}
	data, _ := json.Marshal(v)
	return string(data)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// getTx reads one row by key in the writer's transaction.
func getTx(tx *sql.Tx, key string) (*row, error) {
	rw, err := scanRow(tx.QueryRow(`SELECT `+cols+` FROM sessions WHERE key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rw, err
}

// putTx writes r (old: the row it replaces, nil for a new one) with a new
// sequence number, keeping the full-text index in step.
func (d *DB) putTx(tx *sql.Tx, old *row, r *Record) error {
	m := &r.Meta
	seq := d.nextSeq()
	args := []any{r.Node, r.Home, r.Kind, r.SID, m.Cwd, m.ProjectID, m.Branch, m.Title, m.FirstPrompt, m.LastUser, m.LastAssistant,
		jsonText(m.Todos), m.Turns, m.Tokens, m.StartedAt, m.LastActivity, m.Origin, b2i(m.External), jsonText(m.Live), m.Size, m.Version,
		m.Path, m.UserHome, m.Prompts, m.Answers, r.MetaS.String(), b2i(r.Archived), r.ArchivedS.String(), b2i(r.Deleted), r.DeletedS.String(),
		r.MovedTo, r.MovedS.String(), r.RemovedAt, r.RemovedS.String(), jsonText(r.Mirrors), seq}
	if old == nil {
		res, err := tx.Exec(`INSERT INTO sessions (key, node, home, kind, sid, cwd, project, branch, title, first, last_user, last_assistant, todos, turns, tokens,
 started, last, origin, external, live, size, version, path, user_home, prompts, answers, meta_s, archived, archived_s, deleted, deleted_s, moved_to, moved_s,
 removed_at, removed_s, mirrors, seq) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append([]any{r.Key()}, args...)...)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO fts (rowid, title, prompts, answers) VALUES (?, ?, ?, ?)`, id, m.Title, m.Prompts, m.Answers)
		return err
	}
	o := &old.rec.Meta
	if o.Title != m.Title || o.Prompts != m.Prompts || o.Answers != m.Answers {
		if _, err := tx.Exec(`INSERT INTO fts (fts, rowid, title, prompts, answers) VALUES ('delete', ?, ?, ?, ?)`, old.rowid, o.Title, o.Prompts, o.Answers); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO fts (rowid, title, prompts, answers) VALUES (?, ?, ?, ?)`, old.rowid, m.Title, m.Prompts, m.Answers); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`UPDATE sessions SET node=?, home=?, kind=?, sid=?, cwd=?, project=?, branch=?, title=?, first=?, last_user=?, last_assistant=?, todos=?,
 turns=?, tokens=?, started=?, last=?, origin=?, external=?, live=?, size=?, version=?, path=?, user_home=?, prompts=?, answers=?, meta_s=?, archived=?,
 archived_s=?, deleted=?, deleted_s=?, moved_to=?, moved_s=?, removed_at=?, removed_s=?, mirrors=?, seq=? WHERE rowid=?`, append(args, old.rowid)...)
	return err
}

// get reads one record by key (nil: none).
func (d *DB) get(key string) (*row, error) {
	s, err := d.stmt(`SELECT ` + cols + ` FROM sessions WHERE key = ?`)
	if err != nil {
		return nil, err
	}
	rw, err := scanRow(s.QueryRow(key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rw, err
}

// bySID lists the records of one session id (one per home it had).
func (d *DB) bySID(kind, sid string) ([]*row, error) {
	s, err := d.stmt(`SELECT ` + cols + ` FROM sessions WHERE sid = ? AND kind = ?`)
	if err != nil {
		return nil, err
	}
	rows, err := s.Query(sid, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*row
	for rows.Next() {
		rw, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rw)
	}
	return out, rows.Err()
}

// since lists records changed after seq (by seq), at most limit.
func (d *DB) since(seq int64, limit int) ([]*row, error) {
	s, err := d.stmt(`SELECT ` + cols + ` FROM sessions WHERE seq > ? ORDER BY seq LIMIT ?`)
	if err != nil {
		return nil, err
	}
	rows, err := s.QueryContext(context.Background(), seq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*row
	for rows.Next() {
		rw, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rw)
	}
	return out, rows.Err()
}

// size is the index's size on disk (database and WAL).
func (d *DB) size() int64 {
	var n int64
	for _, suffix := range []string{"", "-wal"} {
		if st, err := os.Stat(d.path + suffix); err == nil {
			n += st.Size()
		}
	}
	return n
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// sqlTx is the writer's transaction.
type sqlTx = *sql.Tx
