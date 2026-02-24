package dominator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type Server struct {
	addr     string
	db       *sql.DB
	catalog  *service.HostCatalogService
	resolver *PolicyResolver
}

func NewServer(addr string, db *sql.DB, catalog *service.HostCatalogService, resolver *PolicyResolver) *Server {
	return &Server{
		addr:     addr,
		db:       db,
		catalog:  catalog,
		resolver: resolver,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/heartbeat", s.handleState)
	mux.HandleFunc("/v1/health", s.Health)
	mux.HandleFunc("/v1/db/health", s.dbHealth)

	log.Printf("Dominator listening on %s\n", s.addr)

	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) dbHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Ping(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status": "unavailable", "error": "%v"}`, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status": "available"}`)
}

func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	log.Println("/Dominator/Health")

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Dominator listening on %s\n", s.addr)
}

func (s *Server) recordState(host *api.Host, body []byte) error {
	s.catalog.TransitionHealth(context.Background(), host.ID, string(api.HostHealthHealthy))
	s.catalog.UpdateLastSeenHeartbeat(context.Background(), host.ID, time.Now())

	return nil
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("hostID")
	log.Printf("/Dominator/HandleState/%s\n", hostID)

	if hostID == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// get host based on token
	host, err := s.catalog.GetByID(context.Background(), hostID)
	if err != nil {
		log.Printf("Error in /handleState/GetByID: %s\n", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}

	log.Printf("State requested: host_id=%s\n",
		hostID,
	)

	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Failed to read body: %s", body)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	err = s.recordState(host, body)
	if err != nil {
		log.Printf("Failed to record state: %s", body)
		http.Error(w, "failed to record state", http.StatusInternalServerError)
		return
	}

	desired, err := s.resolver.Resolve(host)
	if err != nil {
		log.Printf("Error in /handleState/Resolve: %s\n", err)
		http.Error(w, "Error in /handleState/Resolve", http.StatusInternalServerError)
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(desired); err != nil {
		log.Printf("failed to encode response: %v\n", err)
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}
