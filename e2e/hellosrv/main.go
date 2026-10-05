//go:build e2e

// Command hellosrv is the app of the prebuilt-image e2e step: a static
// binary, the only file of its image, that answers every request on $PORT.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on %s", port)
	log.Fatal(http.ListenAndServe(":"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "hello from a prebuilt image")
	})))
}
