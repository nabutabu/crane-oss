package hostoperations

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/pkg/api"
)

const PublicPattern = "PUT /v1/hosts/{hostID}/kubernetes/namespaces/{namespace}/secrets/{name}"
const InternalPath = "/v1/host-secrets"

type targetResolver interface {
	Target(context.Context, string) (Target, error)
}
type API struct {
	Store      targetResolver
	Caller     hostrpc.Caller
	AdminToken string
}

type delivery struct {
	HostID  string            `json:"host_id"`
	Request api.SecretRequest `json:"request"`
}

func (a API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.AdminToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.AdminToken)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	ctx, cancel, err := hostrpc.Context(r)
	if err != nil {
		http.Error(w, "invalid operation deadline", 400)
		return
	}
	defer cancel()
	var in api.SecretRequest
	if !hostrpc.Decode(w, r, &in) {
		return
	}
	if (in.Namespace != "" && in.Namespace != r.PathValue("namespace")) || (in.Name != "" && in.Name != r.PathValue("name")) {
		http.Error(w, "resource identity conflicts with path", 400)
		return
	}
	in.Namespace, in.Name = r.PathValue("namespace"), r.PathValue("name")
	if err := in.Normalize(); err != nil {
		http.Error(w, "invalid Secret request", 400)
		return
	}
	target, err := a.Store.Target(ctx, r.PathValue("hostID"))
	if err != nil {
		http.Error(w, err.Error(), errorStatus(err))
		return
	}
	var result api.SecretResult
	code, err := a.Caller.Call(ctx, target.DominatorEndpoint, target.DominatorIdentity, http.MethodPut, InternalPath, delivery{target.HostID, in}, &result)
	if err != nil || result.HostID != target.HostID || result.ZoneID != target.ZoneID || result.RequestID != in.RequestID {
		if ctx.Err() != nil {
			http.Error(w, "operation deadline exceeded; retry the same request ID", 504)
			return
		}
		http.Error(w, "Dominator response unavailable; retry the same request ID", http.StatusBadGateway)
		return
	}
	if code != 200 && code != 400 && code != 403 && code != 404 && code != 409 && code != 502 && code != 503 && code != 504 {
		code = 502
	}
	hostrpc.JSON(w, code, result)
}

// Handler's route-level identity check keeps the read-only bootstrap probe
// distinct from the Crane API workload admitted by the TLS listener.
func (c Controller) Handler(apiIdentity string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hostrpc.Peer(r) != apiIdentity || apiIdentity == "" {
			http.Error(w, "forbidden", 403)
			return
		}
		ctx, cancel, err := hostrpc.Context(r)
		if err != nil {
			http.Error(w, "invalid operation deadline", 400)
			return
		}
		defer cancel()
		var in delivery
		if !hostrpc.Decode(w, r, &in) {
			return
		}
		if in.HostID == "" {
			http.Error(w, "host required", 400)
			return
		}
		result, err := c.Execute(ctx, in.HostID, in.Request)
		code := 200
		if err != nil {
			code = errorStatus(err)
			result.Error = err.Error()
		}
		hostrpc.JSON(w, code, result)
	})
}
