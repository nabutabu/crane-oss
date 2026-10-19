package autoscaler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type HTTPClient struct {
	baseURL, token, clusterID string
	http                      *http.Client
}

func NewHTTPClient(baseURL, token, clusterID string) (*HTTPClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") || token == "" || clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "Crane URL and adapter credentials are required")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, status.Error(codes.InvalidArgument, "Crane HTTP without TLS is allowed only on loopback")
	}
	u.Path = ""
	return &HTTPClient{baseURL: u.String(), token: token, clusterID: clusterID,
		http: &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *HTTPClient) Target(ctx context.Context, group string) (api.CapacityResult, error) {
	var result api.CapacityResult
	err := c.do(ctx, http.MethodGet, "/v1/node-groups/"+url.PathEscape(group)+"/target", nil, &result)
	if err == nil && (result.NodeGroupID != group || result.TargetNodes < 0 || result.TargetRevision < 0) {
		err = status.Error(codes.Internal, "invalid Crane target response")
	}
	return result, err
}

func (c *HTTPClient) SetTarget(ctx context.Context, req api.CapacityRequest) (api.CapacityResult, error) {
	var result api.CapacityResult
	input := struct {
		NodeGroupID            string `json:"node_group_id"`
		RequestID              string `json:"request_id"`
		ExpectedTargetRevision int64  `json:"expected_target_revision"`
		DesiredTarget          int64  `json:"desired_target"`
		TemplateRevision       string `json:"template_revision"`
	}{req.NodeGroupID, req.RequestID, req.ExpectedTargetRevision, req.DesiredTarget, req.TemplateRevision}
	err := c.do(ctx, http.MethodPost, "/v1/capacity", input, &result)
	if err == nil && (result.NodeGroupID != req.NodeGroupID || result.RequestID != req.RequestID || result.TargetNodes != req.DesiredTarget || result.TargetRevision != req.ExpectedTargetRevision+1) {
		err = status.Error(codes.Unavailable, "Crane outcome could not be verified")
	}
	return result, err
}

func (c *HTTPClient) RemoveNodes(ctx context.Context, req api.NodeRemovalRequest) error {
	return c.do(ctx, http.MethodPost, "/v1/node-removals", req, nil)
}

func (c *HTTPClient) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return status.Error(codes.Internal, "cannot encode Crane request")
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return status.Error(codes.Internal, "cannot construct Crane request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Crane-Cluster-ID", c.clusterID)
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		// Transport failures can include credentials or endpoint details. Never
		// forward their raw text to CA or telemetry.
		return status.Error(codes.Unavailable, "Crane transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := codes.Internal
		switch res.StatusCode {
		case http.StatusBadRequest:
			code = codes.InvalidArgument
		case http.StatusUnauthorized:
			code = codes.Unauthenticated
		case http.StatusForbidden:
			code = codes.PermissionDenied
		case http.StatusNotFound:
			code = codes.NotFound
		case http.StatusConflict:
			code = codes.Aborted
		case http.StatusUnprocessableEntity:
			code = codes.FailedPrecondition
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			code = codes.Unavailable
		}
		// Only consume a known machine code; never expose backend bodies.
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&failure)
		if res.StatusCode == http.StatusUnprocessableEntity && failure.Code == "capacity_limit" {
			code = codes.ResourceExhausted
		}
		return status.Error(code, "Crane rejected request")
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(output); err != nil {
			// A mutation may already have committed. Retry exactly the same
			// request key after an incomplete response.
			return status.Error(codes.Unavailable, "Crane response incomplete")
		}
	}
	return nil
}
