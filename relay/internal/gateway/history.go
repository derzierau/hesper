package gateway

// wireHistory connects the shared history (internal/sessions) to the
// agents (live state, ownership, resume) and the other Macs (replication,
// mirror, moves). The projects (session folders map to projects) are
// given to sessions.Open, before its first scan.
func (d *Daemon) wireHistory() {
	h := d.History
	if h == nil {
		return
	}
	h.SetRegistry(d.Registry)
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
