package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	controlVersion      = 1
	controlCommand      = "admin_token"
	controlSocketMode   = 0o600
	controlIOTimeout    = 5 * time.Second
	controlMaxLineBytes = 4096
	KindSetupToken      = "setup_token"
	KindPairingCode     = "pairing_code"
)

type ControlHandler interface {
	AdminToken(ctx context.Context) (kind string, value string, expiresAt time.Time, err error)
}

type controlRequest struct {
	Version int    `json:"version"`
	Command string `json:"command"`
}

type controlResponse struct {
	Version   int    `json:"version"`
	OK        bool   `json:"ok"`
	Kind      string `json:"kind,omitempty"`
	Token     string `json:"token,omitempty"`
	Code      string `json:"code,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

func ServeControlSocket(ctx context.Context, path string, h ControlHandler) error {
	if h == nil {
		return errors.New("session: control handler is nil")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session: remove stale control socket: %w", err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("session: listen on control socket: %w", err)
	}
	if err := os.Chmod(path, controlSocketMode); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return fmt.Errorf("session: restrict control socket: %w", err)
	}
	stopped := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopped()
	var connections sync.WaitGroup
	defer func() {
		connections.Wait()
		_ = os.Remove(path)
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			_ = listener.Close()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("session: accept control connection: %w", err)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			serveControlConn(ctx, conn, h)
		}()
	}
}

func serveControlConn(ctx context.Context, conn net.Conn, h ControlHandler) {
	defer conn.Close()
	timeout := time.AfterFunc(controlIOTimeout, func() { _ = conn.Close() })
	defer timeout.Stop()
	line, err := bufio.NewReader(io.LimitReader(conn, controlMaxLineBytes)).ReadBytes('\n')
	if err != nil {
		return
	}
	var req controlRequest
	if err := json.Unmarshal(line, &req); err != nil {
		writeControl(conn, controlResponse{Version: controlVersion, Error: "malformed request"})
		return
	}
	writeControl(conn, answerControl(ctx, req, h))
}

func answerControl(ctx context.Context, req controlRequest, h ControlHandler) controlResponse {
	if req.Version != controlVersion {
		return controlResponse{Version: controlVersion, Error: "unsupported version"}
	}
	if req.Command != controlCommand {
		return controlResponse{Version: controlVersion, Error: "unsupported command"}
	}
	kind, value, expiresAt, err := h.AdminToken(ctx)
	if err != nil {
		return controlResponse{Version: controlVersion, Error: "could not issue token"}
	}
	resp := controlResponse{Version: controlVersion, OK: true, Kind: kind, ExpiresAt: formatMillis(expiresAt)}
	if kind == KindSetupToken {
		resp.Token = value
	} else {
		resp.Code = value
	}
	return resp
}

func writeControl(conn net.Conn, resp controlResponse) {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(encoded, '\n'))
}

func RequestAdminToken(ctx context.Context, path string) (kind string, value string, expiresAt time.Time, err error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("%w: %w", ErrControlSocketUnreachable, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	request, err := json.Marshal(controlRequest{Version: controlVersion, Command: controlCommand})
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("session: encode control request: %w", err)
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return "", "", time.Time{}, fmt.Errorf("%w: %w", ErrControlSocketUnreachable, err)
	}
	line, err := bufio.NewReader(io.LimitReader(conn, controlMaxLineBytes)).ReadBytes('\n')
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("%w: read response: %w", ErrControlProtocol, err)
	}
	return decodeControlResponse(line)
}

func decodeControlResponse(line []byte) (string, string, time.Time, error) {
	var resp controlResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return "", "", time.Time{}, fmt.Errorf("%w: decode response: %w", ErrControlProtocol, err)
	}
	if !resp.OK {
		return "", "", time.Time{}, fmt.Errorf("%w: %s", ErrControlProtocol, resp.Error)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, resp.ExpiresAt)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("%w: expires_at: %w", ErrControlProtocol, err)
	}
	value := resp.Code
	if resp.Kind == KindSetupToken {
		value = resp.Token
	}
	return resp.Kind, value, expiresAt, nil
}
