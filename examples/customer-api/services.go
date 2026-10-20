// Package customerapi is the example `aftermarket adapt` is built and
// tested against: a customer API whose v1 was replaced by a v2 with
// different paths, names, shapes and status codes.
//
// NewV1 is the old API, kept as a reference so the contract suite can be
// checked against the real thing. NewV2 is the new API, which only knows
// v2. adapt.yaml in this folder makes v2 answer like v1.
package customerapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type customer struct {
	ID    int
	Name  string
	Email string
	Since string
}

type store struct {
	mu     sync.Mutex
	nextID int
	byID   map[int]customer
}

func newStore() *store {
	return &store{nextID: 100, byID: map[int]customer{
		7:  {ID: 7, Name: "Ada Lovelace", Email: "ada@example.com", Since: "2019-03-01"},
		42: {ID: 42, Name: "Grace Hopper", Email: "grace@example.com", Since: "2021-11-15"},
		55: {ID: 55, Name: "Alan Turing", Email: "alan@example.com", Since: "2022-06-23"},
	}}
}

func (s *store) get(id int) (customer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byID[id]
	return c, ok
}

func (s *store) list(search string, limit int) []customer {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []customer
	for _, c := range s.byID {
		if search == "" || strings.Contains(strings.ToLower(c.Name), strings.ToLower(search)) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *store) create(name, email string) customer {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := customer{ID: s.nextID, Name: name, Email: email, Since: "2026-10-20"}
	s.nextID++
	s.byID[c.ID] = c
	return c
}

func (s *store) delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byID[id]
	delete(s.byID, id)
	return ok
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func intParam(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// NewV1 returns the old API, as the old client knew it.
func NewV1() http.Handler {
	st := newStore()
	type full struct {
		CustomerID int    `json:"customer_id"`
		Name       string `json:"name"`
		Email      string `json:"email"`
	}
	type short struct {
		CustomerID int    `json:"customer_id"`
		Name       string `json:"name"`
	}
	notFound := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "customer not found"})
	}
	shorts := func(cs []customer) []short {
		out := []short{}
		for _, c := range cs {
			out = append(out, short{c.ID, c.Name})
		}
		return out
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/customer", func(w http.ResponseWriter, r *http.Request) {
		id, ok := intParam(r.URL.Query().Get("id"))
		c, found := st.get(id)
		if !ok || !found {
			notFound(w)
			return
		}
		writeJSON(w, http.StatusOK, full{c.ID, c.Name, c.Email})
	})
	mux.HandleFunc("DELETE /api/v1/customer", func(w http.ResponseWriter, r *http.Request) {
		id, ok := intParam(r.URL.Query().Get("id"))
		if !ok || !st.delete(id) {
			notFound(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("GET /api/v1/customers", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := intParam(r.URL.Query().Get("limit"))
		writeJSON(w, http.StatusOK, shorts(st.list("", limit)))
	})
	mux.HandleFunc("GET /api/v1/customers/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, shorts(st.list(r.URL.Query().Get("q"), 0)))
	})
	mux.HandleFunc("POST /api/v1/customers", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "name is required"})
			return
		}
		if in.Email == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "email is required"})
			return
		}
		c := st.create(in.Name, in.Email)
		writeJSON(w, http.StatusOK, full{c.ID, c.Name, c.Email})
	})
	return mux
}

// NewV2 returns the new API. It knows nothing about v1.
func NewV2() http.Handler {
	st := newStore()
	type full struct {
		ID       int    `json:"id"`
		FullName string `json:"full_name"`
		Email    string `json:"email"`
		Since    string `json:"customer_since"`
	}
	conv := func(c customer) full { return full{c.ID, c.Name, c.Email, c.Since} }
	apiErr := func(w http.ResponseWriter, status int, code, msg string) {
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/customers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Api-Version", "2")
		id, ok := intParam(r.PathValue("id"))
		c, found := st.get(id)
		if !ok || !found {
			apiErr(w, http.StatusNotFound, "not_found", "customer not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"customer": conv(c)})
	})
	mux.HandleFunc("DELETE /api/v2/customers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := intParam(r.PathValue("id"))
		if !ok || !st.delete(id) {
			apiErr(w, http.StatusNotFound, "not_found", "customer not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v2/customers", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := intParam(r.URL.Query().Get("limit"))
		data := []full{}
		for _, c := range st.list(r.URL.Query().Get("search"), limit) {
			data = append(data, conv(c))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "next": nil})
	})
	mux.HandleFunc("POST /api/v2/customers", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			apiErr(w, http.StatusUnsupportedMediaType, "unsupported", "send JSON")
			return
		}
		var in struct {
			FullName string `json:"full_name"`
			Email    string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.FullName == "" {
			apiErr(w, http.StatusUnprocessableEntity, "invalid", "name is required")
			return
		}
		if in.Email == "" {
			apiErr(w, http.StatusUnprocessableEntity, "invalid", "email is required")
			return
		}
		c := st.create(in.FullName, in.Email)
		writeJSON(w, http.StatusCreated, map[string]any{"customer": conv(c)})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		apiErr(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return mux
}
