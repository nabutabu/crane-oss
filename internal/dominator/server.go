package dominator

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nabutabu/crane-oss/internal/config"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostoperations"
	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

type Server struct {
	addr       string
	db         *sql.DB
	catalog    *service.HostCatalogService
	resolver   *PolicyResolver
	logger     *slog.Logger
	Middleware func(http.Handler) http.Handler
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
	domain := config.GetEnv("SPIFFE_TRUST_DOMAIN", "crane-oss")
	zone := config.GetEnv("CRANE_ZONE_ID", "")
	if zone == "" {
		return errors.New("CRANE_ZONE_ID is required")
	}
	expectedOwnID, err := api.DominatorID(domain, zone)
	if err != nil {
		return err
	}
	apiID, err := spiffeid.FromString("spiffe://" + domain + "/crane-api")
	if err != nil {
		return errors.New("invalid Crane API identity")
	}
	own, err := source.GetX509SVID()
	if err != nil {
		return errors.New("Dominator workload identity unavailable")
	}
	if own.ID != expectedOwnID {
		return errors.New("Dominator workload identity does not match CRANE_ZONE_ID")
	}
	controller := hostoperations.Controller{Store: hostoperations.Store{DB: s.db}, Caller: hostrpc.Client{Source: source}, ZoneID: zone, TrustDomain: domain, OwnIdentity: own.ID.String()}
	mux.Handle("PUT "+hostoperations.InternalPath, controller.Handler(apiID.String()))

	mux.HandleFunc("/v1/health", s.Health)
	mux.HandleFunc("/v1/db/health", s.dbHealth)

	s.logger.Info("dominator listening", "addr", s.addr)

	allowedID, err := spiffeid.FromString("spiffe://" + domain + "/subd")
	if err != nil {
		return fmt.Errorf("invalid SPIFFE trust domain: %w", err)
	}

	tlsConfig := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeOneOf(allowedID, apiID))
	var handler http.Handler = mux
	if s.Middleware != nil {
		handler = s.Middleware(handler)
	}
	server := &http.Server{
		Addr:              s.addr,
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      api.OperationTimeout + time.Minute,
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
