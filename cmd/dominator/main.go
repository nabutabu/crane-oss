package main

import (
	"log"

	"github.com/nabutabu/crane-oss/internal/dominator"
)

func main() {
	server := dominator.NewServer(":44000")

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}
