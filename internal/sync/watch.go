package sync

import (
	"context"
	"math/rand"
	"strings"
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
func Watch(ctx context.Context, c *Config, opt WatchOptions) error {
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	cl := NewClient(c.Server, c.MachineToken)

	var nextSync time.Time
	if opt.SyncEvery > 0 {
		nextSync = time.Now() // reconcile once on startup
	}

	backoff := time.Second
	logf("watching %s for jobs (poll)", c.Server)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if opt.SyncEvery > 0 && !time.Now().Before(nextSync) {
			if _, err := Run(ctx, c, Options{Inventory: true, Log: func(string, ...any) {}}); err != nil {
				logf("sync failed: %v", err)
			} else {
				logf("sync ok")
			}
			nextSync = time.Now().Add(opt.SyncEvery)
		}

		resp, err := cl.Poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Network hiccup or server restart: back off, then keep trying.
			logf("poll: %v (retry in %s)", err, backoff)
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
			logf("job #%d %s %s", j.ID, j.Type, summarize(j))
			out, jerr := runJob(ctx, j)
			res := protocol.JobResult{ID: j.ID, Status: "done", Output: out}
			if jerr != nil {
				res.Status = "failed"
				res.Output = out + "\nerror: " + jerr.Error()
			}
			if err := cl.JobResult(ctx, res); err != nil {
				logf("job #%d: reporting result failed: %v", j.ID, err)
			} else {
				logf("job #%d %s", j.ID, res.Status)
			}
		}
	}
}

func summarize(j protocol.Job) string {
	s := string(j.Payload)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
