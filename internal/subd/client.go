package subd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/nabutabu/crane-oss/pkg/api"
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

func NewClient(url, hostID, token string) *Client {
	return &Client{
		URL:    url,
		Token:  token,
		Client: &http.Client{},
	}
}

func (client *Client) Heartbeat(currState api.CurrentState) (*api.DesiredState, error) {
	// Convert the struct to a JSON byte slice
	jsonData, err := json.Marshal(currState)
	if err != nil {
		log.Println(err)
	}

	// POST to Dominator with current state
	req, err := http.NewRequest("POST", client.URL+HEARTBEAT+"?hostID="+client.HostID, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+client.Token)

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

	req.Header.Set("Authorization", "Bearer "+client.Token)

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
