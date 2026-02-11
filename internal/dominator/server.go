package dominator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
)

type Server struct {
	addr    string
	catalog service.HostCatalogService
}

func NewServer(addr string) *Server {
	return &Server{
		addr: addr,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/state", s.handleState)

	log.Printf("Dominator listening on %s\n", s.addr)

	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("hostID")

	if hostID == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// get host based on token
	s.catalog.GetByID(context.Background(), hostID)
	log.Printf("State requested: host_id=%s\n",
		hostID,
	)

	desired := Resolve(hostID) // We'll improve this later

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(desired); err != nil {
		log.Printf("failed to encode response: %v\n", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
}
