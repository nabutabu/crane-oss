package capacity

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
)

const maxBodyBytes = 1 << 20

// API exposes the small platform control-plane surface. Topology mutations
// require the configured admin bearer token; capacity mutations derive cluster
// identity from X-Crane-Cluster-ID and never accept a team from the request.
type API struct {
	store        *Store
	adminToken   string
	adapterToken string
}

// NewAPI builds the platform API. The first adapter token, when present, is
// required on capacity admission requests.
func NewAPI(store *Store, adminToken string, adapterTokens ...string) *API {
	apiHandler := &API{store: store, adminToken: adminToken}
	if len(adapterTokens) > 0 {
		apiHandler.adapterToken = adapterTokens[0]
	}
	return apiHandler
}

// Handler returns the router that maps each platform endpoint to its handler.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/zones", a.admin(a.createZone))
	mux.Handle("POST /v1/clusters", a.admin(a.createCluster))
	mux.Handle("POST /v1/node-pools", a.admin(a.createNodePool))
	mux.Handle("POST /v1/node-groups", a.admin(a.createNodeGroup))
	mux.Handle("POST /v1/first-zone", a.admin(a.seedFirstZone))
	mux.Handle("POST /v1/bootstrap", a.admin(a.createZoneBootstrap))
	mux.Handle("POST /v1/capacity", a.adapter(a.setTarget))
	return mux
}

// admin guards topology mutations behind the configured admin bearer token.
func (a *API) admin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.adminToken == "" || r.Header.Get("Authorization") != "Bearer "+a.adminToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adapter guards capacity admission behind the configured adapter bearer token.
func (a *API) adapter(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.adapterToken == "" || r.Header.Get("Authorization") != "Bearer "+a.adapterToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) createZone(w http.ResponseWriter, r *http.Request) {
	var in api.ZoneInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.CreateZone(r.Context(), in); err != nil {
		http.Error(w, err.Error(), status(err))
		return
	}
	writeJSON(w, in)
}

func (a *API) createCluster(w http.ResponseWriter, r *http.Request) {
	var in api.ClusterInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.CreateCluster(r.Context(), in); err != nil {
		http.Error(w, err.Error(), status(err))
		return
	}
	writeJSON(w, in)
}

func (a *API) createNodePool(w http.ResponseWriter, r *http.Request) {
	var in api.NodePoolInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.CreateNodePool(r.Context(), in); err != nil {
		http.Error(w, err.Error(), status(err))
		return
	}
	writeJSON(w, in)
}

func (a *API) createNodeGroup(w http.ResponseWriter, r *http.Request) {
	var in api.NodeGroupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.CreateNodeGroup(r.Context(), in); err != nil {
		http.Error(w, err.Error(), status(err))
		return
	}
	writeJSON(w, in)
}

func (a *API) seedFirstZone(w http.ResponseWriter, r *http.Request) {
	var in api.FirstZoneInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.SeedFirstZone(r.Context(), in.Zone, in.Cluster, in.Budget, in.Pool, in.Group); err != nil {
		http.Error(w, err.Error(), status(err))
		return
	}
	writeJSON(w, map[string]string{"status": "ready"})
}

type zoneBootstrapRequest struct {
	ZoneID     string          `json:"zone_id"`
	Parameters json.RawMessage `json:"parameters"`
}

func (a *API) createZoneBootstrap(w http.ResponseWriter, r *http.Request) {
	var in zoneBootstrapRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	id, err := a.store.CreateZoneBootstrap(r.Context(), in.ZoneID, in.Parameters)
	if err != nil {
		http.Error(w, err.Error(), status(err))
		return
	}
	writeJSON(w, map[string]string{"operation_id": id})
}

type setTargetRequest struct {
	NodeGroupID            string `json:"node_group_id"`
	RequestID              string `json:"request_id"`
	ExpectedTargetRevision int64  `json:"expected_target_revision"`
	DesiredTarget          int64  `json:"desired_target"`
	TemplateRevision       string `json:"template_revision"`
}

func (a *API) setTarget(w http.ResponseWriter, r *http.Request) {
	cluster := strings.TrimSpace(r.Header.Get("X-Crane-Cluster-ID"))
	if cluster == "" {
		http.Error(w, "missing cluster identity", http.StatusUnauthorized)
		return
	}

	var in setTargetRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	out, err := a.store.SetTarget(r.Context(), api.CapacityRequest{
		NodeGroupID:            in.NodeGroupID,
		RequestID:              in.RequestID,
		ClusterID:              cluster,
		ExpectedTargetRevision: in.ExpectedTargetRevision,
		DesiredTarget:          in.DesiredTarget,
		TemplateRevision:       in.TemplateRevision,
	})
	if err != nil {
		http.Error(w, err.Error(), status(err))
		return
	}
	writeJSON(w, out)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func status(err error) int {
	switch {
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrConflict), errors.Is(err, ErrReplayMismatch):
		return http.StatusConflict
	case errors.Is(err, ErrLimit), errors.Is(err, ErrInactive), errors.Is(err, ErrTemplate):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}
