package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/activitymanager"
	"github.com/nabutabu/crane-oss/internal/activitymanager/problemcache"
	"github.com/nabutabu/crane-oss/internal/badhost"
	"github.com/nabutabu/crane-oss/internal/badhost/checks"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
)

const BHD_CONFIG_YAML_PATH = "bhd.json"

type configJSON struct {
	Zone                string   `json:"zone"`
	ScanIntervalMinutes int      `json:"scan_interval"`
	Checks              []string `json:"checks"`
}

func LoadConfigFromFile(path string) (*badhost.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var tmp configJSON
	err = json.Unmarshal(data, &tmp)
	if err != nil {
		return nil, err
	}

	return &badhost.Config{
		Zone:         tmp.Zone,
		ScanInterval: time.Duration(tmp.ScanIntervalMinutes) * time.Minute,
		Checks:       tmp.Checks,
	}, nil
}

func BuildChecks(enabled []string, deps checks.Dependencies) ([]checks.Check, error) {
	var res []checks.Check

	for _, name := range enabled {
		factory, ok := checks.CheckCatalog[name]
		if !ok {
			return nil, fmt.Errorf("check does not exist: %s", name)
		}

		res = append(res, factory(deps))
	}

	return res, nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		var intVal int
		if _, err := fmt.Sscanf(value, "%d", &intVal); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func main() {
	ctx := context.Background()

	host := getEnv("DB_HOST", "localhost")
	port := getEnvInt("DB_PORT", 5432)
	user := getEnv("DB_USER", "postgres")
	password := getEnv("DB_PASSWORD", "mysecretpassword")
	dbname := getEnv("DB_NAME", "crane")

	// 2. Create the connection string
	// The sslmode parameter is often set to 'disable' for local development.
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	db, err := sql.Open("pgx", psqlInfo)
	if err != nil {
		log.Println(err)
	}

	actionStore := execute.NewPostgresActionStore(db)
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-west-2"), // Optional: specify region here
	)
	if err != nil {
		log.Fatalf("unable to load SDK config, %v", err)
	}

	ec2Client := ec2.NewFromConfig(cfg)

	deps := checks.Dependencies{
		EC2Client:   ec2Client,
		HostCatalog: hostCatalog,
	}

	provider := awscompute.New(ec2Client)

	executor := execute.NewDefaultExecutor(
		hostCatalog,
		provider,
	)

	worker := execute.NewWorker(
		actionStore,
		executor,
	)

	problemStore := problem.New(db)

	bhd_config, err := LoadConfigFromFile(BHD_CONFIG_YAML_PATH)
	if err != nil {
		log.Println(err)
	}

	checks, err := BuildChecks(bhd_config.Checks, deps)
	if err != nil {
		log.Println(err)
	}

	bhd := badhost.New(hostCatalog, problemStore, checks, bhd_config)

	manager := activitymanager.NewActivityManager(problemStore, actionStore, problemcache.NewCache(10, time.Hour), time.Minute*5, time.Minute)

	// Activity Manager - Create actions based on problems seen by BHD
	go manager.Run(ctx)

	// Bad Host Detector - Detect problems with existing Hosts in a Zone
	go bhd.Run(ctx)

	// Worker to put the hosts in appropriate states once detected by BHD
	go worker.Run(ctx)

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Healthy, %s", r.URL.Path)
		fmt.Fprintf(w, `{"status": "available"}`)
	})

	mux.HandleFunc("/v1/db/health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status": "unavailable", "error": "%v"}`, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status": "available"}`)
	})

	log.Fatal(http.ListenAndServe(":43060", mux))
}
