// Command api serves the HTTP API: create problems, accept submissions,
// report results.
//
// It doesn't judge anything. Since Step 4, judging happens in separate
// worker processes (cmd/worker), so you can run as many as you need without
// touching the API.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"codejudge/internal/api"
	"codejudge/internal/store"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is not set. From the project folder, run: set -a; source .env.example; set +a")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	// ctx is cancelled when you press Ctrl+C (SIGINT) or the process gets
	// SIGTERM, which starts a clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A pool keeps several database connections open and hands one to each
	// query, so concurrent requests don't queue up behind one connection.
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	srv := &http.Server{Addr: addr, Handler: api.New(store.New(pool)).Routes()}
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		// Stop accepting new requests and give in-flight ones 5 s to finish.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
		close(shutdownDone)
	}()

	log.Printf("api: listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("api: %v", err)
	}
	<-shutdownDone
	log.Print("api: stopped")
}
