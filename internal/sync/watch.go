package sync

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

// WatchOptions configures the long-poll worker.
type WatchOptions struct {
	// SyncEvery triggers a full reconcile on this interval (0 disables it, in
	// which case you keep the scheduled timer for syncs).
	SyncEvery time.Duration
	Log       func(format string, a ...any)
}

// Watch keeps a long poll open against the server and runs jobs as they
// arrive, so a command typed in the console executes within seconds. The
// machine always dials out, which is what makes this work from a NAT'd box
// that the server cannot reach.
//
// Polling and execution run in separate goroutines on purpose. When they shared
// one loop, a machine stopped listening while it worked: a 15s reconcile or a
// slow upgrade meant the next command sat in the queue until the machine got
// around to polling again, which read as "this machine takes forever to pick
// things up". Now the poll is always parked on the server, and work happens
// alongside it.
func Watch(ctx context.Context, c *Config, opt WatchOptions) error {
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cl := NewClient(c.Server, c.MachineToken)
	w := &watcher{c: c, cl: cl, logf: logf, every: opt.SyncEvery, jobs: make(chan protocol.Job, 32)}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.work(ctx)
	}()

	logf("watching %s for jobs (poll)", c.Server)
	err := w.poll(ctx)
	cancel()
	wg.Wait()
	return err
}

type watcher struct {
	c     *Config
	cl    *Client
	logf  func(string, ...any)
	every time.Duration
	jobs  chan protocol.Job

	// pending holds jobs pulled off the channel while coalescing syncs. They run
	// before anything still queued, preserving the order they were created in.
	pending []protocol.Job

	// syncAt guards the periodic reconcile schedule, which both the timer and a
	// console-triggered sync push forward.
	mu     sync.Mutex
	syncAt time.Time
}

// next returns the job to run, preferring ones we already took off the channel.
func (w *watcher) next(ctx context.Context) (protocol.Job, bool) {
	if len(w.pending) > 0 {
		j := w.pending[0]
		w.pending = w.pending[1:]
		return j, true
	}
	select {
	case j := <-w.jobs:
		return j, true
	case <-ctx.Done():
		return protocol.Job{}, false
	}
}

// poll keeps one request parked on the server at all times and hands whatever
// arrives to the worker without waiting for it to finish.
func (w *watcher) poll(ctx context.Context) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := w.cl.Poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Network hiccup or server restart: back off, then keep trying.
			w.logf("poll: %v (retry in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff + time.Duration(rand.Int63n(int64(time.Second)))):
			}
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, j := range resp.Jobs {
			select {
			case w.jobs <- j:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// work runs jobs one at a time and fires the periodic reconcile. Serialising
// here is deliberate: two upgrades racing on the same machine is worse than a
// short queue.
func (w *watcher) work(ctx context.Context) {
	if w.every > 0 {
		w.setNextSync(time.Now()) // reconcile once on startup
	}
	for {
		if due, wait := w.syncDue(); due {
			w.runPeriodicSync(ctx)
			continue
		} else if wait > 0 && len(w.pending) == 0 {
			select {
			case <-ctx.Done():
				return
			case j := <-w.jobs:
				w.dispatch(ctx, j)
			case <-time.After(wait):
			}
			continue
		}
		j, ok := w.next(ctx)
		if !ok {
			return
		}
		w.dispatch(ctx, j)
	}
}

// dispatch runs one job. Sync requests are coalesced: several clicks on
// "立即同步" only deserve one reconcile.
func (w *watcher) dispatch(ctx context.Context, j protocol.Job) {
	if j.Type == protocol.JobSync {
		w.runSyncJobs(ctx, append([]protocol.Job{j}, w.drainSyncJobs()...))
		return
	}

	w.logf("job #%d %s %s", j.ID, j.Type, summarize(j))
	out, jerr := runJob(ctx, j)
	res := protocol.JobResult{ID: j.ID, Status: "done", Output: out}
	if jerr != nil {
		res.Status = "failed"
		res.Output = out + "\nerror: " + jerr.Error()
	}
	if err := w.cl.JobResult(ctx, res); err != nil {
		w.logf("job #%d: reporting result failed: %v", j.ID, err)
	} else {
		w.logf("job #%d %s", j.ID, res.Status)
	}
}

// drainSyncJobs takes the sync requests already queued behind us and stops at
// the first job that isn't one. Stopping matters: pushing a shell job back onto
// the channel would move it behind work that arrived later, silently reordering
// what the user queued.
func (w *watcher) drainSyncJobs() []protocol.Job {
	var out []protocol.Job
	for {
		select {
		case j := <-w.jobs:
			if j.Type != protocol.JobSync {
				w.pending = append(w.pending, j)
				return out
			}
			out = append(out, j)
		default:
			return out
		}
	}
}

func (w *watcher) runSyncJobs(ctx context.Context, jobs []protocol.Job) {
	w.logf("sync requested from console (%d job(s))", len(jobs))
	out, serr := runSyncJob(ctx, w.c)
	status := "done"
	if serr != nil {
		status = "failed"
		out += "\nerror: " + serr.Error()
	}
	// The manual run counts as the periodic one, so we don't reconcile twice
	// back to back.
	if w.every > 0 {
		w.setNextSync(time.Now().Add(w.every))
	}
	for _, j := range jobs {
		if err := w.cl.JobResult(ctx, protocol.JobResult{ID: j.ID, Status: status, Output: out}); err != nil {
			w.logf("job #%d: reporting result failed: %v", j.ID, err)
		}
	}
	w.logf("sync job %s", status)
}

func (w *watcher) runPeriodicSync(ctx context.Context) {
	if _, err := Run(ctx, w.c, Options{Inventory: true, Log: func(string, ...any) {}}); err != nil {
		w.logf("sync failed: %v", err)
	} else {
		w.logf("sync ok")
	}
	w.setNextSync(time.Now().Add(w.every))
}

// syncDue reports whether the periodic reconcile should run now, or how long to
// wait for it. A zero wait means there's nothing scheduled.
func (w *watcher) syncDue() (bool, time.Duration) {
	if w.every <= 0 {
		return false, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	d := time.Until(w.syncAt)
	if d <= 0 {
		return true, 0
	}
	return false, d
}

func (w *watcher) setNextSync(t time.Time) {
	w.mu.Lock()
	w.syncAt = t
	w.mu.Unlock()
}

func summarize(j protocol.Job) string {
	s := string(j.Payload)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// runSyncJob performs the reconcile a console "sync now" asks for and returns a
// human-readable summary for the job output.
func runSyncJob(ctx context.Context, c *Config) (string, error) {
	rep, err := Run(ctx, c, Options{Inventory: true, Log: func(string, ...any) {}})
	if err != nil {
		return "", err
	}
	return Summary(rep), nil
}
