package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Hub fans server events out to every open page. One way, so there is no
// protocol to agree on and the browser reconnects by itself.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan string]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan string]struct{}{}} }

func (h *Hub) add() chan string {
	ch := make(chan string, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) drop(ch chan string) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *Hub) Send(event string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, b)

	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		// A page that has stopped reading must not stall every other page.
		select {
		case ch <- msg:
		default:
		}
	}
}

type Server struct {
	doc    *Doc
	queue  *Queue
	orders *Orders
	hub    *Hub
	page   string // served from disk so editing the page needs no rebuild
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.getPage)
	mux.HandleFunc("GET /api/doc", s.getDoc)
	mux.HandleFunc("POST /api/doc", s.postDoc)
	mux.HandleFunc("GET /api/history", s.getHistory)
	mux.HandleFunc("GET /api/order/{id}", s.getOrder)
	mux.HandleFunc("POST /api/order/{id}", s.postOrder)
	mux.HandleFunc("POST /api/workorder", s.postWorkOrder)
	mux.HandleFunc("GET /api/ask", s.listAsk)
	mux.HandleFunc("POST /api/ask", s.postAsk)
	mux.HandleFunc("GET /api/ask/{id}", s.getAsk)
	mux.HandleFunc("GET /api/events", s.events)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) getPage(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.page)
	if err != nil {
		http.Error(w, "page missing: "+s.page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) getDoc(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.doc.Latest())
}

func (s *Server) postDoc(w http.ResponseWriter, r *http.Request) {
	var in struct{ Src, Note string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "bad json")
		return
	}
	rev, err := s.doc.Save(in.Src, in.Note)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.doc.History())
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	d, err := s.orders.Description(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"description": d})
}

func (s *Server) postOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Description string `json:"description"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.orders.SetDescription(r.PathValue("id"), in.Description); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(in.Description)})
}

func (s *Server) postWorkOrder(w http.ResponseWriter, r *http.Request) {
	var in OrderReq
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "bad json")
		return
	}
	path, err := s.orders.Brief(in)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": in.ID, "brief": path})
}

func (s *Server) listAsk(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.queue.List())
}

func (s *Server) getAsk(w http.ResponseWriter, r *http.Request) {
	j, ok := s.queue.Get(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// postAsk returns immediately with an id. It never waits for the answer, and
// that single property is what the rest of the design rests on.
func (s *Server) postAsk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Node   string   `json:"node"`
		Prompt string   `json:"prompt"`
		Path   []string `json:"path"`
		Kind   string   `json:"kind"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(in.Prompt) == "" {
		fail(w, http.StatusBadRequest, "a question cannot be empty")
		return
	}
	if in.Kind == "" {
		in.Kind = "ask"
	}
	writeJSON(w, http.StatusAccepted, s.queue.Submit(in.Kind, in.Node, in.Prompt, in.Path))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	fl.Flush()

	ch := s.hub.add()
	defer s.hub.drop(ch)

	for {
		select {
		case <-r.Context().Done():
			return // the page went away; nothing it asked for is affected
		case msg := <-ch:
			if _, err := fmt.Fprint(w, msg); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/events") {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
