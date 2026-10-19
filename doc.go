package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// Revision is one saved state of the map. The file is append-only, so nothing
// a user typed is ever overwritten and the whole history is a `tail` away.
type Revision struct {
	N    int    `json:"n"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
	Src  string `json:"src"`
}

// Doc is the document log. Reads are served from memory; every write goes to
// disk before it is acknowledged, so a crash can only lose what was never
// confirmed.
type Doc struct {
	path string

	mu   sync.RWMutex
	revs []Revision
}

func OpenDoc(path string) (*Doc, error) {
	d := &Doc{path: path}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // a map outgrows the default
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r Revision
		// A malformed line is skipped rather than fatal: one bad write must
		// not make every earlier revision unreachable.
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			// The revision number is where the line sits in the log, not
			// something a line claims about itself. Older writers did not
			// record it at all.
			r.N = len(d.revs) + 1
			d.revs = append(d.revs, r)
		}
	}
	return d, sc.Err()
}

func (d *Doc) Latest() Revision {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.revs) == 0 {
		return Revision{}
	}
	return d.revs[len(d.revs)-1]
}

// History omits Src: the list is for choosing a revision, and shipping every
// version of the document to render a dropdown is wasteful.
func (d *Doc) History() []Revision {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Revision, 0, len(d.revs))
	for i := len(d.revs) - 1; i >= 0; i-- {
		r := d.revs[i]
		r.Src = ""
		out = append(out, r)
	}
	return out
}

func (d *Doc) Save(src, note string) (Revision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	r := Revision{N: len(d.revs) + 1, At: time.Now().Format("2006-01-02T15:04:05"), Note: note, Src: src}
	line, err := json.Marshal(r)
	if err != nil {
		return Revision{}, err
	}

	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Revision{}, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return Revision{}, err
	}
	// Only once it is on disk does the in-memory log admit it exists.
	d.revs = append(d.revs, r)
	return r, nil
}
