package subd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

type fakeHost struct {
	applied           int
	fail, collectFail bool
}

type blockedHost struct{ started, release chan struct{} }

func (h blockedHost) Collect(context.Context) (api.CurrentState, error) {
	return api.CurrentState{}, nil
}
func (h blockedHost) Apply(ctx context.Context, _ api.ExpectedHostState) error {
	close(h.started)
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestHealthRemainsAvailableDuringEnforcement(t *testing.T) {
	h := blockedHost{started: make(chan struct{}), release: make(chan struct{})}
	s := testServer(t, h)
	done := make(chan int, 1)
	go func() {
		done <- request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1}`).Code
	}()
	<-h.started
	read := make(chan *httptest.ResponseRecorder, 1)
	go func() { read <- request(t, s, testConfig().ReaderIdentities[0], "GET", "/v1/health", "") }()
	select {
	case w := <-read:
		var status api.HostAgentStatus
		if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Outcome != "applying" || status.AppliedRevision != 0 {
			t.Error("health lost in-progress outcome")
		}
	case <-time.After(time.Second):
		t.Error("enforcement blocked health")
	}
	close(h.release)
	if code := <-done; code != 200 {
		t.Fatal(code)
	}
}

func TestPersistenceFailurePreventsApplication(t *testing.T) {
	h := &fakeHost{}
	s := testServer(t, h)
	if err := os.Mkdir(s.statePath, 0700); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1}`)
	if w.Code != 500 || h.applied != 0 || s.saved.Request.Revision != 0 {
		t.Fatal("unpersisted intent applied", w.Code, h.applied)
	}
}

func (h *fakeHost) Collect(context.Context) (api.CurrentState, error) {
	if h.collectFail {
		return api.CurrentState{}, errors.New("private collection detail")
	}
	return api.CurrentState{}, nil
}
func (h *fakeHost) Apply(context.Context, api.ExpectedHostState) error {
	h.applied++
	if h.fail {
		return errors.New("private application detail")
	}
	return nil
}

func testConfig() Config {
	return Config{ListenAddress: "127.0.0.1:44001", TrustDomain: "test.internal", WriterIdentity: "spiffe://test.internal/dominator", ReaderIdentities: []string{"spiffe://test.internal/probe"}}
}
func testServer(t *testing.T, h Host) *Server {
	t.Helper()
	dir := t.TempDir()
	s, err := NewServer(testConfig(), "spiffe://test.internal/subd/host-1", filepath.Join(dir, "state.json"), filepath.Join(dir, "status.json"), h)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func request(t *testing.T, s *Server, identity, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if identity != "" {
		u, err := url.Parse(identity)
		if err != nil {
			t.Fatal(err)
		}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAuthorizationAndValidation(t *testing.T) {
	s := testServer(t, &fakeHost{})
	for _, tc := range []struct {
		id, method, path, body string
		code                   int
	}{
		{"", "GET", "/v1/state", "", 401},
		{"spiffe://foreign.internal/dominator", "GET", "/v1/state", "", 403},
		{"spiffe://test.internal/subd/host-2", "GET", "/v1/state", "", 403},
		{testConfig().ReaderIdentities[0], "GET", "/v1/health", "", 200},
		{testConfig().ReaderIdentities[0], "PUT", "/v1/expected-state", `{"revision":1}`, 403},
		{testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":0}`, 400},
		{testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1,"HostID":"spoof"}`, 400},
		{testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1} {}`, 400},
		{testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1,"services":{"--bad.service":true}}`, 400},
		{testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1,"packages":[{"name":"$(secret)","action":"install"}]}`, 400},
		{testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1,"packages":[{"name":"curl","action":"purge"}]}`, 400},
	} {
		if w := request(t, s, tc.id, tc.method, tc.path, tc.body); w.Code != tc.code {
			t.Errorf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.code)
		}
	}
	if w := request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1,"services":{`+strings.Repeat(" ", 65536)+`}}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestUpdatePersistenceRetryAndRestart(t *testing.T) {
	h := &fakeHost{}
	s := testServer(t, h)
	put := func(body string, code int) {
		t.Helper()
		w := request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", body)
		if w.Code != code {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
	}
	put(`{"revision":2}`, 200)
	put(`{"revision":2}`, 200)
	if h.applied != 1 {
		t.Fatal("duplicate applied")
	}
	put(`{"revision":1}`, 409)
	put(`{"revision":2,"services":{"ssh.service":true}}`, 409)
	h.fail = true
	put(`{"revision":3}`, 500)
	if s.saved.AppliedRevision != 2 || s.saved.Outcome != "failed" {
		t.Fatal(s.saved)
	}
	h.fail = false
	put(`{"revision":3}`, 200)
	before := h.applied
	restarted, err := NewServer(testConfig(), s.identity, s.statePath, s.statusPath, h)
	if err != nil {
		t.Fatal(err)
	}
	if h.applied != before || restarted.saved.AppliedRevision != 3 {
		t.Fatal("restart initiated enforcement or lost revision")
	}
	if w := request(t, restarted, testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":3}`); w.Code != 200 || h.applied != before {
		t.Fatal("retry after restart applied")
	}
	info, err := os.Stat(s.statePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state permissions", err)
	}
	s.saved.Outcome = "applying"
	if err := atomicJSON(s.statePath, s.saved); err != nil {
		t.Fatal(err)
	}
	restarted, err = NewServer(testConfig(), s.identity, s.statePath, s.statusPath, h)
	if err != nil || restarted.saved.Outcome != "interrupted" || h.applied != before {
		t.Fatal("interrupted update must await resubmission", err)
	}
}

func TestConcurrentDuplicatesAndFailureRedaction(t *testing.T) {
	h := &fakeHost{}
	s := testServer(t, h)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":1}`); w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	wg.Wait()
	if h.applied != 1 {
		t.Fatal(h.applied)
	}
	h.collectFail = true
	w := request(t, s, testConfig().ReaderIdentities[0], "GET", "/v1/state", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("collection failure leaked or succeeded")
	}
	h.fail = true
	w = request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", `{"revision":2}`)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("application failure leaked or succeeded")
	}
}

func TestConfigAndIdentity(t *testing.T) {
	for _, edit := range []func(*Config){func(c *Config) { c.ListenAddress = "0.0.0.0:44001" }, func(c *Config) { c.ListenAddress = "1.1.1.1:44001" }, func(c *Config) { c.ListenAddress = "10.0.0.1:99999" }, func(c *Config) { c.TrustDomain = "bad/domain" }, func(c *Config) { c.WriterIdentity = "" }, func(c *Config) { c.ReaderIdentities = []string{"spiffe://foreign.internal/probe"} }} {
		c := testConfig()
		edit(&c)
		if c.Validate() == nil {
			t.Fatal(c)
		}
	}
	for _, id := range []string{"spiffe://test.internal/subd", "spiffe://foreign.internal/subd/h", "spiffe://test.internal/subd/h/extra"} {
		if validateOwnID(id, "test.internal") == nil {
			t.Fatal(id)
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(testConfig())
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"Token":"do-not-log"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || strings.Contains(err.Error(), "do-not-log") {
		t.Fatal(err)
	}
}

type staticSource struct {
	svid   *x509svid.SVID
	bundle *x509bundle.Bundle
}

func (s staticSource) GetX509SVID() (*x509svid.SVID, error) { return s.svid, nil }
func (s staticSource) GetX509BundleForTrustDomain(td spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	if td != s.bundle.TrustDomain() {
		return nil, errors.New("unknown domain")
	}
	return s.bundle, nil
}

func TestRealMutualTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	bundle := x509bundle.FromX509Authorities(spiffeid.RequireTrustDomainFromString("test.internal"), []*x509.Certificate{ca})
	issue := func(id string, serial int64) staticSource {
		t.Helper()
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(id)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), URIs: []*url.URL{u}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
		b, err := x509.CreateCertificate(rand.Reader, template, ca, &k.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(b)
		if err != nil {
			t.Fatal(err)
		}
		return staticSource{svid: &x509svid.SVID{ID: spiffeid.RequireFromString(id), Certificates: []*x509.Certificate{cert}, PrivateKey: k}, bundle: bundle}
	}
	s := testServer(t, &fakeHost{})
	own := issue(s.identity, 2)
	server := httptest.NewUnstartedServer(s.Handler())
	server.TLS = tlsconfig.MTLSServerConfig(own, own, tlsconfig.AuthorizeOneOf(spiffeid.RequireFromString(testConfig().WriterIdentity), spiffeid.RequireFromString(testConfig().ReaderIdentities[0])))
	server.TLS.Certificates = []tls.Certificate{{Certificate: [][]byte{own.svid.Certificates[0].Raw}, PrivateKey: own.svid.PrivateKey}}
	server.StartTLS()
	defer server.Close()
	for i, tc := range []struct {
		id, serverID string
		ok           bool
	}{{testConfig().WriterIdentity, s.identity, true}, {testConfig().ReaderIdentities[0], s.identity, true}, {"spiffe://test.internal/other", s.identity, false}, {"spiffe://foreign.internal/dominator", s.identity, false}, {testConfig().WriterIdentity, "spiffe://test.internal/subd/host-2", false}} {
		peer := issue(tc.id, int64(3+i))
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsconfig.MTLSClientConfig(peer, peer, tlsconfig.AuthorizeID(spiffeid.RequireFromString(tc.serverID)))}, Timeout: time.Second}
		resp, err := client.Get(server.URL + "/v1/health")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		client.CloseIdleConnections()
		if tc.ok && (err != nil || resp.StatusCode != 200) {
			t.Fatalf("authorized peer: %v", err)
		}
		if !tc.ok && err == nil {
			t.Fatal("unauthorized identity accepted", tc.id, tc.serverID)
		}
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsconfig.TLSClientConfig(own, tlsconfig.AuthorizeID(own.svid.ID))}, Timeout: time.Second}
	if resp, err := client.Get(server.URL + "/v1/health"); err == nil {
		resp.Body.Close()
		t.Fatal("missing client certificate accepted")
	}
}
