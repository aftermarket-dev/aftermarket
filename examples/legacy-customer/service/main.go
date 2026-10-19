// Command service runs the example's new v2 customer service.
//
//	go run ./examples/legacy-customer/service -listen 127.0.0.1:9000
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	legacycustomer "github.com/aftermarket-dev/aftermarket/examples/legacy-customer"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9000", "address to listen on")
	flag.Parse()
	log.Printf("v2 customer service on http://%s (try /api/v2/customers/7)", *listen)
	srv := &http.Server{Addr: *listen, Handler: legacycustomer.NewService(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
