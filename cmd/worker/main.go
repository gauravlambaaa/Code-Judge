// Command worker takes submissions off the queue and judges them, one at a
// time, until you press Ctrl+C.
//
// Run as many copies as you like. Each is a separate process with its own id
// ("<hostname>-<pid>"), and the claim query in internal/store is what makes
// sure no two of them ever take the same submission.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"codejudge/internal/sandbox"
	"codejudge/internal/store"
	"codejudge/internal/worker"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is not set. From the project folder, run: set -a; source .env.example; set +a")
	}

	// ctx is cancelled when you press Ctrl+C (SIGINT) or the process gets
	// SIGTERM, which tells the worker loop to stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	st := store.New(pool)

	// Step 4 race demo only: CLAIM_DELAY_MS=200 makes every claim pause
	// between "find the oldest pending job" and "mark it running". That
	// widens the window in which two workers can grab the same job, so the
	// race is easy to see instead of happening once in a blue moon.
	if v := os.Getenv("CLAIM_DELAY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil {
			log.Fatalf("CLAIM_DELAY_MS: %v", err)
		}
		st.ClaimDelay = time.Duration(ms) * time.Millisecond
		log.Printf("worker: pausing %v inside every claim (race demo)", st.ClaimDelay)
	}

	worker.New(st, worker.DefaultID()).Run(ctx)

	// Containers that were slow to die are removed in the background; wait
	// for those so none are left behind.
	sandbox.WaitForCleanup()
}
