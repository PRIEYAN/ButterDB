//go:build linux && cgo

package nativeplayer

// #include "x11_linux.h"
import "C"

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const linuxDisplayStandalone linuxDisplayMode = "standalone"

type Player struct {
	mu            sync.Mutex
	closed        bool
	terminal      bool
	view          unsafe.Pointer
	ipcPath       string
	ipcDir        string
	cmd           *exec.Cmd
	done          chan error
	standalone    bool
	stateCancel   context.CancelFunc
	eventConn     net.Conn
	eventDone     chan struct{}
	onState       StateHandler
	lastState     State
	observedState *mpvObservedProperties

	closeOnce sync.Once
	commandMu sync.Mutex
}

func Start(ctx context.Context, url string, rect Rect, opts Options) (*Player, error) {
	if !linuxNativePlayerEnabled() {
		linuxNativeLogf("start rejected: native playback disabled with %s=0", linuxNativePlayerFlag)
		return nil, ErrUnsupported
	}
	if opts.AudioOnly {
		player := &Player{done: make(chan error, 1)}
		if err := player.startProcess(ctx, url, 0, opts); err != nil {
			linuxNativeLogf("start failed: %v", err)
			return nil, err
		}
		return player, nil
	}
	if !rect.Valid() {
		linuxNativeLogf("start rejected: invalid rect x=%.1f y=%.1f w=%.1f h=%.1f", rect.X, rect.Y, rect.Width, rect.Height)
		return nil, fmt.Errorf("native player: invalid view rect")
	}

	displayMode := linuxDisplayX11Embedded
	if opts.Parent == 0 || opts.Standalone {
		displayMode = selectLinuxDisplayMode(
			os.Getenv("GDK_BACKEND"),
			os.Getenv("XDG_SESSION_TYPE"),
			os.Getenv("WAYLAND_DISPLAY"),
			os.Getenv("DISPLAY"),
		)
	}
	if opts.Standalone && displayMode != linuxDisplayUnavailable {
		displayMode = linuxDisplayStandalone
	}
	if displayMode == linuxDisplayUnavailable {
		linuxNativeLogf("start rejected: no supported graphical session")
		return nil, fmt.Errorf("%w: Linux native playback requires X11 or Wayland", ErrUnsupported)
	}

	linuxNativeLogf("start requested: mode=%s rect=x%.1f y%.1f w%.1f h%.1f", displayMode, rect.X, rect.Y, rect.Width, rect.Height)
	var view *C.bdb_x11_view
	var child uintptr
	if displayMode == linuxDisplayX11Embedded {
		view = C.bdb_x11_create(C.ulong(opts.Parent), C.double(rect.X), C.double(rect.Y), C.double(rect.Width), C.double(rect.Height))
		if view == nil {
			linuxNativeLogf("start failed: could not create X11 child window")
			return nil, fmt.Errorf("native player: could not create X11 child window")
		}
		child = uintptr(C.bdb_x11_child(view))
		if child == 0 {
			C.bdb_x11_destroy(view)
			linuxNativeLogf("start failed: X11 child window id is zero")
			return nil, fmt.Errorf("native player: invalid X11 child window")
		}
		linuxNativeLogf("x11 child ready: wid=%d parent=%d", child, opts.Parent)
	}

	player := &Player{
		view:       unsafe.Pointer(view),
		done:       make(chan error, 1),
		standalone: displayMode == linuxDisplayWaylandStandalone || displayMode == linuxDisplayStandalone,
	}
	if err := player.startProcess(ctx, url, child, opts); err != nil {
		player.destroyView()
		linuxNativeLogf("start failed: %v", err)
		return nil, err
	}
	linuxNativeLogf("start ok: mode=%s", displayMode)
	return player, nil
}

func (p *Player) startProcess(ctx context.Context, url string, windowID uintptr, opts Options) error {
	mpvPath, version, err := findLinuxMPV()
	if err != nil {
		linuxNativeLogf("mpv lookup failed: %v", err)
		return err
	}

	ipcDir, err := os.MkdirTemp("", "butterdb-mpv-*")
	if err != nil {
		return fmt.Errorf("native player: create IPC dir: %w", err)
	}
	_ = os.Chmod(ipcDir, 0o700)
	p.ipcDir = ipcDir
	p.ipcPath = filepath.Join(ipcDir, fmt.Sprintf("mpv-%d.sock", os.Getpid()))
	_ = os.Remove(p.ipcPath)
	args := linuxMPVArgs(version, p.ipcPath, windowID, opts)

	output := &mpvOutput{}
	cmd := exec.CommandContext(ctx, mpvPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	cmd.Stdout = output
	cmd.Stderr = output
	linuxNativeLogf("mpv start: path=%s version=%s wid=%d ipc=%s", mpvPath, version, windowID, p.ipcPath)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(p.ipcDir)
		linuxNativeLogf("mpv start failed: %v", err)
		return fmt.Errorf("native player: start mpv: %w", err)
	}
	p.cmd = cmd
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = cmd.Wait()
		close(exited)
	}()
	abort := func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		<-exited
		p.cmd = nil
		_ = os.RemoveAll(p.ipcDir)
	}
	p.onState = opts.OnState
	if opts.OnState != nil {
		p.publishState(normalizeState(State{Status: StatusOpening, Paused: true, Loading: true, Volume: 1, Rate: 1}))
	}
	eventConn, err := dialLinuxMPVIPCWithAttempts(p.ipcPath, 80, exited)
	if err != nil {
		select {
		case <-exited:
			err = mpvStartupError(exitErr, output.Tail())
		default:
		}
		abort()
		return err
	}
	stateCtx, cancel := context.WithCancel(ctx)
	p.stateCancel = cancel
	p.eventConn = eventConn
	p.eventDone = make(chan struct{})
	if opts.OnState != nil {
		_ = eventConn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		if err := writeMPVObserveProperties(eventConn, mpvStatePropertyNames); err != nil {
			cancel()
			_ = eventConn.Close()
			abort()
			return fmt.Errorf("native player: observe mpv state: %w", err)
		}
		_ = eventConn.SetWriteDeadline(time.Time{})
	}
	go p.observeEvents(stateCtx, eventConn, p.eventDone)
	if err := p.sendPayload(eventConn, mpvCommandPayload("loadfile", url, "replace"), 80); err != nil {
		cancel()
		_ = eventConn.Close()
		abort()
		return fmt.Errorf("native player: initialize mpv IPC: %w", err)
	}
	go func() {
		<-exited
		err := exitErr
		linuxNativeLogf("mpv process exited: %v output=%q", err, output.Tail())
		p.handleProcessExit(err)
		p.done <- err
		p.destroyView()
		_ = os.RemoveAll(p.ipcDir)
	}()
	return nil
}

func (p *Player) Presentation() Presentation {
	if p != nil && p.standalone {
		return PresentationStandalone
	}
	return PresentationEmbedded
}

func (p *Player) Resize(rect Rect) error {
	if p == nil {
		return nil
	}
	if !rect.Valid() {
		return fmt.Errorf("native player: invalid view rect")
	}

	p.mu.Lock()
	view := p.view
	closed := p.closed
	if closed || view == nil {
		p.mu.Unlock()
		return nil
	}
	C.bdb_x11_resize((*C.bdb_x11_view)(view), C.double(rect.X), C.double(rect.Y), C.double(rect.Width), C.double(rect.Height))
	p.mu.Unlock()
	return nil
}

func (p *Player) Command(command ...string) error {
	if p == nil || len(command) == 0 {
		return nil
	}

	p.mu.Lock()
	ipcPath := p.ipcPath
	closed := p.closed
	conn := p.eventConn
	p.mu.Unlock()
	if closed || ipcPath == "" {
		return nil
	}

	return p.sendPayload(conn, mpvCommandPayload(command...), 20)
}

func (p *Player) sendPayload(conn net.Conn, payload []byte, attempts int) error {
	if conn != nil {
		p.commandMu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		written, err := conn.Write(payload)
		_ = conn.SetWriteDeadline(time.Time{})
		p.commandMu.Unlock()
		if err == nil && written == len(payload) {
			return nil
		}
	}
	return writeMPVIPCWithAttempts(p.ipcPath, payload, attempts)
}

func (p *Player) Close() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	ipcPath := p.ipcPath
	ipcDir := p.ipcDir
	cmd := p.cmd
	done := p.done
	stateCancel := p.stateCancel
	eventConn := p.eventConn
	eventDone := p.eventDone
	p.mu.Unlock()

	linuxNativeLogf("close requested")
	if ipcPath != "" {
		_ = writeMPVIPC(ipcPath, []byte(`{"command":["quit"]}`+"\n"))
	}
	if stateCancel != nil {
		stateCancel()
	}
	if eventConn != nil {
		_ = eventConn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		select {
		case <-done:
		case <-time.After(750 * time.Millisecond):
			linuxNativeLogf("close timeout: killing mpv process group pid=%d", cmd.Process.Pid)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	}
	if eventDone != nil {
		select {
		case <-eventDone:
		case <-time.After(500 * time.Millisecond):
		}
	}
	p.destroyView()
	_ = os.RemoveAll(ipcDir)
	p.emitTerminal(StatusClosed)
	linuxNativeLogf("close complete")
	return nil
}

func (p *Player) destroyView() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		view := p.view
		p.view = nil
		if view != nil {
			C.bdb_x11_destroy((*C.bdb_x11_view)(view))
			linuxNativeLogf("x11 child destroyed")
		}
		p.mu.Unlock()
	})
}

func writeMPVIPC(path string, payload []byte) error {
	return writeMPVIPCWithAttempts(path, payload, 20)
}

func writeMPVIPCWithAttempts(path string, payload []byte, attempts int) error {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
			return writeAndCloseWithTimeout(conn, payload, 600*time.Millisecond)
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("native player: mpv IPC unavailable: %w", lastErr)
}

func (p *Player) handleProcessExit(err error) {
	p.mu.Lock()
	closed := p.closed
	standalone := p.standalone
	stateCancel := p.stateCancel
	eventConn := p.eventConn
	p.mu.Unlock()
	if stateCancel != nil {
		stateCancel()
	}
	if eventConn != nil {
		_ = eventConn.Close()
	}
	switch sidecarExitStatus(closed, standalone, err) {
	case StatusClosed:
		p.emitTerminal(StatusClosed)
	default:
		p.emitTerminal(StatusFailed)
	}
}

func (p *Player) observeEvents(ctx context.Context, conn net.Conn, done chan<- struct{}) {
	defer close(done)
	defer conn.Close()
	stopClose := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopClose:
		}
	}()
	defer close(stopClose)
	if err := scanMPVEvents(conn, func(event mpvIPCEvent) {
		p.handleMPVEvent(event)
	}); err != nil && ctx.Err() == nil {
		linuxNativeLogf("mpv event observer stopped: %v", err)
	}
}

func (p *Player) handleMPVEvent(event mpvIPCEvent) {
	if event.Event == "property-change" {
		p.updateObservedState(event)
		return
	}
	status, ok := mpvEventStatus(event)
	if !ok {
		return
	}
	if status == StatusEnded {
		p.mu.Lock()
		lastState := p.lastState
		p.mu.Unlock()
		p.publishState(endedState(lastState))
		return
	}
	p.emitTerminal(status)
}

func (p *Player) updateObservedState(event mpvIPCEvent) {
	if event.Name == "" {
		return
	}
	p.mu.Lock()
	if p.closed || p.terminal {
		p.mu.Unlock()
		return
	}
	if p.observedState == nil {
		p.observedState = newMPVObservedProperties(mpvStatePropertyNames)
	}
	values, ready := p.observedState.update(event, time.Now())
	p.mu.Unlock()
	if !ready {
		return
	}
	p.publishState(stateFromMPVProperties(values))
}

func dialLinuxMPVIPCWithAttempts(path string, attempts int, exited <-chan struct{}) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-exited:
			return nil, fmt.Errorf("native player: mpv IPC unavailable: %w", lastErr)
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("native player: mpv IPC unavailable: %w", lastErr)
}

func EmbeddingSupported() bool {
	if !linuxNativePlayerEnabled() {
		return false
	}
	return selectLinuxDisplayMode(os.Getenv("GDK_BACKEND"), os.Getenv("XDG_SESSION_TYPE"), os.Getenv("WAYLAND_DISPLAY"), os.Getenv("DISPLAY")) == linuxDisplayX11Embedded
}
