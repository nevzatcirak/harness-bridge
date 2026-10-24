package main

import (
	"context"
	"crypto/tls"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/security"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	token, err := readSecret(os.Getenv, "BRIDGE_TOKEN")
	if err != nil {
		log.Fatal(err)
	}
	if len(token) < 32 {
		log.Fatal("BRIDGE_TOKEN must be at least 32 characters")
	}
	base := os.Getenv("OPENCODE_URL")
	if base == "" {
		base = "http://127.0.0.1:4096"
	}
	password, err := readSecret(os.Getenv, "OPENCODE_SERVER_PASSWORD")
	if err != nil {
		log.Fatal(err)
	}
	client, err := opencode.New(base, os.Getenv("OPENCODE_SERVER_USERNAME"), password)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := readIngress(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	policy, err := security.New(strings.Split(os.Getenv("BRIDGE_ALLOWED_ROOTS"), string(os.PathListSeparator)), strings.Split(os.Getenv("BRIDGE_ALLOWED_SESSIONS"), ","))
	if err != nil {
		log.Fatal(err)
	}
	runs := execution.New(client)
	defer runs.Close()
	mux := http.NewServeMux()
	mux.Handle("/mcp", &mcp.Server{Client: client, Runs: runs, Token: token, Policy: policy, Remote: cfg.remote, ReadOnly: cfg.readOnly})
	srv := &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	log.Printf("Bridge listening on %s", cfg.addr)
	var serveErr error
	if cfg.remote != nil {
		serveErr = srv.ListenAndServeTLS(cfg.cert, cfg.key)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if err := serveErr; err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
