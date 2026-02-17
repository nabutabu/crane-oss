package subd

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/nabutabu/crane-oss/pkg/api"
)

const (
	HEARTBEAT = "/v1/heartbeat"
)

type Client struct {
	URL    string
	Token  string
	Client *http.Client
}

func NewClient(url string, token string) *Client {
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
	req, err := http.NewRequest("POST", client.URL+HEARTBEAT, bytes.NewReader(jsonData))
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
