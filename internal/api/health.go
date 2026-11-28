package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
)

type Pinger interface {
	Ping(context.Context) error
}

type Service struct {
	ready atomic.Pointer[readyService]
}

type readyService struct {
	eng     *flow.Engine
	store   Pinger
	handler http.Handler
}

func NewService() *Service { return &Service{} }

func (s *Service) Ready(eng *flow.Engine, store Pinger) {
	s.ready.Store(&readyService{eng: eng, store: store, handler: Handler(eng)})
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ready := s.ready.Load()
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		switch r.URL.Path {
		case "/healthz":
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		case "/readyz":
			w.Header().Set("Cache-Control", "no-store")
			if ready == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := ready.store.Ping(ctx); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "last_seq": ready.eng.LastSeq()})
			return
		}
	}
	if ready == nil {
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusServiceUnavailable, flow.Error{Code: "unavailable", Message: "metronome is starting"})
		return
	}
	ready.handler.ServeHTTP(w, r)
}
