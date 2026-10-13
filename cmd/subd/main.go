package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/subd"
	"github.com/nabutabu/crane-oss/internal/version"
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

func LoadConfig(logger *logging.Logger) Configuration {
	file, err := os.Open(APP_SETTINGS_FILE)
	if err != nil {
		logger.Error("Error opening config file", "error", err)
		os.Exit(1)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	configuration := Configuration{}
	err = decoder.Decode(&configuration)
	if err != nil {
		logger.Error("Error decoding config file", "error", err)
		os.Exit(1)
	}

	return configuration
}

func main() {
	logger := logging.New("subd")
	logger.Info("subd", "version", version.String())
	config := LoadConfig(logger)
	ctx := context.Background()

	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		logger.Error("failed to create X509Source", "error", err)
		os.Exit(1)
	}
	defer source.Close()

	runner := subd.NewRunner(
		subd.NewClient(config.DominatorURL, config.HostID, config.Token, source),
		subd.NewServicesCollector(),
		subd.NewPackagesCollecetor(),
		time.Second*10)

	runner.Run()
}
