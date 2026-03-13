package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/nabutabu/crane-oss/internal/subd"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

const (
	APP_SETTINGS_FILE = "appsettings.json"
)

type Configuration struct {
	DominatorURL string
	HostID       string
	Token        string
}

func LoadConfig() Configuration {
	file, err := os.Open(APP_SETTINGS_FILE)
	if err != nil {
		log.Fatal("Error opening config file:", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	configuration := Configuration{}
	err = decoder.Decode(&configuration)
	if err != nil {
		log.Fatal("Error decoding config file:", err)
	}

	return configuration
}

func main() {
	config := LoadConfig()
	ctx := context.Background()

	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		log.Fatalf("failed to create X509Source: %v", err)
	}
	defer source.Close()

	runner := subd.NewRunner(
		subd.NewClient(config.DominatorURL, config.HostID, config.Token, source),
		subd.NewServicesCollector(),
		subd.NewPackagesCollecetor(),
		time.Second*10)

	runner.Run()
}
