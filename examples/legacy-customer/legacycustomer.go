// Package legacycustomer is the example AfterPatch is built and tested
// against.
//
// An old client asks for GET /api/v1/customer?id=N and expects a flat JSON
// object. The service it used to call has been replaced. The new one answers
// GET /api/v2/customers/N with the customer nested under "customer" and some
// fields renamed. Neither side can be changed. rules.yaml in this folder
// makes them work together.
package legacycustomer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type v2Customer struct {
	ID       int    `json:"id"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Since    string `json:"customer_since"`
}

var customers = map[int]v2Customer{
	7:  {ID: 7, FullName: "Ada Lovelace", Email: "ada@example.com", Since: "2019-03-01"},
	42: {ID: 42, FullName: "Grace Hopper", Email: "grace@example.com", Since: "2021-11-15"},
}

// NewService returns the replacement service. It only knows the v2 API.
func NewService() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/customers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Api-Version", "2")
		id, err := strconv.Atoi(r.PathValue("id"))
		c, ok := customers[id]
		if err != nil || !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"customer not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"customer": c})
	})
	return mux
}

// Customer is what the old client understands.
type Customer struct {
	CustomerID int    `json:"customer_id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
}

// ErrNotFound is returned for a customer that does not exist.
var ErrNotFound = errors.New("customer not found")

// Fetch is the old client. It cannot be changed: it calls the v1 path and
// insists on the v1 shape.
func Fetch(baseURL string, id int) (Customer, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(baseURL, "/") + "/api/v1/customer?id=" + strconv.Itoa(id))
	if err != nil {
		return Customer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Customer{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Customer{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	var c Customer
	if err := dec.Decode(&c); err != nil {
		return Customer{}, fmt.Errorf("response is not in the v1 shape: %w", err)
	}
	if c.CustomerID != id || c.Name == "" || c.Email == "" {
		return Customer{}, fmt.Errorf("response is missing v1 fields: %+v", c)
	}
	return c, nil
}
