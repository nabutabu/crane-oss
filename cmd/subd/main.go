package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/subd"
	"github.com/nabutabu/crane-oss/internal/version"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

func main() {
	logger := logging.New("subd")
	logger.SetDefault()
	path := flag.String("config", "/usr/local/bin/appsettings.json", "deployment configuration")
	flag.Parse()
	logger.Info("subd", "version", version.String())
	if err := run(*path); err != nil {
		logger.Error("Subd stopped", "error", err)
		os.Exit(1)
	}
}

func run(path string) error {
	cfg, err := subd.LoadConfig(path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	initial, cancel := context.WithTimeout(ctx, 30*time.Second)
	source, err := workloadapi.NewX509Source(initial)
	cancel()
	if err != nil {
		return err
	}
	defer source.Close()
	own, err := source.GetX509SVID()
	if err != nil {
		return err
	}
	s, err := subd.NewServer(cfg, own.ID.String(), "/var/lib/crane/subd/state.json", "/run/crane/subd/status.json", subd.LocalHost{})
	if err != nil {
		return err
	}
	return s.Serve(ctx, source)
}
