// The welcome page of the Go image, for a new website with no application yet.
// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Go</title><p>This website has no Go application yet. Upload a go.mod to start your application.</p>\n"))
	})
	_ = http.ListenAndServe(os.Getenv("HOST")+":"+port, nil)
}
