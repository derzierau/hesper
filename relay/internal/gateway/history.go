package gateway

// wireHistory connects the shared history (internal/sessions) to the
// agents (live state, ownership, resume), the projects (session folders
// map to projects) and the other Macs (replication, mirror, moves).
func (d *Daemon) wireHistory() {
	h := d.History
	if h == nil {
		return
	}
	h.SetRegistry(d.Registry)
	h.SetProjects(d.Projects)
	if d.Fleet != nil {
		h.SetPeers(d.Fleet)
		d.Fleet.SetSessions(h)
	}
}

func (d *Daemon) closeHistory() {
	if d.History != nil {
		d.History.Close()
	}
}
