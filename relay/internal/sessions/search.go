package sessions

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Search: filters on indexed columns, full text through FTS5 (title,
// prompts, answers), newest activity first, keyset pagination. The list
// query never reads the text columns; snippets are made for the page's
// rows only.

// listCols are cols without the (large) text columns.
var listCols = strings.Replace(cols, "prompts, answers", "'', ''", 1)

const maxLimit = 50

// ftsQuery turns what someone typed into an FTS5 query: every word must
// match, the last one as a prefix ("flaky bad" finds "flaky badge").
func ftsQuery(q string) string {
	words := queryWords(q)
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		b.WriteString(w)
		b.WriteByte('"')
		if i == len(words)-1 {
			b.WriteByte('*')
		}
	}
	return b.String()
}

// queryWords are the words of a query (what FTS5's tokenizer keeps).
func queryWords(q string) []string {
	return strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
}

// Search runs sessions.search.
func (s *Service) Search(p wire.SessionSearchParams) (wire.SessionSearchResult, error) {
	limit := p.Limit
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}
	where := []string{"deleted = 0"}
	var args []any
	if !p.Moved {
		where = append(where, "moved_to = ''")
	}
	if p.Archived != nil && *p.Archived {
		where = append(where, "archived = 1")
	} else {
		where = append(where, "archived = 0")
	}
	if p.ProjectID != "" {
		where = append(where, "project = ?")
		args = append(args, p.ProjectID)
	}
	if len(p.Kinds) > 0 {
		where = append(where, "kind IN ("+marks(len(p.Kinds))+")")
		for _, k := range p.Kinds {
			args = append(args, k)
		}
	}
	if len(p.Machines) > 0 {
		var conds []string
		var nodes, homes []any
		for _, m := range p.Machines {
			if n := s.nodeOf(m); n != "" {
				nodes = append(nodes, n)
			}
			homes = append(homes, m)
		}
		if len(nodes) > 0 {
			conds = append(conds, "node IN ("+marks(len(nodes))+")")
			args = append(args, nodes...)
		}
		conds = append(conds, "home IN ("+marks(len(homes))+")")
		args = append(args, homes...)
		where = append(where, "("+strings.Join(conds, " OR ")+")")
	}
	if p.Since != "" {
		t, err := time.Parse(time.RFC3339Nano, p.Since)
		if err != nil {
			return wire.SessionSearchResult{}, wire.Errorf(wire.CodeInvalid, "since must be RFC 3339")
		}
		where = append(where, "last >= ?")
		args = append(args, t.UnixMilli())
	}
	if p.Live != nil {
		if *p.Live {
			where = append(where, "live != ''")
		} else {
			where = append(where, "live = ''")
		}
	}
	if p.External != nil {
		where = append(where, "external = ?")
		args = append(args, b2i(*p.External))
	}
	if p.Cursor != "" {
		last, rowid, ok := parseCursor(p.Cursor)
		if !ok {
			return wire.SessionSearchResult{}, wire.Errorf(wire.CodeInvalid, "bad cursor")
		}
		where = append(where, "(last < ? OR (last = ? AND rowid < ?))")
		args = append(args, last, last, rowid)
	}
	match := ""
	if q := strings.TrimSpace(p.Query); q != "" {
		match = ftsQuery(q)
		if match == "" {
			return wire.SessionSearchResult{Items: []wire.Session{}}, nil
		}
		where = append(where, "rowid IN (SELECT rowid FROM fts WHERE fts MATCH ?)")
		args = append(args, match)
	}
	// Two steps: the page's rowids (sorting only last and rowid), then
	// their columns (a sort carrying every column of thousands of
	// matches cost ~3 ms).
	q := "SELECT rowid FROM sessions WHERE " + strings.Join(where, " AND ") + " ORDER BY last DESC, rowid DESC LIMIT " + strconv.Itoa(limit+1)
	st, err := s.db.stmt(q)
	if err != nil {
		return wire.SessionSearchResult{}, err
	}
	rows, err := st.Query(args...)
	if err != nil {
		return wire.SessionSearchResult{}, wire.Errorf(wire.CodeInvalid, "search: %v", err)
	}
	var ids []any
	order := map[int64]int{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return wire.SessionSearchResult{}, err
		}
		order[id] = len(ids)
		ids = append(ids, id)
	}
	rows.Close()
	found := make([]*row, len(ids))
	if len(ids) > 0 {
		st, err := s.db.stmt("SELECT " + listCols + " FROM sessions WHERE rowid IN (" + marks(len(ids)) + ")")
		if err != nil {
			return wire.SessionSearchResult{}, err
		}
		rows, err := st.Query(ids...)
		if err != nil {
			return wire.SessionSearchResult{}, err
		}
		for rows.Next() {
			rw, err := scanRow(rows)
			if err != nil {
				rows.Close()
				return wire.SessionSearchResult{}, err
			}
			found[order[rw.rowid]] = rw
		}
		rows.Close()
	}
	kept := found[:0]
	for _, rw := range found {
		if rw != nil {
			kept = append(kept, rw)
		}
	}
	found = kept
	res := wire.SessionSearchResult{Items: make([]wire.Session, 0, len(found))}
	if len(found) > limit {
		lastRow := found[limit-1]
		res.Cursor = strconv.FormatInt(lastRow.rec.Meta.LastActivity, 10) + "." + strconv.FormatInt(lastRow.rowid, 10)
		found = found[:limit]
	}
	var snippets map[int64]string
	if match != "" && len(found) > 0 {
		snippets = s.snippets(queryWords(p.Query), found)
	}
	for _, rw := range found {
		item := s.toWire(&rw.rec)
		item.Snippet = snippets[rw.rowid]
		res.Items = append(res.Items, item)
	}
	return res, nil
}

// snippets highlights the first match in each of the page's rows, made
// here rather than by FTS5's snippet() (which tokenizes every row's text
// again: 15 ms for a page of 50; this reads the text and searches it).
func (s *Service) snippets(words []string, page []*row) map[int64]string {
	ids := make([]any, 0, len(page))
	for _, rw := range page {
		ids = append(ids, rw.rowid)
	}
	st, err := s.db.stmt("SELECT rowid, title, prompts, answers FROM sessions WHERE rowid IN (" + marks(len(page)) + ")")
	if err != nil {
		return nil
	}
	rows, err := st.Query(ids...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var title, prompts, answers string
		if rows.Scan(&id, &title, &prompts, &answers) != nil {
			continue
		}
		for _, text := range []string{prompts, answers, title} {
			if snip := snippet(lower, text); snip != "" {
				out[id] = snip
				break
			}
		}
	}
	return out
}

func isWordByte(r byte) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 0x80
}

// snippet is the text around the first word of words (lower case; the
// last one a prefix) found in text, matches in [ ], or "".
func snippet(words []string, text string) string {
	lt := strings.ToLower(text)
	if len(lt) != len(text) {
		text = lt // a case mapping changed lengths: show it lowered
	}
	// matchAt: a word of words starts at i (at a word start; whole word
	// unless it is the last, which may be a prefix): its length, else 0.
	matchAt := func(i int) int {
		if i > 0 && isWordByte(lt[i-1]) {
			return 0
		}
		for k, w := range words {
			if !strings.HasPrefix(lt[i:], w) {
				continue
			}
			end := i + len(w)
			if k == len(words)-1 {
				for end < len(lt) && isWordByte(lt[end]) {
					end++
				}
				return end - i
			}
			if end == len(lt) || !isWordByte(lt[end]) {
				return len(w)
			}
		}
		return 0
	}
	first := -1
	for _, w := range words {
		for off := 0; off < len(lt); {
			j := strings.Index(lt[off:], w)
			if j < 0 {
				break
			}
			if matchAt(off+j) > 0 {
				if first < 0 || off+j < first {
					first = off + j
				}
				break
			}
			off += j + 1
		}
	}
	if first < 0 {
		return ""
	}
	start, end := first-60, first+120
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	for i := start; i < end; {
		if n := matchAt(i); n > 0 {
			if i+n > end {
				n = end - i
			}
			b.WriteByte('[')
			b.WriteString(text[i : i+n])
			b.WriteByte(']')
			i += n
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	if end < len(text) {
		b.WriteString("…")
	}
	out := strings.ReplaceAll(b.String(), pieceSep, " … ")
	return strings.Join(strings.Fields(out), " ")
}

func parseCursor(c string) (last, rowid int64, ok bool) {
	a, b, found := strings.Cut(c, ".")
	if !found {
		return 0, 0, false
	}
	var err1, err2 error
	last, err1 = strconv.ParseInt(a, 10, 64)
	rowid, err2 = strconv.ParseInt(b, 10, 64)
	return last, rowid, err1 == nil && err2 == nil
}

func marks(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// Stats runs sessions.stats.
func (s *Service) Stats() (wire.SessionStats, error) {
	st := wire.SessionStats{ByKind: map[string]int{}, ByMachine: map[string]int{}}
	rows, err := s.db.r.Query(`SELECT node, home, kind, COUNT(*) FROM sessions WHERE deleted = 0 GROUP BY node, home, kind`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var node, home, kind string
		var n int
		if rows.Scan(&node, &home, &kind, &n) == nil {
			st.Count += n
			st.ByKind[kind] += n
			st.ByMachine[s.nameOf(node, home)] += n
		}
	}
	rows.Close()
	st.IndexBytes = s.db.size()
	s.db.r.QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM mirror`).Scan(&st.MirrorBytes)
	s.mu.Lock()
	st.Indexing = s.indexing
	s.mu.Unlock()
	return st, nil
}
