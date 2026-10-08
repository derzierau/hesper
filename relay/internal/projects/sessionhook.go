package projects

// PathOn (shared history, internal/sessions) is a project's folder on
// this Mac ("" when it has none or was removed): where a session of
// another Mac resumes when its own folder does not exist here.
func (s *Store) PathOn(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.projects[id]
	if r == nil || r.Deleted.V {
		return ""
	}
	return r.Paths[s.node].V
}
