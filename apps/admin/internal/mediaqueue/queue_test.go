package mediaqueue

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/mediaservice"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/nativeplayer"
)

type fakePlayer struct {
	mu       sync.Mutex
	opened   [][]string
	appended []string
	removed  []int
	nexts    int
	closed   []string
	onState  func(*mediaservice.State)
	openErr  error
}

func (p *fakePlayer) OpenNativePlaylist(_ string, keys []string, opts mediaservice.OpenOptions) (mediaservice.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.openErr != nil {
		return mediaservice.Result{}, p.openErr
	}
	p.opened = append(p.opened, keys)
	p.onState = opts.OnState
	return mediaservice.Result{Token: "tok"}, nil
}

func (p *fakePlayer) AppendLocalFile(_, path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.appended = append(p.appended, path)
	return nil
}

func (p *fakePlayer) RemovePlaylistEntry(_ string, index int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removed = append(p.removed, index)
	return nil
}

func (p *fakePlayer) PlayNext(string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nexts++
	return nil
}

func (p *fakePlayer) CloseNativeMedia(token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = append(p.closed, token)
	return nil
}

func (p *fakePlayer) state(status nativeplayer.PlaybackStatus, pos int) {
	p.mu.Lock()
	fn := p.onState
	p.mu.Unlock()
	fn(&mediaservice.State{Token: "tok", Status: status, PlaylistPos: pos})
}

func (p *fakePlayer) snapshot() (appended []string, removed []int, nexts int, closed []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.appended...), append([]int(nil), p.removed...), p.nexts, append([]string(nil), p.closed...)
}

type fakeSource struct {
	mu    sync.Mutex
	data  map[string][]byte
	fail  map[string]bool
	reads []adminv1.GetObjectOptions
}

func (s *fakeSource) GetObject(_ context.Context, _, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, opts)
	if s.fail[key] {
		return adminv1.ObjectStream{}, &adminv1.APIError{Status: http.StatusInternalServerError}
	}
	return adminv1.ObjectStream{Body: io.NopCloser(bytes.NewReader(s.data[key])), Status: http.StatusOK, ContentLength: int64(len(s.data[key]))}, nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var songs = []adminv1.ObjectSummary{
	{Key: "music/one.mp3", ETag: `"1"`},
	{Key: "music/two.mp3", ETag: `"2"`},
	{Key: "music/three.mp3", ETag: `"3"`},
}

func newQueue(t *testing.T, delay time.Duration) (*Queue, *fakePlayer, *fakeSource, string) {
	t.Helper()
	root := t.TempDir()
	player := &fakePlayer{}
	source := &fakeSource{data: map[string][]byte{"music/one.mp3": []byte("1111"), "music/two.mp3": []byte("2222"), "music/three.mp3": []byte("3333")}, fail: map[string]bool{}}
	return New(player, source, Options{RemoveDelay: delay, TempRoot: root}), player, source, root
}

func TestQueueStreamsFirstAndDownloadsOnlyTheNextFileAhead(t *testing.T) {
	q, player, source, root := newQueue(t, time.Hour)
	if err := q.Play(context.Background(), "b", songs, nil); err != nil {
		t.Fatal(err)
	}
	if len(player.opened) != 1 || len(player.opened[0]) != 1 || player.opened[0][0] != "music/one.mp3" {
		t.Fatalf("player opened %v, want the first song only", player.opened)
	}
	eventually(t, "next song downloaded", func() bool { a, _, _, _ := player.snapshot(); return len(a) == 1 })
	appended, _, _, _ := player.snapshot()
	if !strings.HasPrefix(appended[0], root) || filepath.Base(appended[0]) != "two.mp3" {
		t.Fatalf("appended %v, want a temp copy of two.mp3", appended)
	}
	if data, _ := os.ReadFile(appended[0]); string(data) != "2222" {
		t.Fatalf("temp copy holds %q", data)
	}
	if info, _ := os.Stat(appended[0]); info.Mode().Perm() != 0o600 {
		t.Fatalf("temp copy mode %v, want private", info.Mode().Perm())
	}
	time.Sleep(50 * time.Millisecond)
	if a, _, _, _ := player.snapshot(); len(a) != 1 {
		t.Fatalf("downloaded too far ahead: %v", a)
	}
	source.mu.Lock()
	if source.reads[0].IfMatch != `"2"` {
		t.Fatalf("download not pinned to the listed version: %+v", source.reads[0])
	}
	source.mu.Unlock()

	player.state(nativeplayer.StatusPlaying, 1)
	eventually(t, "third song downloaded once the second plays", func() bool { a, _, _, _ := player.snapshot(); return len(a) == 2 })
}

func TestFinishedSongsLeaveTempAfterTheDelayNotBefore(t *testing.T) {
	q, player, _, _ := newQueue(t, 150*time.Millisecond)
	if err := q.Play(context.Background(), "b", songs, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second song cached", func() bool { a, _, _, _ := player.snapshot(); return len(a) == 1 })
	player.state(nativeplayer.StatusPlaying, 1)
	eventually(t, "third song cached", func() bool { a, _, _, _ := player.snapshot(); return len(a) == 2 })
	appended, _, _, _ := player.snapshot()
	second := appended[0]

	player.state(nativeplayer.StatusPlaying, 2)
	time.Sleep(40 * time.Millisecond)
	if !exists(second) {
		t.Fatal("finished song deleted immediately")
	}
	eventually(t, "finished songs removed", func() bool {
		_, removed, _, _ := player.snapshot()
		return len(removed) == 2 && !exists(second)
	})
	if _, removed, _, _ := player.snapshot(); removed[0] != 0 || removed[1] != 0 {
		t.Fatalf("playlist removals %v, want the head entry each time", removed)
	}
	if !exists(appended[1]) {
		t.Fatal("the song still playing was deleted")
	}
}

func TestGoingBackKeepsTheSong(t *testing.T) {
	q, player, _, _ := newQueue(t, 120*time.Millisecond)
	if err := q.Play(context.Background(), "b", songs, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second song cached", func() bool { a, _, _, _ := player.snapshot(); return len(a) == 1 })
	player.state(nativeplayer.StatusPlaying, 1)
	eventually(t, "third song cached", func() bool { a, _, _, _ := player.snapshot(); return len(a) == 2 })
	second := func() string { a, _, _, _ := player.snapshot(); return a[0] }()
	player.state(nativeplayer.StatusPlaying, 2)
	player.state(nativeplayer.StatusPlaying, 1)
	time.Sleep(300 * time.Millisecond)
	if !exists(second) {
		t.Fatal("a song the listener went back to was deleted")
	}
}

func TestDownloadArrivingAfterQueueEndResumesPlayback(t *testing.T) {
	q, player, source, _ := newQueue(t, time.Hour)
	block := make(chan struct{})
	source.data["music/two.mp3"] = nil
	slow := &slowSource{fakeSource: source, gate: block, key: "music/two.mp3", body: []byte("2222")}
	q.source = slow
	if err := q.Play(context.Background(), "b", songs[:2], nil); err != nil {
		t.Fatal(err)
	}
	player.state(nativeplayer.StatusEnded, 0)
	close(block)
	eventually(t, "playback resumed", func() bool { _, _, nexts, _ := player.snapshot(); return nexts == 1 })
}

type slowSource struct {
	*fakeSource
	gate <-chan struct{}
	key  string
	body []byte
}

func (s *slowSource) GetObject(ctx context.Context, bucket, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error) {
	if key == s.key {
		<-s.gate
		return adminv1.ObjectStream{Body: io.NopCloser(bytes.NewReader(s.body)), Status: http.StatusOK}, nil
	}
	return s.fakeSource.GetObject(ctx, bucket, key, opts)
}

func TestFailedDownloadIsReportedAndSkipped(t *testing.T) {
	q, player, source, _ := newQueue(t, time.Hour)
	source.fail["music/two.mp3"] = true
	var mu sync.Mutex
	var reports []error
	if err := q.Play(context.Background(), "b", songs, func(err error) { mu.Lock(); reports = append(reports, err); mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	player.state(nativeplayer.StatusPlaying, 0)
	eventually(t, "skip to the third song", func() bool {
		a, _, _, _ := player.snapshot()
		return len(a) == 1 && filepath.Base(a[0]) == "three.mp3"
	})
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 || !errors.Is(reports[0], ErrDownload) || !strings.Contains(reports[0].Error(), "two.mp3") {
		t.Fatalf("reports = %v", reports)
	}
}

func TestClosingThePlayerCleansTheQueueUpLater(t *testing.T) {
	q, player, _, root := newQueue(t, 100*time.Millisecond)
	if err := q.Play(context.Background(), "b", songs, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second song cached", func() bool { a, _, _, _ := player.snapshot(); return len(a) == 1 })
	player.state(nativeplayer.StatusClosed, 0)
	eventually(t, "player released", func() bool { _, _, _, c := player.snapshot(); return len(c) == 1 })
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("queue folder removed immediately on close")
	}
	eventually(t, "queue folder removed", func() bool { e, _ := os.ReadDir(root); return len(e) == 0 })
}

func TestNewPlayReplacesTheQueueAndOpenErrorsReturn(t *testing.T) {
	q, player, _, _ := newQueue(t, time.Hour)
	if err := q.Play(context.Background(), "b", songs, nil); err != nil {
		t.Fatal(err)
	}
	if err := q.Play(context.Background(), "b", songs[1:], nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, closed := player.snapshot(); len(closed) != 1 {
		t.Fatalf("previous queue not closed: %v", closed)
	}
	player.openErr = errors.New("mpv missing")
	if err := q.Play(context.Background(), "b", songs, nil); err == nil {
		t.Fatal("open error was swallowed")
	}
}

func TestSweepRemovesOnlyOldQueueFolders(t *testing.T) {
	root := t.TempDir()
	old, fresh, other := filepath.Join(root, dirPrefix+"old"), filepath.Join(root, dirPrefix+"new"), filepath.Join(root, "keep")
	for _, d := range []string{old, fresh, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(other, past, past)
	Sweep(root, time.Now())
	if exists(old) || !exists(fresh) || !exists(other) {
		t.Fatalf("old=%v fresh=%v other=%v", exists(old), exists(fresh), exists(other))
	}
}
