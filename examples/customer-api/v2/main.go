// Command v2 runs the example's new customer API. It only knows v2.
//
//	go run ./examples/customer-api/v2 -listen 127.0.0.1:9002
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	customerapi "github.com/aftermarket-dev/aftermarket/examples/customer-api"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9002", "address to listen on")
	flag.Parse()
	log.Printf("customer API v2 on http://%s", *listen)
	srv := &http.Server{Addr: *listen, Handler: customerapi.NewV2(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
