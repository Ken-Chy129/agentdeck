// agentdeckd is the server: API + embedded web console + SQLite.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/api"
	"github.com/Ken-Chy129/agentdeck/internal/secret"
	"github.com/Ken-Chy129/agentdeck/internal/store"
	"github.com/Ken-Chy129/agentdeck/web"
)

func main() {
	addr := flag.String("addr", envOr("AGENTDECK_ADDR", "127.0.0.1:8480"), "listen address")
	dataDir := flag.String("data", envOr("AGENTDECK_DATA", "./data"), "data directory")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	adminToken := strings.TrimSpace(os.Getenv("AGENTDECK_ADMIN_TOKEN"))
	if adminToken == "" {
		adminToken = loadOrCreateToken(filepath.Join(*dataDir, "admin_token"))
	}

	st, err := store.Open(filepath.Join(*dataDir, "agentdeck.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	box, err := secret.Load(filepath.Join(*dataDir, "master_key"))
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	api.New(st, box, adminToken).Register(mux)
	mux.Handle("/", web.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// A machine can die mid-job (reboot, network drop). Don't leave those jobs
	// stuck as 'running' where the console would wait on them forever.
	if err := st.RequeueStaleJobs(context.Background(), 2*time.Hour); err != nil {
		log.Printf("stale job cleanup: %v", err)
	}
	go func() {
		for range time.Tick(30 * time.Minute) {
			if err := st.RequeueStaleJobs(context.Background(), 2*time.Hour); err != nil {
				log.Printf("stale job cleanup: %v", err)
			}
		}
	}()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           logMW(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
	}
	log.Printf("agentdeckd listening on http://%s (data=%s)", *addr, *dataDir)
	log.Fatal(srv.ListenAndServe())
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func loadOrCreateToken(path string) string {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b))
	}
	buf := make([]byte, 24)
	rand.Read(buf)
	tok := "adadmin_" + hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "generated admin token -> %s\n", path)
	return tok
}

func logMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(rw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.code, time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
