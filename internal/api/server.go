package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/ws"
)

type Server struct {
	db     *db.DB
	hub    *ws.Hub
	router *chi.Mux
	addr   string
}

func NewServer(database *db.DB, addr string) *Server {
	s := &Server{
		db:   database,
		hub:  ws.NewHub(),
		addr: addr,
	}
	go s.hub.Run()
	s.router = s.setupRoutes()
	return s
}

func (s *Server) Start() error {
	srv := &http.Server{
		Addr:         s.addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	log.Printf("Vault API server listening on %s", s.addr)
	return srv.ListenAndServe()
}

func (s *Server) StartWithContext(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("Vault API server listening on %s", s.addr)
	return srv.ListenAndServe()
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
