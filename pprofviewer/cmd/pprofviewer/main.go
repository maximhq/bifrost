package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/maximhq/bifrost/pprofviewer/internal/server"
)

func main() {
	addr := flag.String("addr", ":7777", "HTTP listen address")
	flag.Parse()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.New(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("pprofviewer listening on http://localhost%s", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
