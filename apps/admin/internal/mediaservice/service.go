package mediaservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/media"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/nativeplayer"
)

type Player interface {
	Presentation() nativeplayer.Presentation
	Resize(rect nativeplayer.Rect) error
	Command(command ...string) error
	Close() error
}

type Starter func(ctx context.Context, url string, rect nativeplayer.Rect, opts nativeplayer.Options) (Player, error)

type Sessions interface {
	Open(ctx context.Context, bucket, key string) (media.OpenResult, error)
	CloseSession(token string) error
}

type Result struct {
	Token        string
	Name         string
	Kind         media.StreamKind
	Presentation string
	InitialState *State
	Info         media.LogicalFile
}

type Range struct {
	Start float64
	End   float64
}

type State struct {
	Token       string
	Sequence    uint64
	Status      nativeplayer.PlaybackStatus
	Error       string
	EOF         bool
	Paused      bool
	CurrentTime float64
	Duration    float64
	Buffered    []Range
	Volume      float64
	Muted       bool
	Rate        float64
	Loading     bool
	Tracks      []nativeplayer.Track
	PlaylistPos int
}

type OpenOptions struct {
	Parent     uintptr
	Standalone bool
	OnState    func(*State)
}

type nativeMediaSession struct {
	player        Player
	attaching     bool
	encrypted     bool
	stateSequence uint64
	lastState     *State
	terminal      bool
	onState       func(*State)
	extras        []string
}

var errInvalidNativeMediaViewport = errors.New("invalid native media viewport")

type Service struct {
	ctx      context.Context
	sessions Sessions
	start    Starter

	nativeMediaMu sync.Mutex
	nativeMedia   map[string]*nativeMediaSession
}

func New(ctx context.Context, sessions Sessions, start Starter) *Service {
	if start == nil {
		start = func(ctx context.Context, url string, rect nativeplayer.Rect, opts nativeplayer.Options) (Player, error) {
			player, err := nativeplayer.Start(ctx, url, rect, opts)
			if err != nil {
				return nil, err
			}
			return player, nil
		}
	}
	return &Service{ctx: ctx, sessions: sessions, start: start}
}

func (s *Service) OpenNativeMedia(bucket, key string, rect nativeplayer.Rect, opts OpenOptions) (Result, error) {
	if s.sessions == nil {
		return Result{}, fmt.Errorf("backend not ready")
	}
	opened, err := s.sessions.Open(s.ctx, bucket, key)
	if err != nil {
		return Result{}, err
	}
	if opened.Kind == media.StreamKindVideo && !rect.Valid() {
		_ = s.sessions.CloseSession(opened.Token)
		return Result{}, fmt.Errorf("invalid video viewport")
	}
	result, err := s.attachNativeMedia(opened, rect, opts)
	if err != nil {
		_ = s.sessions.CloseSession(opened.Token)
		return Result{}, err
	}
	return result, nil
}

func (s *Service) attachNativeMedia(opened media.OpenResult, rect nativeplayer.Rect, open OpenOptions) (Result, error) {
	if err := validateNativeMediaOpenResult(opened); err != nil {
		return Result{}, err
	}
	reservation, err := s.reserveNativeMediaSession(opened.Token, opened.Info.Encrypted)
	if err != nil {
		return Result{}, err
	}
	reservation.onState = open.OnState
	completed := false
	defer func() {
		if !completed {
			s.releaseNativeMediaReservation(opened.Token, reservation)
		}
	}()

	token := opened.Token
	opts := nativeplayer.Options{
		Protected:  opened.Info.Protected,
		AudioOnly:  opened.Kind == media.StreamKindAudio && !open.Standalone,
		Parent:     open.Parent,
		Standalone: open.Standalone,
		Title:      opened.Name,
		OnState: func(state nativeplayer.State) {
			snapshot, emit := s.recordNativeMediaState(token, reservation, state)
			if emit {
				s.emitNativeMediaState(reservation, snapshot)
			}
		},
	}
	playerStarted := time.Now()
	player, err := s.start(s.ctx, opened.URL, rect, opts)
	slog.Debug("native media: player start finished", "elapsed", time.Since(playerStarted), "err", err)
	if err != nil {
		if errors.Is(err, nativeplayer.ErrUnsupported) {
			return Result{}, fmt.Errorf("%w: native playback is not available on this platform yet", nativeplayer.ErrUnsupported)
		}
		return Result{}, err
	}
	if _, ok := s.nativeMediaStateSnapshot(opened.Token, reservation); !ok {
		_, _ = s.recordNativeMediaState(token, reservation, nativeplayer.State{
			Status:  nativeplayer.StatusOpening,
			Paused:  true,
			Loading: true,
			Volume:  1,
			Rate:    1,
		})
	}

	if !s.completeNativeMediaSession(opened.Token, reservation, player) {
		_ = player.Close()
		return Result{}, fmt.Errorf("native playback attachment was canceled")
	}
	completed = true
	initialState, _ := s.nativeMediaStateSnapshot(opened.Token, reservation)

	return Result{
		Token:        opened.Token,
		Name:         opened.Name,
		Kind:         opened.Kind,
		Presentation: string(player.Presentation()),
		InitialState: initialState,
		Info:         opened.Info,
	}, nil
}

var standaloneRect = nativeplayer.Rect{Width: 1280, Height: 720}

func (s *Service) OpenNativePlaylist(bucket string, keys []string, opts OpenOptions) (Result, error) {
	if s.sessions == nil {
		return Result{}, fmt.Errorf("backend not ready")
	}
	if len(keys) == 0 {
		return Result{}, fmt.Errorf("nothing to play")
	}
	opened := make([]media.OpenResult, 0, len(keys))
	closeOpened := func() {
		for _, item := range opened {
			_ = s.sessions.CloseSession(item.Token)
		}
	}
	for _, key := range keys {
		item, err := s.sessions.Open(s.ctx, bucket, key)
		if err != nil {
			closeOpened()
			return Result{}, err
		}
		opened = append(opened, item)
	}
	opts.Standalone = true
	opts.Parent = 0
	result, err := s.attachNativeMedia(opened[0], standaloneRect, opts)
	if err != nil {
		closeOpened()
		return Result{}, err
	}
	extras := make([]string, 0, len(opened)-1)
	player := s.nativeMediaPlayer(result.Token)
	for _, item := range opened[1:] {
		extras = append(extras, item.Token)
		if player != nil {
			_ = player.Command("loadfile", item.URL, "append")
		}
	}
	if !s.attachExtras(result.Token, extras) {
		for _, token := range extras {
			_ = s.sessions.CloseSession(token)
		}
	}
	return result, nil
}

func (s *Service) AppendLocalFile(token, path string) error {
	return s.internalCommand(token, "loadfile", path, "append")
}

func (s *Service) RemovePlaylistEntry(token string, index int) error {
	return s.internalCommand(token, "playlist-remove", strconv.Itoa(index))
}

func (s *Service) PlayNext(token string) error {
	return s.internalCommand(token, "playlist-next")
}

func (s *Service) internalCommand(token string, command ...string) error {
	player := s.nativeMediaPlayer(token)
	if player == nil {
		return media.ErrSessionNotFound
	}
	return player.Command(command...)
}

func (s *Service) attachExtras(token string, extras []string) bool {
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	session := s.nativeMedia[token]
	if session == nil {
		return false
	}
	session.extras = append(session.extras, extras...)
	return true
}

func (s *Service) ResizeNativeMedia(token string, rect nativeplayer.Rect) error {
	if !rect.Valid() {
		return errInvalidNativeMediaViewport
	}
	player := s.nativeMediaPlayer(token)
	if player == nil {
		return nil
	}
	return player.Resize(rect)
}

func (s *Service) NativeMediaCommand(token string, command []string) error {
	if err := validateNativeMediaCommand(command); err != nil {
		return err
	}
	player := s.nativeMediaPlayer(token)
	if player == nil {
		return nil
	}
	return player.Command(command...)
}

func (s *Service) CloseNativeMedia(token string) error {
	if token == "" {
		return nil
	}
	session := s.takeNativeMediaSession(token)
	if session != nil && session.player != nil {
		_ = session.player.Close()
	}
	if session != nil && s.sessions != nil {
		for _, extra := range session.extras {
			_ = s.sessions.CloseSession(extra)
		}
	}
	if s.sessions != nil {
		err := s.sessions.CloseSession(token)
		if errors.Is(err, media.ErrSessionNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) CloseAllNativeMedia() {
	s.nativeMediaMu.Lock()
	sessions := s.nativeMedia
	s.nativeMedia = nil
	s.nativeMediaMu.Unlock()

	for token, session := range sessions {
		if session != nil && session.player != nil {
			_ = session.player.Close()
		}
		if s.sessions != nil {
			_ = s.sessions.CloseSession(token)
			if session != nil {
				for _, extra := range session.extras {
					_ = s.sessions.CloseSession(extra)
				}
			}
		}
	}
}

func (s *Service) emitNativeMediaState(reservation *nativeMediaSession, state *State) {
	if state == nil || state.Token == "" || reservation == nil || reservation.onState == nil {
		return
	}
	reservation.onState(state)
}

func (s *Service) nativeMediaSession(token string) *nativeMediaSession {
	if token == "" {
		return nil
	}
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	return s.nativeMedia[token]
}

func (s *Service) nativeMediaPlayer(token string) Player {
	if token == "" {
		return nil
	}
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	session := s.nativeMedia[token]
	if session == nil {
		return nil
	}
	return session.player
}

func (s *Service) reserveNativeMediaSession(token string, encrypted bool) (*nativeMediaSession, error) {
	if token == "" {
		return nil, fmt.Errorf("media session is required")
	}
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	if s.nativeMedia == nil {
		s.nativeMedia = make(map[string]*nativeMediaSession)
	}
	if _, exists := s.nativeMedia[token]; exists {
		return nil, fmt.Errorf("native playback is already attached or attaching")
	}
	reservation := &nativeMediaSession{attaching: true, encrypted: encrypted}
	s.nativeMedia[token] = reservation
	return reservation, nil
}

func (s *Service) completeNativeMediaSession(token string, reservation *nativeMediaSession, player Player) bool {
	if token == "" || reservation == nil || player == nil {
		return false
	}
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	current, exists := s.nativeMedia[token]
	if !exists || current != reservation || !reservation.attaching {
		return false
	}
	reservation.player = player
	reservation.attaching = false
	return true
}

func (s *Service) recordNativeMediaState(token string, reservation *nativeMediaSession, state nativeplayer.State) (*State, bool) {
	if token == "" || reservation == nil {
		return nil, false
	}
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	current := s.nativeMedia[token]
	if current == nil || current != reservation {
		return nil, false
	}
	if current.terminal {
		return cloneNativeMediaState(current.lastState), false
	}
	current.stateSequence++
	snapshot := nativeMediaStateFromPlayer(token, current.stateSequence, state)
	current.lastState = snapshot
	current.terminal = snapshot.Status == nativeplayer.StatusFailed || snapshot.Status == nativeplayer.StatusClosed
	return cloneNativeMediaState(snapshot), !current.attaching
}

func (s *Service) nativeMediaStateSnapshot(token string, reservation *nativeMediaSession) (*State, bool) {
	if token == "" || reservation == nil {
		return nil, false
	}
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	current := s.nativeMedia[token]
	if current == nil || current != reservation || current.lastState == nil {
		return nil, false
	}
	return cloneNativeMediaState(current.lastState), true
}

func nativeMediaStateFromPlayer(token string, sequence uint64, state nativeplayer.State) *State {
	buffered := make([]Range, 0, len(state.Buffered))
	for _, item := range state.Buffered {
		if item.End <= item.Start {
			continue
		}
		buffered = append(buffered, Range{Start: item.Start, End: item.End})
	}
	tracks := make([]nativeplayer.Track, len(state.Tracks))
	copy(tracks, state.Tracks)
	return &State{
		Token:       token,
		Sequence:    sequence,
		Status:      state.Status,
		Error:       state.Error,
		EOF:         state.EOF,
		Paused:      state.Paused,
		CurrentTime: state.CurrentTime,
		Duration:    state.Duration,
		Buffered:    buffered,
		Volume:      state.Volume,
		Muted:       state.Muted,
		Rate:        state.Rate,
		Loading:     state.Loading,
		Tracks:      tracks,
		PlaylistPos: state.PlaylistPos,
	}
}

func cloneNativeMediaState(state *State) *State {
	if state == nil {
		return nil
	}
	clone := *state
	clone.Buffered = append([]Range(nil), state.Buffered...)
	clone.Tracks = append([]nativeplayer.Track(nil), state.Tracks...)
	return &clone
}

func (s *Service) releaseNativeMediaReservation(token string, reservation *nativeMediaSession) {
	if token == "" || reservation == nil {
		return
	}
	s.nativeMediaMu.Lock()
	if s.nativeMedia[token] == reservation && reservation.attaching {
		delete(s.nativeMedia, token)
	}
	s.nativeMediaMu.Unlock()
}

func (s *Service) takeNativeMediaSession(token string) *nativeMediaSession {
	if token == "" {
		return nil
	}
	s.nativeMediaMu.Lock()
	defer s.nativeMediaMu.Unlock()
	session := s.nativeMedia[token]
	delete(s.nativeMedia, token)
	return session
}

func validateNativeMediaCommand(command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("native media command is empty")
	}

	switch command[0] {
	case "cycle":
		if len(command) != 2 {
			return fmt.Errorf("invalid native media cycle command")
		}
		switch command[1] {
		case "pause", "mute":
			return nil
		default:
			return fmt.Errorf("unsupported native media cycle target")
		}
	case "seek":
		if len(command) != 3 {
			return fmt.Errorf("invalid native media seek command")
		}
		value, err := strconv.ParseFloat(command[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("invalid native media seek offset")
		}
		switch command[2] {
		case "relative":
			if math.Abs(value) > 3600 {
				return fmt.Errorf("native media relative seek is too large")
			}
		case "absolute":
			if value < 0 || value > 24*3600 {
				return fmt.Errorf("native media absolute seek is out of range")
			}
		default:
			return fmt.Errorf("unsupported native media seek mode")
		}
		return nil
	case "set":
		if len(command) != 3 {
			return fmt.Errorf("invalid native media set command")
		}
		switch command[1] {
		case "volume":
			value, err := strconv.ParseFloat(command[2], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
				return fmt.Errorf("invalid native media volume")
			}
			return nil
		case "speed":
			value, err := strconv.ParseFloat(command[2], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.25 || value > 4 {
				return fmt.Errorf("invalid native media speed")
			}
			return nil
		case "mute", "pause":
			if command[2] != "yes" && command[2] != "no" {
				return fmt.Errorf("invalid native media boolean value")
			}
			return nil
		case "aid", "sid":
			if command[2] == "auto" || command[2] == "no" {
				return nil
			}
			trackID, err := strconv.ParseInt(command[2], 10, 32)
			if err != nil || trackID <= 0 {
				return fmt.Errorf("invalid native media track selection")
			}
			return nil
		default:
			return fmt.Errorf("unsupported native media setting")
		}
	default:
		return fmt.Errorf("unsupported native media command")
	}
}

func validateNativeMediaOpenResult(opened media.OpenResult) error {
	if opened.Kind != media.StreamKindVideo && opened.Kind != media.StreamKindAudio {
		return media.ErrUnsupportedMediaType
	}
	if opened.Token == "" || opened.URL == "" {
		return fmt.Errorf("media session is not playable")
	}
	return nil
}
