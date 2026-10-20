package customerapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Check is one result of the contract suite.
type Check struct {
	Name string
	Err  error
}

// Contract is the old client's test suite: everything the v1 client relies
// on, checked strictly. Field names, extra fields, status codes and error
// messages all count. It runs against a fresh service at base.
func Contract(base string) []Check {
	c := &client{base: strings.TrimSuffix(base, "/"), http: &http.Client{Timeout: 10 * time.Second}}
	var out []Check
	check := func(name string, f func() error) {
		out = append(out, Check{Name: name, Err: f()})
	}

	check("get a customer", func() error {
		got, err := c.call("GET", "/api/v1/customer?id=7", nil, 200)
		if err != nil {
			return err
		}
		return same(got, map[string]any{"customer_id": 7.0, "name": "Ada Lovelace", "email": "ada@example.com"})
	})
	check("missing customer is a 404 with a message", func() error {
		got, err := c.call("GET", "/api/v1/customer?id=999", nil, 404)
		if err != nil {
			return err
		}
		return same(got, map[string]any{"message": "customer not found"})
	})
	check("list customers", func() error {
		got, err := c.call("GET", "/api/v1/customers", nil, 200)
		if err != nil {
			return err
		}
		return same(got, []any{
			map[string]any{"customer_id": 7.0, "name": "Ada Lovelace"},
			map[string]any{"customer_id": 42.0, "name": "Grace Hopper"},
			map[string]any{"customer_id": 55.0, "name": "Alan Turing"},
		})
	})
	check("list honours limit", func() error {
		got, err := c.call("GET", "/api/v1/customers?limit=2", nil, 200)
		if err != nil {
			return err
		}
		list, ok := got.([]any)
		if !ok || len(list) != 2 {
			return fmt.Errorf("want 2 customers, got %v", got)
		}
		return nil
	})
	check("search by name", func() error {
		got, err := c.call("GET", "/api/v1/customers/search?q=grace", nil, 200)
		if err != nil {
			return err
		}
		return same(got, []any{map[string]any{"customer_id": 42.0, "name": "Grace Hopper"}})
	})
	check("search with no match is an empty list", func() error {
		got, err := c.call("GET", "/api/v1/customers/search?q=nobody", nil, 200)
		if err != nil {
			return err
		}
		return same(got, []any{})
	})
	var created float64
	check("create a customer", func() error {
		got, err := c.call("POST", "/api/v1/customers", map[string]any{"name": "Zoë Ångström", "email": "zoe@example.com"}, 200)
		if err != nil {
			return err
		}
		m, _ := got.(map[string]any)
		id, ok := m["customer_id"].(float64)
		if !ok || id <= 0 {
			return fmt.Errorf("no customer_id in %v", got)
		}
		created = id
		return same(got, map[string]any{"customer_id": id, "name": "Zoë Ångström", "email": "zoe@example.com"})
	})
	check("create without email is a 400 with a message", func() error {
		got, err := c.call("POST", "/api/v1/customers", map[string]any{"name": "No Mail"}, 400)
		if err != nil {
			return err
		}
		return same(got, map[string]any{"message": "email is required"})
	})
	check("a created customer can be read back", func() error {
		if created == 0 {
			return fmt.Errorf("nothing was created")
		}
		got, err := c.call("GET", fmt.Sprintf("/api/v1/customer?id=%d", int(created)), nil, 200)
		if err != nil {
			return err
		}
		return same(got, map[string]any{"customer_id": created, "name": "Zoë Ångström", "email": "zoe@example.com"})
	})
	check("delete a customer", func() error {
		if created == 0 {
			return fmt.Errorf("nothing was created")
		}
		got, err := c.call("DELETE", fmt.Sprintf("/api/v1/customer?id=%d", int(created)), nil, 200)
		if err != nil {
			return err
		}
		return same(got, map[string]any{"deleted": true})
	})
	check("a deleted customer is gone", func() error {
		got, err := c.call("GET", fmt.Sprintf("/api/v1/customer?id=%d", int(created)), nil, 404)
		if err != nil {
			return err
		}
		return same(got, map[string]any{"message": "customer not found"})
	})
	check("deleting a missing customer is a 404 with a message", func() error {
		got, err := c.call("DELETE", "/api/v1/customer?id=999", nil, 404)
		if err != nil {
			return err
		}
		return same(got, map[string]any{"message": "customer not found"})
	})
	return out
}

type client struct {
	base string
	http *http.Client
}

// call sends a request the way the old client does and decodes the JSON
// answer. Any other status than want is an error.
func (c *client) call(method, path string, body any, want int) (any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		return nil, fmt.Errorf("%s %s: status %d, want %d (body %s)", method, path, resp.StatusCode, want, trim(data))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil, fmt.Errorf("%s %s: content type %q, want JSON", method, path, resp.Header.Get("Content-Type"))
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s %s: body is not JSON: %s", method, path, trim(data))
	}
	return v, nil
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// same compares exactly: no missing fields and no extra ones.
func same(got, want any) error {
	if reflect.DeepEqual(got, want) {
		return nil
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return fmt.Errorf("got %s, want %s", g, w)
}

// Failed returns the names of the checks that failed.
func Failed(checks []Check) []string {
	var out []string
	for _, c := range checks {
		if c.Err != nil {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}
