package log

import (
	"context"
	"database/sql"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func TestProjectorNeverReadsClock(t *testing.T) {
	t.Run("package imports neither time nor the clock", func(t *testing.T) {
		files, err := filepath.Glob("*.go")
		if err != nil {
			t.Fatalf("glob: %v", err)
		}
		fset := token.NewFileSet()
		checked := 0
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			checked++
			for _, spec := range parsed.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatalf("unquote import: %v", err)
				}
				if path == "time" || strings.HasSuffix(path, "/platform/clock") {
					t.Fatalf("%s imports %q, the projector must not read the clock", name, path)
				}
			}
		}
		if checked == 0 {
			t.Fatal("no source files checked")
		}
	})

	t.Run("projected rows do not depend on when they are projected", func(t *testing.T) {
		project := func() string {
			db := openProjectionDB(t)
			projector := newTestProjector(t, map[wire.OpType]projection.Applier{wire.OpBucketCreate: insertBucketApplier()})
			texts := []string{
				formatted(t, bucketCreateOp(testJobID, 3600)),
				"TDX1|t=zap|j=" + otherJobID,
				"TDX10|t=put",
			}
			for i, text := range texts {
				if _, err := projector.ProjectFromOp(context.Background(), db, observed(int64(i+1), text)); err != nil {
					t.Fatalf("project %d: %v", i+1, err)
				}
			}
			if _, err := projector.ProjectFromOp(context.Background(), db, observed(1, texts[0]+"|edit=1")); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			return dumpTables(t, db)
		}
		first := project()
		time.Sleep(1100 * time.Millisecond)
		if second := project(); second != first {
			t.Fatalf("rows differ between two runs at different wall times:\n%s\nvs\n%s", first, second)
		}
	})
}

func TestNoTxAcrossNetwork(t *testing.T) {
	const (
		scriptedDelay  = 2 * time.Second
		writerBudget   = 100 * time.Millisecond
		concurrentOpen = 8
	)
	db := openProjectionDB(t)
	projector := bucketProjector(t)
	text := formatted(t, bucketCreateOp(testJobID, 3600))

	scriptedNetworkCall := func() { time.Sleep(scriptedDelay) }

	pipelineDone := make(chan error, 1)
	networkStarted := make(chan struct{})
	go func() {
		close(networkStarted)
		scriptedNetworkCall()
		_, err := projector.ProjectFromOp(context.Background(), db, observed(1, text))
		pipelineDone <- err
	}()
	<-networkStarted

	var wg sync.WaitGroup
	elapsed := make([]time.Duration, concurrentOpen)
	failures := make([]error, concurrentOpen)
	for i := 0; i < concurrentOpen; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			failures[i] = db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?, ?, 'cleanup', 0)`, testChannel, i+100)
				return err
			})
			elapsed[i] = time.Since(started)
		}()
	}
	wg.Wait()
	for i := 0; i < concurrentOpen; i++ {
		if failures[i] != nil {
			t.Fatalf("concurrent write %d: %v", i, failures[i])
		}
		if elapsed[i] >= writerBudget {
			t.Fatalf("concurrent write %d took %v during the scripted network delay, want under %v", i, elapsed[i], writerBudget)
		}
	}
	if err := <-pipelineDone; err != nil {
		t.Fatalf("project after the network call: %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log`); got != 1 {
		t.Fatalf("replay_log rows = %d, want 1", got)
	}
}
