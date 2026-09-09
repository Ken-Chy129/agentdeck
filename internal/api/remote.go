package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/protocol"
	"github.com/Ken-Chy129/agentdeck/internal/store"
)

// pollTimeout is how long a machine parks its request before we answer empty.
// Well under typical 60s proxy idle timeouts, so Caddy/nginx won't cut it.
const pollTimeout = 45 * time.Second

// agentPoll is a long poll: the machine asks "anything for me?" and we hold the
// request until a job appears (or we time out). This is what turns a 15-minute
// wait into a couple of seconds without needing to reach into the machine.
func (s *Server) agentPoll(w http.ResponseWriter, r *http.Request) {
	m := machineFrom(r)
	ctx := r.Context()

	if jobs := s.queuedJobs(w, r); jobs == nil || len(jobs) > 0 {
		return
	}

	ch, done := s.wake.wait(m.ID)
	defer done()

	select {
	case <-ch:
		s.queuedJobs(w, r)
	case <-time.After(pollTimeout):
		writeJSON(w, 200, protocol.PollResponse{Jobs: []protocol.Job{}})
	case <-ctx.Done():
	}
}

// queuedJobs writes the machine's queued jobs. Returns nil if it already wrote
// an error, otherwise the (possibly empty) job list it wrote.
func (s *Server) queuedJobs(w http.ResponseWriter, r *http.Request) []protocol.Job {
	m := machineFrom(r)
	rows, err := s.st.ClaimJobs(r.Context(), m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil
	}
	out := []protocol.Job{}
	for _, j := range rows {
		out = append(out, protocol.Job{ID: j.ID, Type: j.Type, Payload: j.Payload})
	}
	if len(out) > 0 {
		writeJSON(w, 200, protocol.PollResponse{Jobs: out})
	}
	return out
}

// agentJobResult reports one finished job immediately, so output shows up in
// the console as soon as the command ends instead of at the next full sync.
func (s *Server) agentJobResult(w http.ResponseWriter, r *http.Request) {
	m := machineFrom(r)
	var res protocol.JobResult
	if err := decode(r, &res); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	st := "failed"
	if res.Status == "done" {
		st = "done"
	}
	if err := s.st.FinishJob(r.Context(), m.ID, pathID(r), st, res.Output); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// done reports whether a job has reached a terminal state. Claimed-but-running
// jobs are still in flight, so the console keeps waiting on them.
func jobDone(status string) bool {
	return status == "done" || status == "failed" || status == "cancelled"
}

// runShellNow queues a shell job, wakes the machine, and waits for the result
// so the console can behave like a terminal instead of a ticket queue.
func (s *Server) runShell(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd        string `json:"cmd"`
		Cwd        string `json:"cwd"`
		TimeoutSec int    `json:"timeout_sec"`
		WaitSec    int    `json:"wait_sec"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Cmd == "" {
		writeErr(w, 400, "cmd required")
		return
	}
	id := r.PathValue("id")
	if _, err := s.st.MachineByID(r.Context(), id); err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	payload, _ := json.Marshal(map[string]any{"cmd": req.Cmd, "cwd": req.Cwd, "timeout_sec": req.TimeoutSec})
	j, err := s.st.CreateJob(r.Context(), id, protocol.JobShell, json.RawMessage(payload))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.st.Audit(r.Context(), "admin", "shell", id, req.Cmd)
	s.wake.notify(id)
	s.waitForJob(w, r, j, req.WaitSec, 30)
}

// syncNow queues a sync job and wakes the machine so an admin doesn't have to
// wait for the next scheduled reconcile. Machines that aren't watching pick the
// job up on their next scheduled sync instead, hence the "queued" answer.
func (s *Server) syncNow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WaitSec int `json:"wait_sec"`
	}
	// Body is optional: a bare POST means "use the default wait".
	if r.ContentLength > 0 {
		if err := decode(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	id := r.PathValue("id")
	if _, err := s.st.MachineByID(r.Context(), id); err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	// Don't pile up requests: an already-queued sync will reconcile the same
	// desired state, so reuse it.
	j, err := s.pendingSyncJob(r, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if j == nil {
		if j, err = s.st.CreateJob(r.Context(), id, protocol.JobSync, json.RawMessage(`{}`)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.st.Audit(r.Context(), "admin", "sync", id, "sync now")
	}
	s.wake.notify(id)
	s.waitForJob(w, r, j, req.WaitSec, 45)
}

// pendingSyncJob returns an unfinished sync job for the machine, if any.
func (s *Server) pendingSyncJob(r *http.Request, machineID string) (*store.Job, error) {
	jobs, err := s.st.QueuedJobs(r.Context(), machineID)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Type == protocol.JobSync {
			return j, nil
		}
	}
	return nil, nil
}

// waitForJob holds the request until the job finishes or the wait budget runs
// out, then answers with the job's latest state either way. The console keeps
// polling /api/admin/jobs/{id} when it's still running.
func (s *Server) waitForJob(w http.ResponseWriter, r *http.Request, j *store.Job, wait, def int) {
	if wait <= 0 {
		wait = def
	}
	if wait > 120 {
		wait = 120
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		cur, err := s.st.JobByID(r.Context(), j.ID)
		if err != nil {
			continue
		}
		if jobDone(cur.Status) {
			writeJSON(w, 200, cur)
			return
		}
	}
	cur, err := s.st.JobByID(r.Context(), j.ID)
	if err != nil {
		writeJSON(w, 200, j)
		return
	}
	writeJSON(w, 200, cur)
}
