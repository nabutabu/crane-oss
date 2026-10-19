package subd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

type Server struct {
	cfg                   Config
	host                  Host
	identity              string
	statePath, statusPath string
	mu                    sync.Mutex
	view                  atomic.Value // api.HostAgentStatus; reads never wait for enforcement
	saved                 savedState
}

func NewServer(cfg Config, identity, statePath, statusPath string, host Host) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := validateOwnID(identity, cfg.TrustDomain); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, identity: identity, host: host, statePath: statePath, statusPath: statusPath}
	data, err := os.ReadFile(statePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot read persisted Subd state")
	}
	if err == nil {
		if json.Unmarshal(data, &s.saved) != nil || validateExpected(s.saved.Request) != nil || digest(s.saved.Request) != s.saved.Digest || s.saved.AppliedRevision > s.saved.Request.Revision {
			return nil, errors.New("invalid persisted Subd state")
		}
		switch s.saved.Outcome {
		case "applying":
			s.saved.Outcome = "interrupted"
		case "applied", "failed", "interrupted":
		default:
			return nil, errors.New("invalid persisted Subd outcome")
		}
	} else {
		s.saved.Outcome = "unconfigured"
	}
	s.view.Store(s.status())
	return s, nil
}

func digest(in api.ExpectedHostState) string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Server) status() api.HostAgentStatus {
	return api.HostAgentStatus{Identity: s.identity, PID: os.Getpid(), InvocationID: os.Getenv("INVOCATION_ID"), Ready: true, AcceptedRevision: s.saved.Request.Revision, AppliedRevision: s.saved.AppliedRevision, Outcome: s.saved.Outcome}
}

func (s *Server) publish() error {
	status := s.status()
	s.view.Store(status)
	return atomicJSON(s.statusPath, status)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/state", s.current)
	mux.HandleFunc("PUT /v1/expected-state", s.update)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// go-spiffe verifies chains in VerifyPeerCertificate, so VerifiedChains
		// is intentionally empty. Serve always installs that verification hook.
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client identity required", http.StatusUnauthorized)
			return
		}
		id, err := x509svid.IDFromCert(r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, "invalid client identity", http.StatusUnauthorized)
			return
		}
		allowed := id.String() == s.cfg.WriterIdentity
		if r.Method == http.MethodGet {
			for _, reader := range s.cfg.ReaderIdentities {
				allowed = allowed || id.String() == reader
			}
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.view.Load().(api.HostAgentStatus))
}

func (s *Server) current(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	state, err := s.host.Collect(ctx)
	if err != nil {
		http.Error(w, "host collection failed", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, api.HostAgentState{HostAgentStatus: s.view.Load().(api.HostAgentStatus), Current: state})
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	var in api.ExpectedHostState
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || validateExpected(in) != nil {
		http.Error(w, "invalid expected state", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Context().Err() != nil {
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
		return
	}
	hash := digest(in)
	if in.Revision < s.saved.Request.Revision || (in.Revision == s.saved.Request.Revision && hash != s.saved.Digest) {
		http.Error(w, "revision conflict", http.StatusConflict)
		return
	}
	if in.Revision == s.saved.Request.Revision && s.saved.Outcome == "applied" {
		writeJSON(w, s.status())
		return
	}
	previous := s.saved
	s.saved = savedState{Request: in, Digest: hash, AppliedRevision: previous.AppliedRevision, Outcome: "applying"}
	if err := atomicJSON(s.statePath, s.saved); err != nil {
		s.saved = previous
		http.Error(w, "cannot persist update", http.StatusInternalServerError)
		return
	}
	if err := s.publish(); err != nil {
		http.Error(w, "cannot publish status", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	err := s.host.Apply(ctx, in)
	s.saved.Outcome = "failed"
	if err == nil {
		s.saved.Outcome = "applied"
		s.saved.AppliedRevision = in.Revision
	}
	if persistErr := atomicJSON(s.statePath, s.saved); persistErr != nil {
		s.saved.Outcome = "interrupted"
		s.saved.AppliedRevision = previous.AppliedRevision
		_ = s.publish()
		http.Error(w, "cannot persist outcome", http.StatusInternalServerError)
		return
	}
	if publishErr := s.publish(); publishErr != nil {
		http.Error(w, "cannot publish status", http.StatusInternalServerError)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "host update failed", "revision", in.Revision)
		http.Error(w, "host update failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.status())
}

type IdentitySource interface {
	x509svid.Source
	x509bundle.Source
}

func (s *Server) Serve(ctx context.Context, source IdentitySource) error {
	ids := []spiffeid.ID{spiffeid.RequireFromString(s.cfg.WriterIdentity)}
	for _, raw := range s.cfg.ReaderIdentities {
		ids = append(ids, spiffeid.RequireFromString(raw))
	}
	conf := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeOneOf(ids...))
	server := &http.Server{Addr: s.cfg.ListenAddress, Handler: s.Handler(), TLSConfig: conf, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 6 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		return errors.New("cannot bind Subd listener")
	}
	defer listener.Close()
	if err := s.publish(); err != nil {
		return errors.New("cannot publish Subd readiness")
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	err = server.ServeTLS(listener, "", "")
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
