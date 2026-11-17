package mediaservice

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/media"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/nativeplayer"
)

type fakePlayer struct {
	mu       sync.Mutex
	resizes  []nativeplayer.Rect
	commands [][]string
	closed   int
}

func (p *fakePlayer) Presentation() nativeplayer.Presentation {
	return nativeplayer.PresentationEmbedded
}
func (p *fakePlayer) Resize(rect nativeplayer.Rect) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, rect)
	return nil
}
func (p *fakePlayer) Command(command ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands = append(p.commands, command)
	return nil
}
func (p *fakePlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed++
	return nil
}

type fakeSessions struct {
	mu      sync.Mutex
	result  media.OpenResult
	err     error
	failKey string
	closed  []string
	opened  []string
}

func (f *fakeSessions) Open(_ context.Context, _ string, key string) (media.OpenResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil || key == f.failKey {
		return media.OpenResult{}, errors.Join(f.err, media.ErrFileNotFound)
	}
	if f.result.Token != "" {
		return f.result, nil
	}
	f.opened = append(f.opened, key)
	kind := media.StreamKindVideo
	if strings.HasSuffix(key, ".mp3") {
		kind = media.StreamKindAudio
	}
	return media.OpenResult{Token: "tok-" + key, URL: "http://127.0.0.1:1/media/file/tok-" + key + "/" + key, Name: key, Kind: kind}, nil
}

func (f *fakeSessions) CloseSession(token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, token)
	return nil
}

func newTestMediaService() *Service {
	return New(context.Background(), &fakeSessions{}, nil)
}

func validRect() nativeplayer.Rect {
	return nativeplayer.Rect{X: 10, Y: 20, Width: 640, Height: 360}
}

func TestValidateNativeMediaCommandAcceptsSupportedCommands(t *testing.T) {
	tests := [][]string{
		{"cycle", "pause"},
		{"cycle", "mute"},
		{"seek", "10", "relative"},
		{"seek", "-5.5", "relative"},
		{"seek", "125.25", "absolute"},
		{"set", "volume", "0"},
		{"set", "volume", "82.5"},
		{"set", "speed", "0.5"},
		{"set", "speed", "2"},
		{"set", "mute", "yes"},
		{"set", "mute", "no"},
		{"set", "pause", "yes"},
		{"set", "pause", "no"},
		{"set", "aid", "1"},
		{"set", "aid", "42"},
		{"set", "aid", "auto"},
		{"set", "aid", "no"},
		{"set", "sid", "2"},
		{"set", "sid", "auto"},
		{"set", "sid", "no"},
	}

	for _, command := range tests {
		if err := validateNativeMediaCommand(command); err != nil {
			t.Fatalf("validateNativeMediaCommand(%v) returned error: %v", command, err)
		}
	}
}

func TestValidateNativeMediaCommandRejectsUnsupportedCommands(t *testing.T) {
	tests := [][]string{
		nil,
		{},
		{"loadfile", "https://example.com/video.mkv"},
		{"screenshot-to-file", "/tmp/frame.png"},
		{"screenshot"},
		{"dump-cache", "/tmp/video.mkv"},
		{"write-watch-later-config"},
		{"set", "stream-record", "/tmp/video.mkv"},
		{"set", "cache-on-disk", "yes"},
		{"set", "input-default-bindings", "yes"},
		{"cycle", "fullscreen"},
		{"cycle", "pause", "extra"},
		{"seek", "-1", "absolute"},
		{"seek", "90000", "absolute"},
		{"seek", "4000", "relative"},
		{"seek", "soon", "relative"},
		{"seek", "10", "exact"},
		{"set", "volume", "-1"},
		{"set", "volume", "101"},
		{"set", "speed", "0.1"},
		{"set", "speed", "5"},
		{"set", "mute", "maybe"},
		{"set", "pause", "maybe"},
		{"set", "pause", "true"},
		{"set", "pause", "1"},
		{"set", "pause", "yes", "extra"},
		{"set", "aid", "-1"},
		{"set", "aid", "1.5"},
		{"set", "aid", "yes"},
		{"set", "sid", "0"},
		{"set", "sid", "999999999999999999999"},
		{"set", "vid", "1"},
		{"set", "playlist-pos", "1"},
	}

	for _, command := range tests {
		if err := validateNativeMediaCommand(command); err == nil {
			t.Fatalf("validateNativeMediaCommand(%v) returned nil error", command)
		}
	}
}

func TestValidateNativeMediaOpenResultRejectsNonMedia(t *testing.T) {
	for _, kind := range []media.StreamKind{media.StreamKindImage, media.StreamKindPDF, media.StreamKindText, media.StreamKindUnknown} {
		err := validateNativeMediaOpenResult(media.OpenResult{Token: "t", URL: "http://127.0.0.1/x", Kind: kind})
		if !errors.Is(err, media.ErrUnsupportedMediaType) {
			t.Fatalf("kind %s err = %v", kind, err)
		}
	}
	if err := validateNativeMediaOpenResult(media.OpenResult{Kind: media.StreamKindVideo}); err == nil {
		t.Fatal("a session without a URL was accepted")
	}
}

func TestNativeMediaReservationRetainsSequencedStateBeforeCompletion(t *testing.T) {
	media := newTestMediaService()
	reservation, err := media.reserveNativeMediaSession("opaque-session-token", false)
	if err != nil {
		t.Fatalf("reserveNativeMediaSession: %v", err)
	}

	failed := nativeplayer.State{Status: nativeplayer.StatusFailed, Error: nativeplayer.ErrPlayerExited.Error(), Paused: true}
	snapshot, emit := media.recordNativeMediaState("opaque-session-token", reservation, failed)
	if emit {
		t.Fatal("state emitted while native attachment was still in progress")
	}
	if snapshot.Sequence != 1 || snapshot.Status != nativeplayer.StatusFailed {
		t.Fatalf("recorded state = %#v, want sequence 1 failed", snapshot)
	}
	if !media.completeNativeMediaSession("opaque-session-token", reservation, &fakePlayer{}) {
		t.Fatal("reservation could not be completed")
	}

	initial, ok := media.nativeMediaStateSnapshot("opaque-session-token", reservation)
	if !ok || initial.Sequence != 1 || initial.Status != nativeplayer.StatusFailed {
		t.Fatalf("initial state = %#v, %t; want retained sequence 1 failure", initial, ok)
	}

	playing := nativeplayer.State{Status: nativeplayer.StatusPlaying, Rate: 1, Volume: 1}
	next, emit := media.recordNativeMediaState("opaque-session-token", reservation, playing)
	if emit || next.Sequence != 1 || next.Status != nativeplayer.StatusFailed {
		t.Fatalf("state after terminal failure = %#v, emit=%t; want retained sequence 1 failure", next, emit)
	}

	active, err := media.reserveNativeMediaSession("active-session-token", false)
	if err != nil {
		t.Fatalf("reserve active session: %v", err)
	}
	opening := nativeplayer.State{Status: nativeplayer.StatusOpening, Paused: true, Loading: true, Rate: 1, Volume: 1}
	if snapshot, emit := media.recordNativeMediaState("active-session-token", active, opening); emit || snapshot.Sequence != 1 {
		t.Fatalf("opening state = %#v, emit=%t; want retained sequence 1", snapshot, emit)
	}
	if !media.completeNativeMediaSession("active-session-token", active, &fakePlayer{}) {
		t.Fatal("active reservation could not be completed")
	}
	next, emit = media.recordNativeMediaState("active-session-token", active, playing)
	if !emit || next.Sequence != 2 || next.Status != nativeplayer.StatusPlaying {
		t.Fatalf("active state = %#v, emit=%t; want emitted sequence 2 playing", next, emit)
	}
}

func TestNativeMediaBridgeIgnoresStaleSessions(t *testing.T) {
	const staleToken = "stale-session-token"
	media := newTestMediaService()
	invalidRect := nativeplayer.Rect{X: math.Inf(1), Y: 10, Width: 144, Height: 81}

	if err := media.NativeMediaCommand(staleToken, []string{"set", "pause", "yes"}); err != nil {
		t.Fatalf("NativeMediaCommand() stale-session error = %v, want nil", err)
	}
	if err := media.ResizeNativeMedia(staleToken, validRect()); err != nil {
		t.Fatalf("ResizeNativeMedia() stale-session error = %v, want nil", err)
	}
	if err := media.CloseNativeMedia(""); err != nil {
		t.Fatalf("CloseNativeMedia() empty-token error = %v, want nil", err)
	}
	if err := media.CloseNativeMedia(staleToken); err != nil {
		t.Fatalf("CloseNativeMedia() stale-session error = %v, want nil", err)
	}
	if err := media.ResizeNativeMedia(staleToken, invalidRect); !errors.Is(err, errInvalidNativeMediaViewport) {
		t.Fatalf("ResizeNativeMedia() invalid-rect error = %v", err)
	}
}

func TestNativeMediaReservationRejectsConcurrentDuplicateAttach(t *testing.T) {
	media := newTestMediaService()
	const callers = 32
	start := make(chan struct{})
	var successes atomic.Int32
	var winner *nativeMediaSession
	var winnerMu sync.Mutex
	var wg sync.WaitGroup

	for range callers {
		wg.Go(func() {
			<-start
			reservation, err := media.reserveNativeMediaSession("opaque-session-token", false)
			if err != nil {
				if strings.Contains(err.Error(), "opaque-session-token") {
					t.Errorf("duplicate-attach error leaked the media token: %v", err)
				}
				return
			}
			successes.Add(1)
			winnerMu.Lock()
			winner = reservation
			winnerMu.Unlock()
		})
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful reservations = %d, want 1", got)
	}
	winnerMu.Lock()
	reservation := winner
	winnerMu.Unlock()
	if !media.completeNativeMediaSession("opaque-session-token", reservation, &fakePlayer{}) {
		t.Fatal("winning reservation could not be completed")
	}
	if got := media.nativeMediaSession("opaque-session-token"); got != reservation || got.player == nil || got.attaching {
		t.Fatalf("completed session = %#v, want active player", got)
	}
}

func TestNativeMediaReservationCannotCompleteAfterClose(t *testing.T) {
	media := newTestMediaService()
	reservation, err := media.reserveNativeMediaSession("opaque-session-token", false)
	if err != nil {
		t.Fatalf("reserveNativeMediaSession: %v", err)
	}
	if err := media.CloseNativeMedia("opaque-session-token"); err != nil {
		t.Fatalf("CloseNativeMedia: %v", err)
	}
	if media.completeNativeMediaSession("opaque-session-token", reservation, &fakePlayer{}) {
		t.Fatal("reservation completed after it was closed")
	}
	if got := media.nativeMediaSession("opaque-session-token"); got != nil {
		t.Fatalf("native media session survived close: %#v", got)
	}
}

func openService(kind media.StreamKind) (*Service, *fakeSessions, *fakePlayer, *nativeplayer.Options, *string) {
	sessions := &fakeSessions{result: media.OpenResult{Token: "tok", URL: "http://127.0.0.1:1/media/file/tok", Name: "clip", Kind: kind, Info: media.LogicalFile{Protected: true}}}
	player := &fakePlayer{}
	var opts nativeplayer.Options
	var url string
	svc := New(context.Background(), sessions, func(_ context.Context, u string, _ nativeplayer.Rect, o nativeplayer.Options) (Player, error) {
		opts, url = o, u
		return player, nil
	})
	return svc, sessions, player, &opts, &url
}

func TestOpenNativeMediaStartsPlayerAndRoutesStateAndCommands(t *testing.T) {
	svc, sessions, player, opts, url := openService(media.StreamKindVideo)
	var states []*State
	result, err := svc.OpenNativeMedia("b", "clip.mp4", validRect(), OpenOptions{Parent: 77, OnState: func(s *State) { states = append(states, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if *url != sessions.result.URL || opts.Parent != 77 || opts.AudioOnly || !opts.Protected || opts.Title != "clip" {
		t.Fatalf("player started with url=%s opts=%+v", *url, *opts)
	}
	if result.Token != "tok" || result.InitialState == nil || result.InitialState.Status != nativeplayer.StatusOpening {
		t.Fatalf("result = %+v", result)
	}
	opts.OnState(nativeplayer.State{Status: nativeplayer.StatusPlaying, CurrentTime: 3, Duration: 9})
	if len(states) != 1 || states[0].Sequence != 2 || states[0].CurrentTime != 3 || states[0].Token != "tok" {
		t.Fatalf("states = %+v", states)
	}
	if err := svc.NativeMediaCommand("tok", []string{"set", "pause", "yes"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.NativeMediaCommand("tok", []string{"loadfile", "/etc/passwd"}); err == nil {
		t.Fatal("unsafe command reached the player")
	}
	if err := svc.ResizeNativeMedia("tok", nativeplayer.Rect{Width: 100, Height: 50}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CloseNativeMedia("tok"); err != nil {
		t.Fatal(err)
	}
	if len(player.commands) != 1 || len(player.resizes) != 1 || player.closed != 1 || len(sessions.closed) != 1 {
		t.Fatalf("player=%+v sessions closed=%v", player, sessions.closed)
	}
}

func TestOpenNativeMediaAudioNeedsNoViewportAndVideoDoes(t *testing.T) {
	svc, _, _, opts, _ := openService(media.StreamKindAudio)
	if _, err := svc.OpenNativeMedia("b", "song.mp3", nativeplayer.Rect{}, OpenOptions{}); err != nil || !opts.AudioOnly {
		t.Fatalf("audio open err=%v opts=%+v", err, *opts)
	}
	svc, sessions, _, _, _ := openService(media.StreamKindVideo)
	if _, err := svc.OpenNativeMedia("b", "clip.mp4", nativeplayer.Rect{}, OpenOptions{}); err == nil {
		t.Fatal("video opened without a viewport")
	}
	if len(sessions.closed) != 1 {
		t.Fatal("rejected open leaked its media session")
	}
}

func TestOpenNativeMediaReleasesSessionWhenPlayerFails(t *testing.T) {
	sessions := &fakeSessions{result: media.OpenResult{Token: "tok", URL: "http://x", Kind: media.StreamKindVideo}}
	svc := New(context.Background(), sessions, func(context.Context, string, nativeplayer.Rect, nativeplayer.Options) (Player, error) {
		return nil, nativeplayer.ErrUnsupported
	})
	if _, err := svc.OpenNativeMedia("b", "clip.mp4", validRect(), OpenOptions{}); !errors.Is(err, nativeplayer.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if len(sessions.closed) != 1 || svc.nativeMediaSession("tok") != nil {
		t.Fatal("failed start leaked the media session or reservation")
	}
}

func TestOpenNativePlaylistQueuesEveryFileInOneStandaloneWindow(t *testing.T) {
	sessions := &fakeSessions{}
	player := &fakePlayer{}
	var opts nativeplayer.Options
	var url string
	svc := New(context.Background(), sessions, func(_ context.Context, u string, _ nativeplayer.Rect, o nativeplayer.Options) (Player, error) {
		opts, url = o, u
		return player, nil
	})
	result, err := svc.OpenNativePlaylist("b", []string{"song.mp3", "clip.mp4", "end.mp4"}, OpenOptions{Parent: 55})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Standalone || opts.Parent != 0 || opts.AudioOnly || !strings.HasSuffix(url, "/song.mp3") {
		t.Fatalf("first file started with url=%s opts=%+v", url, opts)
	}
	want := []string{"loadfile http://127.0.0.1:1/media/file/tok-clip.mp4/clip.mp4 append", "loadfile http://127.0.0.1:1/media/file/tok-end.mp4/end.mp4 append"}
	if len(player.commands) != 2 || strings.Join(player.commands[0], " ") != want[0] || strings.Join(player.commands[1], " ") != want[1] {
		t.Fatalf("queued commands = %v", player.commands)
	}
	if err := svc.CloseNativeMedia(result.Token); err != nil {
		t.Fatal(err)
	}
	if player.closed != 1 || len(sessions.closed) != 3 {
		t.Fatalf("player closed %d times, sessions closed %v", player.closed, sessions.closed)
	}
}

func TestOpenNativePlaylistReleasesEverythingWhenAFileIsMissing(t *testing.T) {
	sessions := &fakeSessions{failKey: "gone.mp4"}
	started := false
	svc := New(context.Background(), sessions, func(context.Context, string, nativeplayer.Rect, nativeplayer.Options) (Player, error) {
		started = true
		return &fakePlayer{}, nil
	})
	if _, err := svc.OpenNativePlaylist("b", []string{"a.mp4", "gone.mp4"}, OpenOptions{}); !errors.Is(err, media.ErrFileNotFound) {
		t.Fatalf("err = %v", err)
	}
	if started || len(sessions.closed) != 1 || sessions.closed[0] != "tok-a.mp4" {
		t.Fatalf("started=%v closed=%v", started, sessions.closed)
	}
}
