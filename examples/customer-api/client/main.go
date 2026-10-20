// Command client runs the old client's test suite against a server and
// prints each check. Run it against a fresh server: it creates and deletes
// a customer.
//
//	go run ./examples/customer-api/client -base http://127.0.0.1:8080
package main

import (
	"flag"
	"fmt"
	"os"

	customerapi "github.com/aftermarket-dev/aftermarket/examples/customer-api"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "where the old client sends requests")
	flag.Parse()
	checks := customerapi.Contract(*base)
	failed := 0
	for _, c := range checks {
		if c.Err == nil {
			fmt.Printf("PASS  %s\n", c.Name)
			continue
		}
		failed++
		fmt.Printf("FAIL  %s\n      %v\n", c.Name, c.Err)
	}
	fmt.Printf("%d checks, %d failed\n", len(checks), failed)
	if failed > 0 {
		os.Exit(1)
	}
}
