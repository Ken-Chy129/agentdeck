package api

import "sync"

// waker lets an admin action (queueing a job) immediately release a machine's
// long-poll request, so a command runs within seconds instead of waiting for
// the next scheduled sync. Machines behind NAT stay in control: they dial out,
// the server never connects in.
type waker struct {
	mu sync.Mutex
	ch map[string][]chan struct{}
}

func newWaker() *waker { return &waker{ch: map[string][]chan struct{}{}} }

// wait registers a listener for a machine and returns it plus a cleanup func.
func (w *waker) wait(machineID string) (chan struct{}, func()) {
	c := make(chan struct{}, 1)
	w.mu.Lock()
	w.ch[machineID] = append(w.ch[machineID], c)
	w.mu.Unlock()
	return c, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		list := w.ch[machineID]
		for i, x := range list {
			if x == c {
				w.ch[machineID] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(w.ch[machineID]) == 0 {
			delete(w.ch, machineID)
		}
	}
}

// notify releases every listener currently waiting on a machine.
func (w *waker) notify(machineID string) {
	w.mu.Lock()
	list := append([]chan struct{}(nil), w.ch[machineID]...)
	w.mu.Unlock()
	for _, c := range list {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// waiting reports how many machines currently hold an open long poll.
func (w *waker) waiting() map[string]bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]bool, len(w.ch))
	for id, list := range w.ch {
		if len(list) > 0 {
			out[id] = true
		}
	}
	return out
}
