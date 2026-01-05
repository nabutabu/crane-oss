package main

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"log"
	"net/http"

	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/reconcile"
)

func main() {
	ctx := context.Background()

	host := "localhost"
	port := 43544
	user := "postgres"
	password := "postgres"
	dbname := "mysecretpassword"

	// 2. Create the connection string
	// The sslmode parameter is often set to 'disable' for local development.
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	db, err := sql.Open("postgres", psqlInfo)
	if err != nil {
		log.Println(err)
	}

	actionStore := execute.NewPostgresActionStore(db)
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)
	provider := provider.NewNoopProvider()

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

	go worker.Run(ctx)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Healthy, %q", html.EscapeString(r.URL.Path))
	})

	log.Fatal(http.ListenAndServe(":43060", nil))
}
