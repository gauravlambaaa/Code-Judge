// Command api serves the HTTP API.
//
// In Steps 1-3 it also runs the worker, in a background goroutine. Step 4
// moves the worker into its own program (cmd/worker) so several can run at
// once.
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
	"codejudge/internal/worker"
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
	// SIGTERM. Everything below watches it, so the program can stop cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A pool keeps several database connections open and hands one to each
	// query, so the HTTP handlers and the worker don't queue up behind a
	// single connection.
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	st := store.New(pool)

	w := worker.New(st, worker.DefaultID())
	workerDone := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(workerDone)
	}()

	srv := &http.Server{Addr: addr, Handler: api.New(st).Routes()}
	go func() {
		<-ctx.Done()
		// Stop accepting new requests and give in-flight ones 5 s to finish.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("api: listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("api: %v", err)
	}
	<-workerDone
	log.Print("api: stopped")
}
