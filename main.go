// Command mindmap serves the map and answers questions asked on it.
//
// The map is a mermaid document in an append-only log. A question asked on a
// node becomes a job written to disk before it runs, so closing the laptop
// loses nothing and a restart picks up where it stopped.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:4455", "listen address")
		dir     = flag.String("dir", ".", "where the log, orders and page live")
		briefs  = flag.String("briefs", "./briefs", "where emitted briefs land")
		model   = flag.String("model", "fake", "fake | pi | pi-tools | claude")
		workers = flag.Int("workers", 2, "jobs answered at once")
	)
	flag.Parse()

	root, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	briefDir := *briefs
	if !filepath.IsAbs(briefDir) {
		briefDir = filepath.Join(root, briefDir)
	}

	doc, err := OpenDoc(filepath.Join(root, "mindmap.log.jsonl"))
	if err != nil {
		log.Fatalf("document log: %v", err)
	}

	hub := NewHub()
	queue, err := OpenQueue(filepath.Join(root, "jobs.log.jsonl"), hub, *workers)
	if err != nil {
		log.Fatalf("job log: %v", err)
	}

	var m Model
	switch *model {
	case "fake":
		m = FakeModel{Delay: 55 * time.Millisecond}
	case "pi":
		m = PiAgent{}
	case "pi-tools":
		m = PiAgent{Tools: true}
	case "claude":
		m = ClaudeCLI{}
	default:
		log.Fatalf("unknown model %q, want fake, pi, pi-tools or claude", *model)
	}

	// Cancelled on signal, and every job inherits it, so shutdown stops work
	// deliberately rather than by the process vanishing underneath it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	queue.Start(ctx, m, *workers)
	if n := queue.Resume(); n > 0 {
		log.Printf("re-queued %d job(s) left running by the last process", n)
	}

	srv := &Server{
		doc:    doc,
		queue:  queue,
		orders: &Orders{dir: filepath.Join(root, "orders"), briefs: briefDir},
		hub:    hub,
		page:   filepath.Join(root, "index.html"),
	}

	h := &http.Server{
		Addr:        *addr,
		Handler:     logging(srv.Routes()),
		ReadTimeout: 30 * time.Second,
		// No WriteTimeout: the event stream is meant to stay open.
		IdleTimeout: 120 * time.Second,
	}

	go func() {
		log.Printf("mindmap on http://%s  model=%s  workers=%d", *addr, *model, *workers)
		if err := h.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("stopping")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.Shutdown(shutCtx)
}
