package ptyhost

import "time"

// Fit sizing: while no rw owner holds the PTY's size, view attaches that
// declare a fit (wire.AttachRequest.Fit, their tile's grid) size the PTY,
// so a tall tile shows a tall screen instead of empty rows above a short
// one. The PTY takes the largest fit (the widest cols and the most rows
// among them), never below MinFitCols x MinFitRows, debounced by
// FitDebounce and only when a side changes by at least FitStep. An owner
// always wins; when it detaches, the fit applies again.

const (
	// FitDebounce is how long the fit must stay unchanged before the PTY
	// is resized (Config.FitDebounce overrides it).
	FitDebounce = 300 * time.Millisecond
	// FitStep is the smallest change (cols or rows) worth a resize.
	FitStep = 2
)

// The smallest PTY a fit makes.
const MinFitCols, MinFitRows = 80, 24

// fitTarget is the size the views' fits ask for; ok is false when none
// declares one (the lock is held).
func (t *Term) fitTarget() (cols, rows int, ok bool) {
	for v := range t.views {
		if v.fitCols <= 0 || v.fitRows <= 0 {
			continue
		}
		ok = true
		cols, rows = max(cols, v.fitCols), max(rows, v.fitRows)
	}
	if !ok {
		return 0, 0, false
	}
	return min(max(cols, MinFitCols), 1000), min(max(rows, MinFitRows), 1000), true
}

// scheduleFit (re)starts the debounce after a change of the fits or the
// owner (the lock is held).
func (t *Term) scheduleFit() {
	if t.exit != nil || t.ptmx == nil {
		return
	}
	if t.fitTimer != nil {
		t.fitTimer.Stop()
	}
	d := t.cfg.FitDebounce
	if d <= 0 {
		d = FitDebounce
	}
	t.fitTimer = time.AfterFunc(d, t.applyFit)
}

func (t *Term) applyFit() {
	t.mu.Lock()
	t.fitTimer = nil
	if t.owner != nil || t.exit != nil {
		t.mu.Unlock()
		return
	}
	cols, rows, ok := t.fitTarget()
	if !ok || (abs(cols-t.cols) < FitStep && abs(rows-t.rows) < FitStep) {
		t.mu.Unlock()
		return
	}
	changed, _ := t.resizeLocked(cols, rows)
	t.mu.Unlock()
	if changed && t.cfg.OnResize != nil {
		t.cfg.OnResize(cols, rows)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
