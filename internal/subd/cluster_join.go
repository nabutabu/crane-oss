package subd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/pkg/api"
)

var (
	joinTokenRE     = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)
	joinCAHashRE    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	joinOperationRE = regexp.MustCompile(`^[a-f0-9-]{36}$`)
)

type clusterJoinState struct {
	OperationID string `json:"operation_id"`
	Endpoint    string `json:"api_server_endpoint"`
	CACertHash  string `json:"ca_cert_hash"`
	Outcome     string `json:"outcome"`
}

func validateJoinState(s clusterJoinState) error {
	if !joinOperationRE.MatchString(s.OperationID) || s.Endpoint == "" || !joinCAHashRE.MatchString(s.CACertHash) {
		return errors.New("invalid join state")
	}
	switch s.Outcome {
	case "applying", "applied", "failed", "interrupted":
		return nil
	default:
		return errors.New("invalid join outcome")
	}
}

func validateClusterJoin(in api.ClusterJoinRequest) error {
	if !joinOperationRE.MatchString(in.OperationID) || !joinTokenRE.MatchString(in.Token) || !joinCAHashRE.MatchString(in.CACertHash) {
		return errors.New("invalid cluster join request")
	}
	u, err := url.Parse(in.APIServerEndpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid cluster join endpoint")
	}
	return nil
}

func (s *Server) clusterJoin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, err := hostrpc.Context(r)
	if err != nil {
		http.Error(w, "invalid operation deadline", 400)
		return
	}
	defer cancel()
	var in api.ClusterJoinRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || validateClusterJoin(in) != nil {
		http.Error(w, "invalid cluster join request", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.join.OperationID != "" {
		if s.join.OperationID != in.OperationID || s.join.Endpoint != in.APIServerEndpoint || s.join.CACertHash != in.CACertHash {
			http.Error(w, "cluster join operation conflict", 409)
			return
		}
		if s.join.Outcome == "applied" {
			hostrpc.JSON(w, 200, api.ClusterJoinResult{OperationID: in.OperationID, Outcome: "applied"})
			return
		}
	} else {
		s.join = clusterJoinState{OperationID: in.OperationID, Endpoint: in.APIServerEndpoint, CACertHash: in.CACertHash, Outcome: "applying"}
	}
	if _, ok := s.host.(clusterJoinHost); !ok {
		http.Error(w, "Kubernetes join is unavailable", 503)
		return
	}
	s.join.Outcome = "applying"
	if err := atomicJSON(s.joinPath, s.join); err != nil {
		http.Error(w, "cannot persist cluster join progress", 503)
		return
	}
	if err := s.host.(clusterJoinHost).JoinCluster(ctx, in); err != nil {
		s.join.Outcome = "failed"
		_ = atomicJSON(s.joinPath, s.join)
		http.Error(w, "Kubernetes node join failed", 502)
		return
	}
	s.join.Outcome = "applied"
	if err := atomicJSON(s.joinPath, s.join); err != nil {
		s.join.Outcome = "interrupted"
		http.Error(w, "cannot persist cluster join outcome", 503)
		return
	}
	hostrpc.JSON(w, 200, api.ClusterJoinResult{OperationID: in.OperationID, Outcome: "applied"})
}
