package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/dominator"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

func main() {
	host := getEnv("DB_HOST", "localhost")
	port := getEnvInt("DB_PORT", 43544)
	user := getEnv("DB_USER", "postgres")
	password := getEnv("DB_PASSWORD", "mysecretpassword")
	dbname := getEnv("DB_NAME", "crane")

	ctx := context.Background()

	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		log.Fatalf("failed to create X509 source: %v", err)
	}
	defer source.Close()

	svid, err := source.GetX509SVID()
	if err != nil {
		log.Fatal("Couldnt get X509 %v", err)
	}

	log.Println("SPIFFE ID:", svid.ID.String())

	// 2. Create the connection string
	// The sslmode parameter is often set to 'disable' for local development.
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	db, err := sql.Open("pgx", psqlInfo)
	if err != nil {
		log.Println(err)
	}
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)

	resolver, err := dominator.NewPolicyResolver("./policy/os.star")
	if err != nil {
		log.Fatal(err)
	}

	server := dominator.NewServer(":44000", db, hostCatalog, resolver)

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var intVal int
		if _, err := fmt.Sscanf(value, "%d", &intVal); err == nil {
			return intVal
		}
	}
	return defaultValue
}
