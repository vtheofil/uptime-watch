package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/vtheofil/uptime-watch/internal/checker"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "serve":
		serve()
	case "check":
		if len(os.Args) != 3 {
			usage()
		}
		check(os.Args[2])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: uptime-watch serve | uptime-watch check <url>")
	os.Exit(2)
}

func check(url string) {
	r := checker.Check(context.Background(), url, 5*time.Second)
	fmt.Printf("ok=%v status=%d latency=%v err=%v\n", r.OK, r.StatusCode, r.Latency, r.Err)
}

func serve() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	log.Println("listening on", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
