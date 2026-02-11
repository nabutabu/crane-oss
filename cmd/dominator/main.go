package main

import (
	"database/sql"
	"fmt"
	"log"

	"github.com/nabutabu/crane-oss/internal/dominator"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
)

func main() {
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
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)

	resolver, err := dominator.NewPolicyResolver("./policy/os.star")
	if err != nil {
		log.Fatal(err)
	}

	server := dominator.NewServer(":44000", hostCatalog, resolver)

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}
