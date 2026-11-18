// Command fakeapi serves the in-memory Codigate API fake for local end-to-end runs of the provider:
//
//	go run ./cmd/fakeapi -addr 127.0.0.1:8787 -token cga_api_local
//
// then point the provider at api_url = "http://127.0.0.1:8787/api/v1" with that token.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/codigate/terraform-provider-codigate/internal/fakeapi"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	token := flag.String("token", "local-admin-token", "admin bearer token")
	viewer := flag.String("viewer-token", "local-viewer-token", "read-only bearer token")
	flag.Parse()
	api := fakeapi.New(*token, *viewer)
	log.Printf("fake Codigate API on http://%s/api/v1", *addr)
	log.Fatal(http.ListenAndServe(*addr, logRequests(api)))
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		h.ServeHTTP(w, r)
	})
}
