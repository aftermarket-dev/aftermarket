// Command client is the example's old client. It asks for a customer the v1
// way and fails unless the answer is in the v1 shape.
//
//	go run ./examples/legacy-customer/client -base http://127.0.0.1:8080 -id 7
package main

import (
	"flag"
	"fmt"
	"os"

	legacycustomer "github.com/aftermarket-dev/aftermarket/examples/legacy-customer"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "where the old client sends requests")
	id := flag.Int("id", 7, "customer id")
	flag.Parse()
	c, err := legacycustomer.Fetch(*base, *id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "old client failed:", err)
		os.Exit(1)
	}
	fmt.Printf("old client got customer %d: %s <%s>\n", c.CustomerID, c.Name, c.Email)
}
