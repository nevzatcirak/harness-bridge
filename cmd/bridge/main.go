package main

import (
	"context"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/security"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	token := os.Getenv("BRIDGE_TOKEN")
	if len(token) < 32 {
		log.Fatal("BRIDGE_TOKEN must be at least 32 characters")
	}
	base := os.Getenv("OPENCODE_URL")
	if base == "" {
		base = "http://127.0.0.1:4096"
	}
	client, err := opencode.New(base, os.Getenv("OPENCODE_SERVER_USERNAME"), os.Getenv("OPENCODE_SERVER_PASSWORD"))
	if err != nil {
		log.Fatal(err)
	}
	addr := os.Getenv("BRIDGE_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		log.Fatal("BRIDGE_LISTEN must use loopback")
	}
	policy, err := security.New(strings.Split(os.Getenv("BRIDGE_ALLOWED_ROOTS"), string(os.PathListSeparator)), strings.Split(os.Getenv("BRIDGE_ALLOWED_SESSIONS"), ","))
	if err != nil {
		log.Fatal(err)
	}
	runs := execution.New(client)
	defer runs.Close()
	mux := http.NewServeMux()
	mux.Handle("/mcp", &mcp.Server{Client: client, Runs: runs, Token: token, Policy: policy})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	log.Printf("Bridge listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
