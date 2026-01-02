package http

import (
	"encoding/json"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
	"net/http"
)

type Handler struct {
	catalog *service.HostCatalogService
}

func (h *Handler) TransitionState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id := r.PathValue("id")
	newState := r.Header.Get("X-New-State")

	if id == "" || newState == "" {
		http.Error(w, "missing id or state", http.StatusBadRequest)
		return
	}

	err := h.catalog.TransitionState(ctx, id, newState)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TransitionHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id := r.PathValue("id")

	var data api.HealthRequest

	// Create a JSON decoder and decode the body into the struct
	err := json.NewDecoder(r.Body).Decode(&data)
	if err != nil {
		// Handle errors (e.g., malformed JSON, wrong field types)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if id == "" || data.Health == "" {
		http.Error(w, "missing id or state", http.StatusBadRequest)
		return
	}

	err = h.catalog.TransitionHealth(ctx, id, data.Health)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
