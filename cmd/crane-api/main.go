package main

import (
	"html"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Healthy, %q", html.EscapeString(r.URL.Path))
	})

	log.Fatal(http.ListenAndServe(":43060", nil))
}
