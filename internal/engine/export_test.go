package engine

// ConnSeq is the number of Connect calls registered so far (tests only).
func ConnSeq(e *Engine) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.connSeq
}

// SetBeforeOpLock installs the hook connect runs before it takes opMu.
func SetBeforeOpLock(e *Engine, f func(seq uint64)) { e.beforeOpLock = f }
