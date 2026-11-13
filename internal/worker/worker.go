// Package worker takes submissions off the queue and judges them.
package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"codejudge/internal/judge"
	"codejudge/internal/store"
)

type Worker struct {
	store        *store.Store
	id           string
	pollInterval time.Duration
}

func New(st *store.Store, id string) *Worker {
	return &Worker{store: st, id: id, pollInterval: time.Second}
}

// DefaultID names a worker "<hostname>-<pid>". That's unique for every worker
// process on one machine, and easy to read in the worker_id column.
func DefaultID() string {
	host, err := os.Hostname()
	if err != nil {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// Run claims and judges submissions one at a time until ctx is cancelled.
// When the queue is empty it waits pollInterval before checking again.
func (w *Worker) Run(ctx context.Context) {
	log.Printf("worker %s: started", w.id)
	for ctx.Err() == nil {
		job, err := w.store.ClaimNextPending(ctx, w.id)
		if err != nil {
			log.Printf("worker %s: claim failed: %v", w.id, err)
			sleep(ctx, w.pollInterval)
			continue
		}
		if job == nil {
			sleep(ctx, w.pollInterval) // queue is empty
			continue
		}
		w.process(ctx, job)
	}
	log.Printf("worker %s: stopped", w.id)
}

func (w *Worker) process(ctx context.Context, job *store.Job) {
	log.Printf("worker %s: judging %s (%s)", w.id, job.ID, job.Language)
	if err := w.store.RecordClaim(ctx, job.ID, w.id); err != nil {
		log.Printf("worker %s: recording claim of %s failed: %v", w.id, job.ID, err)
	}

	// If anything below fails, the submission stays 'running' forever,
	// because nothing ever looks at it again. Step 5 (leases) fixes that.
	problem, tests, err := w.store.LoadProblem(ctx, job.ProblemID)
	if err != nil {
		log.Printf("worker %s: loading problem for %s failed: %v", w.id, job.ID, err)
		return
	}
	if len(tests) == 0 {
		log.Printf("worker %s: problem %s has no test cases", w.id, job.ProblemID)
		return
	}

	// Step 1 judges only the first test case. Step 6 runs all of them.
	tc := tests[0]
	limits := judge.Limits{
		Time:     time.Duration(problem.TimeLimitMs) * time.Millisecond,
		MemoryMB: problem.MemoryLimitMb,
	}

	res, err := judge.Judge(ctx, job.Language, job.SourceCode, tc.Input, tc.ExpectedOutput, limits)
	if err != nil {
		log.Printf("worker %s: judging %s failed: %v", w.id, job.ID, err)
		return
	}
	if res.Verdict == judge.RuntimeError || res.Verdict == judge.CompileError {
		log.Printf("worker %s: %s %s output:\n%s", w.id, job.ID, res.Verdict, res.Stderr)
	}

	if err := w.store.FinishSubmission(ctx, job.ID, string(res.Verdict), res.RuntimeMs); err != nil {
		log.Printf("worker %s: saving verdict for %s failed: %v", w.id, job.ID, err)
		return
	}
	log.Printf("worker %s: %s -> %s (%d ms)", w.id, job.ID, res.Verdict, res.RuntimeMs)
}

// sleep waits for d, or returns early if ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
