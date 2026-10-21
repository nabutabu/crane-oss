// Package hostrpc provides the private, identity-pinned request transport.
package hostrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const DeadlineHeader = "X-Crane-Deadline"

type IdentitySource interface {
	x509svid.Source
	x509bundle.Source
}
type Caller interface {
	Call(context.Context, string, string, string, string, any, any) (int, error)
}
type Client struct{ Source IdentitySource }

func (c Client) Call(ctx context.Context, endpoint, identity, method, path string, in, out any) (int, error) {
	ctx, span := otel.Tracer("crane/hostrpc").Start(ctx, "private host request", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	status, err := c.call(ctx, endpoint, identity, method, path, in, out)
	span.SetAttributes(attribute.Int("http.response.status_code", status))
	if err != nil || status >= 400 {
		span.SetStatus(codes.Error, "private request failed")
	}
	return status, err
}

func (c Client) call(ctx context.Context, endpoint, identity, method, path string, in, out any) (int, error) {
	u, err := url.Parse(endpoint)
	peer, idErr := spiffeid.FromString(identity)
	if err != nil || idErr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return 0, errors.New("invalid private destination")
	}
	var data []byte
	if in != nil {
		data, err = json.Marshal(in)
		if err != nil {
			return 0, errors.New("cannot encode private request")
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(data))
	if err != nil {
		return 0, errors.New("cannot construct private request")
	}
	request.Header.Set("Content-Type", "application/json")
	if deadline, ok := ctx.Deadline(); ok {
		request.Header.Set(DeadlineHeader, deadline.UTC().Format(time.RFC3339Nano))
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	transport := &http.Transport{TLSClientConfig: tlsconfig.MTLSClientConfig(c.Source, c.Source, tlsconfig.AuthorizeID(peer)), TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: api.OperationTimeout + time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("private request failed; completion may be unknown")
	}
	defer response.Body.Close()
	// Never forward raw response bodies, even on errors.
	if out != nil && response.Header.Get("Content-Type") == "application/json" {
		decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
		if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
			return response.StatusCode, errors.New("invalid private response")
		}
	} else if response.StatusCode == http.StatusOK && out != nil {
		return response.StatusCode, errors.New("invalid private response")
	}
	return response.StatusCode, nil
}

// Context preserves the earliest deadline throughout a blocking request chain.
func Context(r *http.Request) (context.Context, context.CancelFunc, error) {
	deadline := time.Now().Add(api.OperationTimeout)
	if raw := r.Header.Get(DeadlineHeader); raw != "" {
		incoming, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, nil, errors.New("invalid operation deadline")
		}
		if incoming.Before(deadline) {
			deadline = incoming
		}
	}
	base := r.Context()
	if !trace.SpanContextFromContext(base).IsValid() {
		base = otel.GetTextMapPropagator().Extract(base, propagation.HeaderCarrier(r.Header))
	}
	ctx, cancel := context.WithDeadline(base, deadline)
	return ctx, cancel, nil
}

func Decode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, api.KubernetesBodyLimit))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func Peer(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	id, err := x509svid.IDFromCert(r.TLS.PeerCertificates[0])
	if err != nil {
		return ""
	}
	return id.String()
}
