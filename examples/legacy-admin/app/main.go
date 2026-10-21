// Command app runs the legacy admin panel on 127.0.0.1:9100, the address
// guard.yaml expects. Bound to loopback, it can only be reached through
// AfterGuard on the same machine.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/aftermarket-dev/aftermarket/examples/legacy-admin"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9100", "address to listen on")
	flag.Parse()
	log.Printf("legacy admin panel on http://%s (no login of its own)", *listen)
	log.Fatal(http.ListenAndServe(*listen, legacyadmin.New()))
}
