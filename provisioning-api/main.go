// Command provisioning-api is a barebones HTTP service that provisions and
// deprovisions ntfy users, topic ACLs, and access tokens for the 4IRL app
// family by shelling out to the documented ntfy CLI.
//
// Phase-1 scaffold: serves only a health endpoint. The provision/deprovision
// surface is built test-first in later phases.
package main

import (
	"log"
	"net/http"
	"os"
)

const defaultListenAddress = ":8080"

func main() {
	listenAddress := os.Getenv("LISTEN_ADDRESS")
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte("ok")); writeErr != nil {
			log.Printf("healthz write failed: %v", writeErr)
		}
	})

	log.Printf("provisioning-api listening on %s", listenAddress)
	if serveErr := http.ListenAndServe(listenAddress, mux); serveErr != nil {
		log.Fatalf("provisioning-api server exited: %v", serveErr)
	}
}
