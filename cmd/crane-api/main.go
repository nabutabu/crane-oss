package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/badhost"
	"github.com/nabutabu/crane-oss/internal/badhost/checks"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
	"github.com/nabutabu/crane-oss/pkg/reconcile"
)

const BHD_CONFIG_YAML_PATH = "../../bhd.json"

func LoadConfigFromFile(path string) (*badhost.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config badhost.Config
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func BuildChecks(enabled []string) ([]checks.Check, error) {
	var res []checks.Check
	for _, name := range enabled {
		c, ok := checks.CheckCatalog[name]
		if !ok {
			return nil, fmt.Errorf("unknown check: %s", name)
		}
		res = append(res, c)
	}
	return res, nil
}

func main() {
	ctx := context.Background()

	host := "localhost"
	port := 43544
	user := "postgres"
	password := "mysecretpassword"
	dbname := "crane"

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

	provider := awscompute.New(ec2Client)

	executor := execute.NewDefaultExecutor(
		hostCatalog,
		provider,
	)

	reconcile.NewDefaultHostReconciler(
		*hostStore,
		actionStore,
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

	checks, err := BuildChecks(bhd_config.Checks)
	if err != nil {
		log.Println(err)
	}

	bhd := badhost.New(hostCatalog, problemStore, checks, bhd_config)

	// Bad Host Detector
	go bhd.Run(ctx)

	// Worker to put the hosts in appropriate states once detected by BHD
	go worker.Run(ctx)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Healthy, %q", html.EscapeString(r.URL.Path))
	})

	log.Fatal(http.ListenAndServe(":43060", nil))
}
