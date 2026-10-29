package hostoperations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

const ownID = "spiffe://test.internal/dominator/zone"
const apiID = "spiffe://test.internal/crane-api"
const hostID = "deployment/bootstrap/platform"

func operationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("CRANE_TEST_DATABASE_URL") == "" {
		t.Skip("dedicated PostgreSQL fixture required")
	}
	cfg, err := pgx.ParseConfig(os.Getenv("CRANE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	schema := "hostops_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() {
		db.Close()
		_, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		if err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	for i := 0; i < 2; i++ {
		if err := capacity.InitializeSchema(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	if err := capacity.CheckSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO zones(id,provider,account_id,region,provider_zone_id) VALUES('zone','provider','account','region','az');
INSERT INTO host(id,zone,state,providerid,subd_endpoint,protected) VALUES('deployment/bootstrap/platform','zone','READY','provider-host','https://127.0.0.1:44001',true);
INSERT INTO zone_dominators(zone_id,endpoint,identity) VALUES('zone','https://127.0.0.1:44000','spiffe://test.internal/dominator/zone')`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type agentCaller struct {
	mu                                       sync.Mutex
	status                                   api.HostAgentStatus
	applies, deliveries                      int
	failRollout, loseResponse, wrongIdentity bool
	started                                  chan struct{}
	release                                  chan struct{}
}

func (a *agentCaller) Call(ctx context.Context, endpoint, identity, method, path string, in, out any) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if identity != "spiffe://test.internal/subd/provider-host" || endpoint != "https://127.0.0.1:44001" {
		return 0, errors.New("incorrect destination")
	}
	a.status.Identity = identity
	a.status.Ready = true
	if method == "PUT" {
		if a.started != nil {
			close(a.started)
			a.started = nil
			select {
			case <-a.release:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		a.deliveries++
		expected := in.(api.ExpectedHostState)
		if a.status.AppliedRevision != expected.Revision {
			a.applies++
			a.status.AcceptedRevision = expected.Revision
			a.status.Kubernetes = &api.KubernetesResult{Secret: "verified", Rollout: "ready"}
			a.status.Outcome = "applied"
			a.status.AppliedRevision = expected.Revision
			if a.failRollout {
				a.status.Outcome = "failed"
				a.status.AppliedRevision = 0
				a.status.Kubernetes.Rollout = "failed"
			}
		}
		if a.loseResponse {
			a.loseResponse = false
			return 0, errors.New("response lost")
		}
	}
	status := a.status
	if a.wrongIdentity {
		status.Identity = "spiffe://test.internal/subd/another-host"
	}
	*out.(*api.HostAgentStatus) = status
	if status.Outcome == "failed" && method == "PUT" {
		return 500, nil
	}
	return 200, nil
}

func secretRequest(id string) api.SecretRequest {
	return api.SecretRequest{RequestID: id, KubernetesSecret: api.KubernetesSecret{Namespace: "crane-system", Name: "otel-exporter-config", StringData: map[string]string{"exporter.yaml": "synthetic-private-value"}, RolloutDeployment: "otel-collector"}}
}
func controller(db *sql.DB, caller *agentCaller) Controller {
	return Controller{Store: Store{DB: db}, Caller: caller, ZoneID: "zone", TrustDomain: "test.internal", OwnIdentity: ownID}
}

func TestDurableLostResponseReplayConflictAndSupersede(t *testing.T) {
	db := operationDatabase(t)
	agent := &agentCaller{loseResponse: true, status: api.HostAgentStatus{AcceptedRevision: 1}}
	c := controller(db, agent)
	ctx := context.Background()
	first, err := c.Execute(ctx, hostID, secretRequest("first"))
	if err == nil || first.Revision != 2 || first.Outcome != "unknown" {
		t.Fatal(first, err)
	}
	// A fresh controller instance recovers through the saved revision and Subd.
	c = controller(db, agent)
	recovered, err := c.Execute(ctx, hostID, secretRequest("first"))
	if err != nil || recovered.Outcome != "applied" || agent.applies != 1 {
		t.Fatal(recovered, err)
	}
	before := agent.deliveries
	if _, err := c.Execute(ctx, hostID, secretRequest("first")); err != nil || agent.deliveries != before {
		t.Fatal("successful replay delivered again")
	}
	changed := secretRequest("first")
	changed.StringData["exporter.yaml"] = "different"
	if _, err := c.Execute(ctx, hostID, changed); errorStatus(err) != 409 {
		t.Fatal("conflicting payload accepted", err)
	}
	agent.failRollout = true
	if _, err := c.Execute(ctx, hostID, secretRequest("failed")); err == nil {
		t.Fatal("failed rollout succeeded")
	}
	agent.failRollout = false
	if _, err := c.Execute(ctx, hostID, secretRequest("newer")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(ctx, hostID, secretRequest("failed")); errorStatus(err) != 409 {
		t.Fatal("superseded request applied", err)
	}
	var stored, status string
	if err := db.QueryRow(`SELECT string_agg(row_to_json(r)::text,'') FROM host_state_requests r`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "synthetic-private-value") {
		t.Fatal("Secret persisted centrally")
	}
	if err := db.QueryRow(`SELECT observability_status FROM zone_dominators WHERE zone_id='zone'`).Scan(&status); err != nil || status != "ready" {
		t.Fatal(status, err)
	}
}

func TestReplicaSerializationAndCancelledWait(t *testing.T) {
	db := operationDatabase(t)
	started, release := make(chan struct{}), make(chan struct{})
	agent := &agentCaller{started: started, release: release}
	c := controller(db, agent)
	done := make(chan error, 1)
	go func() { _, err := c.Execute(context.Background(), hostID, secretRequest("one")); done <- err }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := controller(db, agent).Execute(ctx, hostID, secretRequest("two")); errorStatus(err) != 504 {
		t.Fatal("waiting replica ignored cancellation", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result, err := controller(db, agent).Execute(context.Background(), hostID, secretRequest("two")); err != nil || result.Revision != 2 {
		t.Fatal(result, err)
	}
}

func TestZoneAndHostIdentityAuthorization(t *testing.T) {
	db := operationDatabase(t)
	agent := &agentCaller{}
	c := controller(db, agent)
	c.ZoneID = "other-zone"
	if _, err := c.Execute(context.Background(), hostID, secretRequest("id")); errorStatus(err) != 403 {
		t.Fatal(err)
	}
	c = controller(db, agent)
	agent.wrongIdentity = true
	if _, err := c.Execute(context.Background(), hostID, secretRequest("id")); errorStatus(err) != 502 {
		t.Fatal(err)
	}
	if agent.deliveries != 0 {
		t.Fatal("delivered to wrong host")
	}
}

type resolverFunc func(context.Context, string) (Target, error)

func (f resolverFunc) Target(ctx context.Context, id string) (Target, error) { return f(ctx, id) }

type callerFunc func(context.Context, string, string, string, string, any, any) (int, error)

func (f callerFunc) Call(ctx context.Context, e, i, m, p string, in, out any) (int, error) {
	return f(ctx, e, i, m, p, in, out)
}

func TestPublicRouteEscapedCatalogIDAndAuthentication(t *testing.T) {
	a := API{AdminToken: "synthetic-admin", Store: resolverFunc(func(_ context.Context, id string) (Target, error) {
		if id != hostID {
			t.Fatal(id)
		}
		return Target{HostID: id, ZoneID: "zone", DominatorIdentity: ownID}, nil
	}), Caller: callerFunc(func(_ context.Context, _, _, _, _ string, in, out any) (int, error) {
		request := in.(delivery)
		*out.(*api.SecretResult) = api.SecretResult{HostID: request.HostID, ZoneID: "zone", RequestID: request.Request.RequestID, Outcome: "applied"}
		return 200, nil
	})}
	mux := http.NewServeMux()
	mux.Handle(PublicPattern, a)
	for _, authorized := range []bool{false, true} {
		r := httptest.NewRequest("PUT", "/v1/hosts/"+url.PathEscape(hostID)+"/kubernetes/namespaces/default/secrets/config", strings.NewReader(`{"request_id":"id","stringData":{"key":"synthetic"}}`))
		if authorized {
			r.Header.Set("Authorization", "Bearer synthetic-admin")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if authorized && w.Code != 200 || !authorized && w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestDominatorProbeCannotMutate(t *testing.T) {
	for _, identity := range []string{"", "spiffe://test.internal/subd"} {
		r := httptest.NewRequest("PUT", InternalPath, strings.NewReader(`{}`))
		if identity != "" {
			u, _ := url.Parse(identity)
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
		}
		w := httptest.NewRecorder()
		(Controller{}).Handler(apiID).ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}

func TestBlockingAPIToDominatorToSubd(t *testing.T) {
	db := operationDatabase(t)
	started, release := make(chan struct{}), make(chan struct{})
	agent := &agentCaller{started: started, release: release}
	dominator := controller(db, agent).Handler(apiID)
	proxy := callerFunc(func(ctx context.Context, endpoint, identity, method, path string, in, out any) (int, error) {
		if endpoint != "https://127.0.0.1:44000" || identity != ownID || path != InternalPath {
			t.Fatal("wrong Dominator destination")
		}
		body, _ := json.Marshal(in)
		r := httptest.NewRequest(method, path, strings.NewReader(string(body))).WithContext(ctx)
		u, _ := url.Parse(apiID)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
		w := httptest.NewRecorder()
		dominator.ServeHTTP(w, r)
		return w.Code, json.Unmarshal(w.Body.Bytes(), out)
	})
	mux := http.NewServeMux()
	mux.Handle(PublicPattern, API{Store: Store{DB: db}, Caller: proxy, AdminToken: "synthetic-admin"})
	r := httptest.NewRequest("PUT", "/v1/hosts/"+url.PathEscape(hostID)+"/kubernetes/namespaces/crane-system/secrets/otel-exporter-config", strings.NewReader(`{"request_id":"id","stringData":{"exporter.yaml":"synthetic-private-value"},"rollout_deployment":"otel-collector"}`))
	r.Header.Set("Authorization", "Bearer synthetic-admin")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, r); done <- w }()
	<-started
	select {
	case <-done:
		t.Fatal("API returned before Subd completed")
	default:
	}
	close(release)
	w := <-done
	var result api.SecretResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Kubernetes.Rollout != "ready" || strings.Contains(w.Body.String(), "synthetic-private-value") {
		t.Fatal(w.Code, w.Body.String())
	}
}
