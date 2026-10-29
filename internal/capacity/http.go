package capacity

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const maxBodyBytes = 1 << 20

type adapterClusterContextKey struct{}

func authenticatedAdapterCluster(ctx context.Context) string {
	clusterID, _ := ctx.Value(adapterClusterContextKey{}).(string)
	return clusterID
}

// API exposes the small platform control-plane surface. Inventory reads and
// topology mutations require the admin bearer token. Each adapter credential
// is bound to one configured cluster; the header must match that binding.
type API struct {
	store             *Store
	adminToken        string
	adapterPrincipals map[string]string
}

// NewAPI builds the platform API with the legacy single-cluster credential.
// Unconfigured or incomplete adapter credentials fail closed.
func NewAPI(store *Store, adminToken, adapterToken, adapterClusterID string) *API {
	principals := map[string]string{}
	if adapterToken != "" && adapterClusterID != "" {
		principals[adapterClusterID] = adapterToken
	}
	return NewAPIWithAdapterPrincipals(store, adminToken, principals)
}

// NewAPIWithAdapterPrincipals binds each configured bearer credential to one
// cluster. Cluster IDs in requests select the already authenticated principal;
// they never grant access by themselves.
func NewAPIWithAdapterPrincipals(store *Store, adminToken string, principals map[string]string) *API {
	configured := make(map[string]string, len(principals))
	for clusterID, token := range principals {
		configured[clusterID] = token
	}
	return &API{store: store, adminToken: adminToken, adapterPrincipals: configured}
}

// ParseAdapterPrincipals parses a JSON object whose keys are cluster IDs and
// values are bearer credentials. Empty, ambiguous, and shared credentials are
// rejected so one credential always represents one cluster principal.
func ParseAdapterPrincipals(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	var principals map[string]string
	if err := json.Unmarshal([]byte(raw), &principals); err != nil || principals == nil {
		return nil, errors.New("CRANE_ADAPTER_PRINCIPALS must be a JSON object")
	}
	seen := make(map[string]struct{}, len(principals))
	for clusterID, token := range principals {
		if clusterID == "" || clusterID != strings.TrimSpace(clusterID) || token == "" || token != strings.TrimSpace(token) {
			return nil, errors.New("adapter principal cluster IDs and credentials must be non-empty and canonical")
		}
		if _, exists := seen[token]; exists {
			return nil, errors.New("adapter credential is assigned to multiple clusters")
		}
		seen[token] = struct{}{}
	}
	return principals, nil
}

// Handler returns the router that maps each platform endpoint to its handler.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /v1/hosts", a.admin(a.listHosts))
	mux.Handle("POST /v1/zones", a.admin(a.createZone))
	mux.Handle("POST /v1/clusters", a.admin(a.createCluster))
	mux.Handle("POST /v1/cluster-zones", a.admin(a.createClusterZone))
	mux.Handle("PATCH /v1/cluster-zones/{cluster_id}/{zone_id}", a.admin(a.updateClusterZone))
	mux.Handle("POST /v1/node-pools", a.admin(a.createNodePool))
	mux.Handle("POST /v1/node-groups", a.admin(a.createNodeGroup))
	mux.Handle("PATCH /v1/node-groups/{id}/template", a.admin(a.updateNodeGroupTemplate))
	mux.Handle("POST /v1/first-zone", a.admin(a.seedFirstZone))
	mux.Handle("POST /v1/bootstrap", a.admin(a.createZoneBootstrap))
	mux.Handle("POST /v1/capacity", a.adapter(a.setTarget))
	mux.Handle("GET /v1/node-groups/{id}/target", a.adapter(a.target))
	mux.Handle("GET /v1/autoscaler/discovery", a.adapter(a.discovery))
	mux.Handle("POST /v1/node-removals", a.adapter(a.removeNodes))
	return mux
}

// admin guards platform administration behind the configured admin bearer token.
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
		presented := ""
		if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
			presented = strings.TrimPrefix(authorization, "Bearer ")
		}
		authenticatedCluster := ""
		for clusterID, token := range a.adapterPrincipals {
			if presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
				authenticatedCluster = clusterID
			}
		}
		if authenticatedCluster == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-Crane-Cluster-ID")) != authenticatedCluster {
			http.Error(w, "forbidden cluster identity", http.StatusForbidden)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), adapterClusterContextKey{}, authenticatedCluster))
		// Preserve an entrypoint's active server span. Standalone handlers still
		// accept incoming propagation when no server instrumentation is present.
		if !trace.SpanContextFromContext(r.Context()).IsValid() {
			r = r.WithContext(otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header)))
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
		capacityError(w, err)
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
		capacityError(w, err)
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
		capacityError(w, err)
		return
	}
	writeJSON(w, in)
}

func (a *API) createNodeGroup(w http.ResponseWriter, r *http.Request) {
	var in api.NodeGroupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.NormalizeID() != nil {
		capacityError(w, ErrInvalid)
		return
	}
	if err := a.store.CreateNodeGroup(r.Context(), in); err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, in)
}

func (a *API) updateNodeGroupTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedTemplateRevision string                 `json:"expected_template_revision"`
		TemplateRevision         string                 `json:"template_revision"`
		Scheduling               api.SchedulingTemplate `json:"scheduling"`
		LaunchParameters         json.RawMessage        `json:"launch_parameters"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.UpdateNodeGroupScheduling(r.Context(), r.PathValue("id"), in.ExpectedTemplateRevision, in.TemplateRevision, in.Scheduling, in.LaunchParameters); err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (a *API) seedFirstZone(w http.ResponseWriter, r *http.Request) {
	var in api.FirstZoneInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.SeedFirstZone(r.Context(), in); err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ready"})
}

func (a *API) createClusterZone(w http.ResponseWriter, r *http.Request) {
	var in api.ClusterZoneInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := normalizeMembership(&in); err != nil {
		capacityError(w, err)
		return
	}
	if err := a.store.CreateClusterZone(r.Context(), in); err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, in)
}

func (a *API) updateClusterZone(w http.ResponseWriter, r *http.Request) {
	var state struct {
		LifecycleState string `json:"lifecycle_state"`
		AccessState    string `json:"access_state"`
	}
	if !decodeJSON(w, r, &state) {
		return
	}
	// Both values are required: a partial patch must never implicitly revoke or approve access.
	if state.LifecycleState == "" || state.AccessState == "" {
		capacityError(w, ErrInvalid)
		return
	}
	in := api.ClusterZoneInput{ClusterID: r.PathValue("cluster_id"), ZoneID: r.PathValue("zone_id"), LifecycleState: state.LifecycleState, AccessState: state.AccessState}
	if err := a.store.UpdateClusterZone(r.Context(), in); err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, in)
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
		capacityError(w, err)
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
	cluster := authenticatedAdapterCluster(r.Context())
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
		capacityError(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) target(w http.ResponseWriter, r *http.Request) {
	out, err := a.store.Target(r.Context(), authenticatedAdapterCluster(r.Context()), r.PathValue("id"))
	if err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) discovery(w http.ResponseWriter, r *http.Request) {
	out, err := a.store.AutoscalerDiscovery(r.Context(), authenticatedAdapterCluster(r.Context()))
	if err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) removeNodes(w http.ResponseWriter, r *http.Request) {
	var in api.NodeRemovalRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := a.store.CreateAutoscalerRemovals(r.Context(), authenticatedAdapterCluster(r.Context()), in); err != nil {
		capacityError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "accepted"})
}

func capacityError(w http.ResponseWriter, err error) {
	code, httpStatus := "internal", http.StatusInternalServerError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		code, httpStatus = "not_found", http.StatusNotFound
	case errors.Is(err, ErrForbidden):
		code, httpStatus = "forbidden", http.StatusForbidden
	case errors.Is(err, ErrConflict), errors.Is(err, ErrReplayMismatch):
		code, httpStatus = "conflict", http.StatusConflict
	case errors.Is(err, ErrLimit):
		code, httpStatus = "capacity_limit", http.StatusUnprocessableEntity
	case errors.Is(err, ErrInactive), errors.Is(err, ErrTemplate), errors.Is(err, ErrFulfilled):
		code, httpStatus = "precondition", http.StatusUnprocessableEntity
	case errors.Is(err, ErrInvalid):
		code, httpStatus = "invalid", http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
