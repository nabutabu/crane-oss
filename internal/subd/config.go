package subd

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

type Config struct {
	ListenAddress    string
	TrustDomain      string
	WriterIdentity   string
	ReaderIdentities []string
	Kubeconfig       string
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("cannot open Subd configuration")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil {
		return cfg, errors.New("invalid Subd configuration")
	}
	if d.Decode(new(any)) != io.EOF {
		return cfg, errors.New("invalid Subd configuration trailer")
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.ListenAddress)
	ip := net.ParseIP(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || n < 1 || n > 65535 || ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return errors.New("Subd listen address must be a private or loopback IP and port")
	}
	td, err := spiffeid.TrustDomainFromString(c.TrustDomain)
	if err != nil || td.String() != c.TrustDomain {
		return errors.New("invalid Subd trust domain")
	}
	if c.WriterIdentity == "" {
		return errors.New("Subd writer identity is required")
	}
	for _, raw := range append([]string{c.WriterIdentity}, c.ReaderIdentities...) {
		id, err := spiffeid.FromString(raw)
		if err != nil || id.TrustDomain() != td || id.Path() == "" {
			return errors.New("Subd peer identity must belong to its trust domain")
		}
	}
	return nil
}

func validateOwnID(raw, domain string) error {
	id, err := spiffeid.FromString(raw)
	if err != nil || id.TrustDomain().String() != domain || !strings.HasPrefix(id.Path(), "/subd/") || strings.Contains(strings.TrimPrefix(id.Path(), "/subd/"), "/") || id.Path() == "/subd/" {
		return errors.New("unexpected Subd workload identity")
	}
	return nil
}
