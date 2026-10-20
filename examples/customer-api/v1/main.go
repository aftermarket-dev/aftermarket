// Command v1 runs the example's old customer API, the one the old client
// was written against.
//
//	go run ./examples/customer-api/v1 -listen 127.0.0.1:9001
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	customerapi "github.com/aftermarket-dev/aftermarket/examples/customer-api"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9001", "address to listen on")
	flag.Parse()
	log.Printf("customer API v1 on http://%s", *listen)
	srv := &http.Server{Addr: *listen, Handler: customerapi.NewV1(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
