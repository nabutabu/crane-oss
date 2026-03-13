package subd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

const (
	HEARTBEAT = "/v1/heartbeat"
	HEALTH    = "/v1/health"
)

type Client struct {
	URL    string
	HostID string
	Token  string
	Client *http.Client
}

func NewClient(url, hostID, token string, source *workloadapi.X509Source) *Client {
	serverID := spiffeid.RequireFromString("spiffe://crane-oss/dominator")
	tlsConfig := tlsconfig.MTLSClientConfig(
		source, // client SVID
		source, // trust bundle
		tlsconfig.AuthorizeID(serverID),
	)

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	return &Client{
		URL:   url,
		Token: token,
		Client: &http.Client{
			Transport: transport,
		},
	}
}

func (client *Client) Heartbeat(currState api.CurrentState) (*api.DesiredState, error) {
	// Convert the struct to a JSON byte slice
	jsonData, err := json.Marshal(currState)
	if err != nil {
		log.Println(err)
	}

	// POST to Dominator with current state
	req, err := http.NewRequest("POST", client.URL+HEARTBEAT, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}

	response, err := client.Client.Do(req)
	if err != nil {
		return nil, err
	}

	var desiredState api.DesiredState
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(body, &desiredState)

	return &desiredState, nil
}

func (client *Client) Health() error {
	req, err := http.NewRequest("GET", client.URL+HEALTH, nil)
	if err != nil {
		return err
	}

	response, err := client.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed with status: %d", response.StatusCode)
	}

	return nil
}
