//go:build linux

package nativeplayer

import (
	"fmt"
	"strings"
	"sync"
)

const mpvOutputLimit = 4096

type mpvOutput struct {
	mu   sync.Mutex
	data []byte
}

func (o *mpvOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.data = append(o.data, p...)
	if len(o.data) > mpvOutputLimit {
		o.data = o.data[len(o.data)-mpvOutputLimit:]
	}
	return len(p), nil
}

func (o *mpvOutput) Tail() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	fields := strings.Fields(string(o.data))
	return strings.Join(fields, " ")
}

func mpvStartupError(exitErr error, output string) error {
	detail := output
	if detail == "" {
		detail = "mpv produced no output"
	}
	if exitErr != nil {
		return fmt.Errorf("native player: mpv exited during startup (%v): %s", exitErr, detail)
	}
	return fmt.Errorf("native player: mpv exited during startup: %s", detail)
}
