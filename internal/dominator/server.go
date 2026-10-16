package dominator

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nabutabu/crane-oss/internal/config"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

type Server struct {
	addr     string
	db       *sql.DB
	catalog  *service.HostCatalogService
	resolver *PolicyResolver
	logger   *slog.Logger
}

func NewServer(addr string, db *sql.DB, catalog *service.HostCatalogService, resolver *PolicyResolver, loggers ...*slog.Logger) *Server {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &Server{
		addr:     addr,
		db:       db,
		catalog:  catalog,
		resolver: resolver,
		logger:   logger,
	}
}

func (s *Server) Start(source *workloadapi.X509Source) error {
	return s.StartContext(context.Background(), source)
}

func (s *Server) StartContext(ctx context.Context, source *workloadapi.X509Source) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/heartbeat", s.handleState)
	mux.HandleFunc("/v1/health", s.Health)
	mux.HandleFunc("/v1/db/health", s.dbHealth)

	s.logger.Info("dominator listening", "addr", s.addr)

	allowedID, err := spiffeid.FromString("spiffe://" + config.GetEnv("SPIFFE_TRUST_DOMAIN", "crane-oss") + "/subd")
	if err != nil {
		return fmt.Errorf("invalid SPIFFE trust domain: %w", err)
	}

	tlsConfig := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeOneOf(allowedID))
	server := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := tls.Listen("tcp", s.addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}

	defer ln.Close()
	done := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				s.logger.Error("HTTP shutdown failed", "error", err)
			}
		case <-done:
		}
	}()
	err = server.Serve(ln)
	close(done)
	<-shutdownDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) dbHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		s.logger.Error("database health check failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status": "unavailable"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status": "available"}`)
}

func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("health probe", "path", r.URL.Path)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Dominator listening on %s\n", s.addr)
}

func (s *Server) recordState(ctx context.Context, host *api.Host) error {
	if err := s.catalog.TransitionHealth(ctx, host.ID, string(api.HostHealthHealthy)); err != nil {
		return err
	}
	return s.catalog.UpdateLastSeenHeartbeat(ctx, host.ID, time.Now())
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "no client cert", http.StatusUnauthorized)
		return
	}

	cert := r.TLS.PeerCertificates[0]
	if len(cert.URIs) == 0 {
		http.Error(w, "missing SPIFFE ID", http.StatusUnauthorized)
		return
	}

	// SPIFFE ID is in URI SAN, e.g. spiffe://crane.internal/subd/i-0abc123def456
	spiffeID := cert.URIs[0].String()
	parts := strings.Split(spiffeID, "/")
	hostID := parts[len(parts)-1]

	s.logger.Info("state requested", "path", r.URL.Path, "spiffe_id", spiffeID, "host_id", hostID)

	if hostID == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// get host based on token
	host, err := s.catalog.GetByProviderID(r.Context(), hostID)
	if err != nil {
		s.logger.Error("failed to look up host", "host_id", hostID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.recordState(r.Context(), host); err != nil {
		s.logger.Error("failed to record state", "host_id", hostID, "error", err)
		http.Error(w, "failed to record state", http.StatusInternalServerError)
		return
	}

	desired, err := s.resolver.Resolve(host)
	if err != nil {
		s.logger.Error("failed to resolve desired state", "host_id", hostID, "error", err)
		http.Error(w, "Error in /handleState/Resolve", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(desired); err != nil {
		s.logger.Error("failed to encode response", "host_id", hostID, "error", err)
	}
}
