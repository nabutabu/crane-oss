package hostoperations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

type Controller struct {
	Store                            Store
	Caller                           hostrpc.Caller
	ZoneID, TrustDomain, OwnIdentity string
}

func (c Controller) Execute(ctx context.Context, host string, in api.SecretRequest) (result api.SecretResult, err error) {
	ctx, span := otel.Tracer("crane/hostoperations").Start(ctx, "ensure Kubernetes Secret")
	started := time.Now()
	result = api.SecretResult{HostID: host, RequestID: in.RequestID, Outcome: "unknown"}
	defer func() {
		attrs := []attribute.KeyValue{attribute.String("outcome", result.Outcome)}
		duration, _ := otel.Meter("crane/hostoperations").Float64Histogram("crane.host_operation.duration", metric.WithUnit("s"))
		count, _ := otel.Meter("crane/hostoperations").Int64Counter("crane.host_operation.count")
		duration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(attrs...))
		count.Add(ctx, 1, metric.WithAttributes(attrs...))
		span.SetAttributes(attrs...)
		if err != nil {
			span.SetStatus(codes.Error, "host operation failed")
		}
		slog.InfoContext(ctx, "host operation completed", "host_id", host, "zone_id", result.ZoneID, "revision", result.Revision, "outcome", result.Outcome)
		span.End()
	}()
	if err := in.Normalize(); err != nil {
		return result, fail(400, "invalid Secret request")
	}
	conn, release, err := c.Store.lock(ctx, host)
	if err != nil {
		return result, err
	}
	defer release()
	target, err := loadTarget(ctx, conn, host)
	if err != nil {
		return result, err
	}
	result.ZoneID = target.ZoneID
	if c.ZoneID == "" || target.ZoneID != c.ZoneID || target.DominatorIdentity != c.OwnIdentity {
		return result, fail(403, "host is not owned by this Dominator")
	}
	td, err := spiffeid.TrustDomainFromString(c.TrustDomain)
	if err != nil {
		return result, fail(503, "host identity unavailable")
	}
	peer, err := spiffeid.FromPath(td, "/subd/"+target.ProviderID)
	if err != nil {
		return result, fail(503, "host identity unavailable")
	}
	fingerprint := api.SecretFingerprint(in.KubernetesSecret)
	var storedFingerprint, outcome string
	var saved []byte
	err = conn.QueryRowContext(ctx, `SELECT fingerprint,revision,outcome,result FROM host_state_requests WHERE host_id=$1 AND request_id=$2`, host, in.RequestID).Scan(&storedFingerprint, &result.Revision, &outcome, &saved)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, fail(503, "operation store unavailable")
	}
	if exists {
		if storedFingerprint != fingerprint {
			return result, fail(409, "request ID already used with different contents")
		}
		if outcome == "applied" {
			if json.Unmarshal(saved, &result) != nil {
				return result, fail(503, "saved operation outcome unavailable")
			}
			return result, nil
		}
	}
	var status api.HostAgentStatus
	code, err := c.Caller.Call(ctx, target.Endpoint, peer.String(), http.MethodGet, "/v1/health", nil, &status)
	if err != nil || code != 200 || status.Identity != peer.String() || !status.Ready {
		return result, fail(502, "host state unavailable; completion may be unknown")
	}
	if exists && status.AcceptedRevision > result.Revision {
		return result, fail(409, "host operation has been superseded")
	}
	if !exists {
		var maximum uint64
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(max(revision),0) FROM host_state_requests WHERE host_id=$1`, host).Scan(&maximum); err != nil {
			return result, fail(503, "operation store unavailable")
		}
		if status.AcceptedRevision > maximum {
			maximum = status.AcceptedRevision
		}
		if maximum >= math.MaxInt64 {
			return result, fail(409, "host revision exhausted")
		}
		result.Revision = maximum + 1
		if _, err := conn.ExecContext(ctx, `INSERT INTO host_state_requests(host_id,request_id,fingerprint,revision) VALUES($1,$2,$3,$4)`, host, in.RequestID, fingerprint, result.Revision); err != nil {
			return result, fail(503, "cannot persist host operation")
		}
	}
	code, err = c.Caller.Call(ctx, target.Endpoint, peer.String(), http.MethodPut, "/v1/expected-state", api.ExpectedHostState{Revision: result.Revision, Secret: &in.KubernetesSecret}, &status)
	result.Outcome = "unknown"
	if err == nil && status.Identity == peer.String() && status.AcceptedRevision == result.Revision {
		result.Kubernetes = status.Kubernetes
		if status.Outcome == "failed" {
			result.Outcome = "failed"
		}
		if code == 200 && status.Outcome == "applied" && status.AppliedRevision == result.Revision && status.Kubernetes != nil && status.Kubernetes.Secret == "verified" &&
			((in.RolloutDeployment == "" && status.Kubernetes.Rollout == "not_requested") || (in.RolloutDeployment != "" && status.Kubernetes.Rollout == "ready")) {
			result.Outcome = "applied"
		}
	}
	if err == nil && code == 409 {
		return result, fail(409, "host revision conflict")
	}
	// Record bounded completion metadata even if the caller has disconnected.
	completion, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, _ := json.Marshal(result)
	tx, err := conn.BeginTx(completion, nil)
	if err != nil {
		return result, fail(503, "cannot persist host outcome; retry the same request ID")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(completion, `UPDATE host_state_requests SET outcome=$3,result=$4 WHERE host_id=$1 AND request_id=$2`, host, in.RequestID, result.Outcome, string(data)); err != nil {
		return result, fail(503, "cannot persist host outcome; retry the same request ID")
	}
	if in.Namespace == "crane-system" && in.Name == "otel-exporter-config" && in.RolloutDeployment == "otel-collector" {
		observability := "failed"
		if result.Outcome == "applied" {
			observability = "ready"
		}
		if _, err := tx.ExecContext(completion, `UPDATE zone_dominators SET observability_status=$2 WHERE zone_id=$1`, target.ZoneID, observability); err != nil {
			return result, fail(503, "cannot persist observability status")
		}
	}
	if err := tx.Commit(); err != nil {
		return result, fail(503, "cannot persist host outcome; retry the same request ID")
	}
	if result.Outcome != "applied" {
		if ctx.Err() != nil {
			return result, fail(504, "operation deadline exceeded; completion may be unknown")
		}
		return result, fail(502, "host operation did not complete; retry the same request ID")
	}
	return result, nil
}
