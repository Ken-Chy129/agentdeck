package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

// The whole point of splitting poll from execution: work arriving while the
// machine is busy still gets queued immediately instead of waiting for the
// current job to finish.
func TestWatcherQueuesWhileBusy(t *testing.T) {
	w := &watcher{jobs: make(chan protocol.Job, 32), logf: func(string, ...any) {}}
	for i := 0; i < 5; i++ {
		select {
		case w.jobs <- protocol.Job{ID: int64(i), Type: protocol.JobEcho}:
		default:
			t.Fatalf("queue rejected job %d; poll would have blocked on a busy machine", i)
		}
	}
	if len(w.jobs) != 5 {
		t.Fatalf("want 5 queued jobs, got %d", len(w.jobs))
	}
}

// Several clicks on "立即同步" should cost one reconcile, not one each.
func TestDrainSyncJobsCoalesces(t *testing.T) {
	w := &watcher{jobs: make(chan protocol.Job, 32), logf: func(string, ...any) {}}
	w.jobs <- protocol.Job{ID: 2, Type: protocol.JobSync}
	w.jobs <- protocol.Job{ID: 3, Type: protocol.JobSync}

	got := w.drainSyncJobs()
	if len(got) != 2 {
		t.Fatalf("want both sync jobs coalesced, got %d", len(got))
	}
}

// Coalescing must not swallow real work queued behind a sync.
func TestDrainSyncJobsKeepsOtherWork(t *testing.T) {
	w := &watcher{jobs: make(chan protocol.Job, 32), logf: func(string, ...any) {}}
	w.jobs <- protocol.Job{ID: 2, Type: protocol.JobSync}
	w.jobs <- protocol.Job{ID: 3, Type: protocol.JobShell}
	w.jobs <- protocol.Job{ID: 4, Type: protocol.JobSync}

	got := w.drainSyncJobs()
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("want only the leading sync, got %v", got)
	}
	// The shell job keeps its place ahead of the sync that arrived after it.
	j, ok := w.next(context.Background())
	if !ok || j.ID != 3 || j.Type != protocol.JobShell {
		t.Fatalf("want the shell job next, got %+v (ok=%v)", j, ok)
	}
	j, ok = w.next(context.Background())
	if !ok || j.ID != 4 {
		t.Fatalf("want the trailing sync after it, got %+v (ok=%v)", j, ok)
	}
}

// A console sync pushes the periodic one out, so we don't reconcile twice in a
// row, and the worker doesn't spin when nothing is due.
func TestSyncSchedule(t *testing.T) {
	w := &watcher{jobs: make(chan protocol.Job, 1), every: time.Hour, logf: func(string, ...any) {}}
	w.setNextSync(time.Now().Add(-time.Second))
	if due, _ := w.syncDue(); !due {
		t.Fatal("want an overdue sync to be due")
	}
	w.setNextSync(time.Now().Add(w.every))
	due, wait := w.syncDue()
	if due {
		t.Fatal("want the sync deferred after it just ran")
	}
	if wait <= 0 || wait > time.Hour {
		t.Fatalf("want a positive wait under an hour, got %s", wait)
	}

	// every == 0 means "no periodic sync"; the worker should just block on jobs.
	idle := &watcher{jobs: make(chan protocol.Job, 1), logf: func(string, ...any) {}}
	if due, wait := idle.syncDue(); due || wait != 0 {
		t.Fatalf("want no schedule when every is 0, got due=%v wait=%s", due, wait)
	}
}

// The worker must keep draining jobs and honour cancellation.
func TestWorkerRunsQueuedJobsAndStops(t *testing.T) {
	w := &watcher{jobs: make(chan protocol.Job, 4), logf: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { w.work(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop on cancellation")
	}
}
