//go:build linux && cgo

package nativeplayer

func (p *Player) publishState(state State) {
	state = normalizeState(state)
	p.mu.Lock()
	if p.closed || p.terminal {
		p.mu.Unlock()
		return
	}
	p.lastState = state
	onState := p.onState
	p.mu.Unlock()
	if onState != nil {
		onState(state)
	}
}

func (p *Player) emitTerminal(status PlaybackStatus) {
	state := terminalState(status)
	p.mu.Lock()
	if p.terminal {
		p.mu.Unlock()
		return
	}
	p.terminal = true
	p.lastState = state
	onState := p.onState
	p.mu.Unlock()
	if onState != nil {
		onState(state)
	}
}
