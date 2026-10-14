package capacity

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// API exposes the small platform control-plane surface. Topology mutations
// require the configured admin bearer token; capacity mutations derive cluster
// identity from X-Crane-Cluster-ID and never accept a team from the request.
func API(dbStore *Store, adminToken string, adapterTokens ...string) http.Handler {
	mux := http.NewServeMux()
	admin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if adminToken == "" || r.Header.Get("Authorization") != "Bearer "+adminToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	adapterToken := ""
	if len(adapterTokens) > 0 {
		adapterToken = adapterTokens[0]
	}
	adapter := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if adapterToken == "" || r.Header.Get("Authorization") != "Bearer "+adapterToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	decode := func(w http.ResponseWriter, r *http.Request, dst any) bool {
		defer r.Body.Close()
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dst); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return false
		}
		return true
	}
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(value)
	}
	mux.Handle("POST /v1/zones", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in api.ZoneInput
		if decode(w, r, &in) {
			if err := dbStore.CreateZone(r.Context(), in); err != nil {
				http.Error(w, err.Error(), status(err))
				return
			}
			write(w, in)
		}
	})))
	mux.Handle("POST /v1/clusters", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in api.ClusterInput
		if decode(w, r, &in) {
			if err := dbStore.CreateCluster(r.Context(), in); err != nil {
				http.Error(w, err.Error(), status(err))
				return
			}
			write(w, in)
		}
	})))
	mux.Handle("POST /v1/node-pools", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in api.NodePoolInput
		if decode(w, r, &in) {
			if err := dbStore.CreateNodePool(r.Context(), in); err != nil {
				http.Error(w, err.Error(), status(err))
				return
			}
			write(w, in)
		}
	})))
	mux.Handle("POST /v1/node-groups", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in api.NodeGroupInput
		if decode(w, r, &in) {
			if err := dbStore.CreateNodeGroup(r.Context(), in); err != nil {
				http.Error(w, err.Error(), status(err))
				return
			}
			write(w, in)
		}
	})))
	mux.Handle("POST /v1/first-zone", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Zone    api.ZoneInput      `json:"zone"`
			Cluster api.ClusterInput   `json:"cluster"`
			Budget  api.Resources      `json:"budget"`
			Pool    api.NodePoolInput  `json:"pool"`
			Group   api.NodeGroupInput `json:"group"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := dbStore.SeedFirstZone(r.Context(), in.Zone, in.Cluster, in.Budget, in.Pool, in.Group); err != nil {
			http.Error(w, err.Error(), status(err))
			return
		}
		write(w, map[string]string{"status": "ready"})
	})))
	mux.Handle("POST /v1/bootstrap", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ZoneID     string          `json:"zone_id"`
			Parameters json.RawMessage `json:"parameters"`
		}
		if !decode(w, r, &in) {
			return
		}
		id, err := dbStore.CreateZoneBootstrap(r.Context(), in.ZoneID, in.Parameters)
		if err != nil {
			http.Error(w, err.Error(), status(err))
			return
		}
		write(w, map[string]string{"operation_id": id})
	})))
	mux.Handle("POST /v1/capacity", adapter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := strings.TrimSpace(r.Header.Get("X-Crane-Cluster-ID"))
		if cluster == "" {
			http.Error(w, "missing cluster identity", http.StatusUnauthorized)
			return
		}
		var in struct {
			NodeGroupID            string `json:"node_group_id"`
			RequestID              string `json:"request_id"`
			ExpectedTargetRevision int64  `json:"expected_target_revision"`
			DesiredTarget          int64  `json:"desired_target"`
			TemplateRevision       string `json:"template_revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := dbStore.SetTarget(r.Context(), api.CapacityRequest{NodeGroupID: in.NodeGroupID, RequestID: in.RequestID, ClusterID: cluster, ExpectedTargetRevision: in.ExpectedTargetRevision, DesiredTarget: in.DesiredTarget, TemplateRevision: in.TemplateRevision})
		if err != nil {
			http.Error(w, err.Error(), status(err))
			return
		}
		write(w, out)
	})))
	return mux
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
