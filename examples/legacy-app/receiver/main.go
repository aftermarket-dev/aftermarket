// Command receiver is a small webhook receiver for the legacy-app example.
// It checks each event's signature and prints the events it accepts.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9900", "address to listen on")
	flag.Parse()
	secret := os.Getenv("HOOK_SECRET")
	var mu sync.Mutex // handlers run concurrently
	seen := map[string]bool{}
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		ts, _ := strconv.ParseInt(r.Header.Get("X-Aftermarket-Timestamp"), 10, 64)
		if secret != "" {
			m := hmac.New(sha256.New, []byte(secret))
			fmt.Fprintf(m, "%d.", ts)
			m.Write(body)
			want := "sha256=" + hex.EncodeToString(m.Sum(nil))
			if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Aftermarket-Signature"))) {
				http.Error(w, "bad signature", http.StatusUnauthorized)
				return
			}
			if age := time.Since(time.Unix(ts, 0)); age > 5*time.Minute || age < -5*time.Minute {
				http.Error(w, "too old", http.StatusUnauthorized)
				return
			}
		}
		id := r.Header.Get("X-Aftermarket-Event")
		mu.Lock()
		defer mu.Unlock()
		if seen[id] {
			fmt.Printf("repeat of %s, ignored\n", id) // at-least-once delivery
			return
		}
		seen[id] = true
		fmt.Printf("%s\n", body)
	})
	log.Printf("receiver on http://%s/events", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
