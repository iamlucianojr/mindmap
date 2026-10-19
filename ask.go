package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// Job states. A job is always at exactly one of them, which is what lets boot
// tell an interrupted job from a finished one.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

type Job struct {
	ID string `json:"id"`
	// Kind is the seam. "ask" answers a question; a station of the delivery
	// line is the same thing with a longer prompt and a file to write, so it
	// can join this queue without a second one being invented.
	Kind   string   `json:"kind"`
	Node   string   `json:"node"`
	Prompt string   `json:"prompt"`
	Path   []string `json:"path,omitempty"`
	State  string   `json:"state"`
	Answer string   `json:"answer,omitempty"`
	Err    string   `json:"err,omitempty"`
	At     string   `json:"at"`
}

// JobID is derived from the question and where it was asked, so asking twice
// is asking once. Reword the prompt and it is honestly a different question.
func JobID(path []string, prompt string) string {
	sum := sha256.Sum256([]byte(strings.Join(path, "\x00") + "\x00" + prompt))
	return hex.EncodeToString(sum[:])[:12]
}

// Queue is durable by being written before it runs. A goroutine dies with the
// process; a line in a file does not.
type Queue struct {
	path string
	hub  *Hub

	mu   sync.RWMutex
	jobs map[string]*Job
	ord  []string // insertion order, so listing is stable

	ch chan string
}

func OpenQueue(path string, hub *Hub, workers int) (*Queue, error) {
	q := &Queue{path: path, hub: hub, jobs: map[string]*Job{}, ch: make(chan string, 256)}

	f, err := os.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var j Job
			if len(sc.Bytes()) == 0 || json.Unmarshal(sc.Bytes(), &j) != nil {
				continue
			}
			// Last line for an id wins: the log records transitions, and the
			// most recent one is the job's current state.
			if _, seen := q.jobs[j.ID]; !seen {
				q.ord = append(q.ord, j.ID)
			}
			c := j
			q.jobs[j.ID] = &c
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return q, nil
}

// Resume re-queues anything that was mid-flight when the process died. At
// least once is the right failure direction here: asking again is harmless,
// losing the answer is not.
func (q *Queue) Resume() int {
	q.mu.Lock()
	var again []*Job
	for _, id := range q.ord {
		if j := q.jobs[id]; j.State == StateRunning || j.State == StateQueued {
			j.State = StateQueued
			again = append(again, j)
		}
	}
	q.mu.Unlock()

	for _, j := range again {
		q.appendLine(j)
		q.ch <- j.ID
	}
	return len(again)
}

func (q *Queue) Start(ctx context.Context, m Model, workers int) {
	for i := 0; i < workers; i++ {
		go q.worker(ctx, m)
	}
}

// Submit is idempotent on the job id, and never blocks on the model.
func (q *Queue) Submit(kind, node, prompt string, path []string) *Job {
	id := JobID(path, prompt)

	q.mu.Lock()
	if j, ok := q.jobs[id]; ok {
		c := *j
		q.mu.Unlock()
		return &c // the same question already has an answer, or is getting one
	}
	j := &Job{ID: id, Kind: kind, Node: node, Prompt: prompt, Path: path,
		State: StateQueued, At: time.Now().Format(time.RFC3339)}
	q.jobs[id] = j
	q.ord = append(q.ord, id)
	c := *j
	q.mu.Unlock()

	q.appendLine(&c) // on disk before anyone is told it exists
	q.hub.Send("job", c)
	q.ch <- id
	return &c
}

func (q *Queue) Get(id string) (Job, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	j, ok := q.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (q *Queue) List() []Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make([]Job, 0, len(q.ord))
	for i := len(q.ord) - 1; i >= 0; i-- {
		out = append(out, *q.jobs[q.ord[i]])
	}
	return out
}

func (q *Queue) worker(ctx context.Context, m Model) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q.ch:
			q.run(ctx, m, id)
		}
	}
}

func (q *Queue) run(ctx context.Context, m Model, id string) {
	j, ok := q.Get(id)
	if !ok || j.State != StateQueued {
		return
	}
	q.set(id, func(j *Job) { j.State = StateRunning })

	// The deadline belongs to the job, not to whoever asked. Closing the tab
	// cancels the waiting, never the work.
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	out := make(chan string, 64)
	go func() {
		for tok := range out {
			q.hub.Send("token", map[string]string{"id": id, "text": tok})
		}
	}()

	text, err := m.Ask(runCtx, Prompt{Question: j.Prompt, Path: j.Path}, out)
	close(out)

	q.set(id, func(j *Job) {
		switch {
		case err != nil && runCtx.Err() != nil:
			j.State, j.Err = StateCancelled, runCtx.Err().Error()
		case err != nil:
			j.State, j.Err = StateFailed, err.Error()
		default:
			j.State, j.Answer = StateDone, text
		}
	})
}

func (q *Queue) set(id string, fn func(*Job)) {
	q.mu.Lock()
	j, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return
	}
	fn(j)
	c := *j
	q.mu.Unlock()

	q.appendLine(&c)
	q.hub.Send("job", c)
}

func (q *Queue) appendLine(j *Job) {
	line, err := json.Marshal(j)
	if err != nil {
		return
	}
	f, err := os.OpenFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
