// Package worker takes submissions off the queue and judges them.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"codejudge/internal/judge"
	"codejudge/internal/sandbox"
	"codejudge/internal/store"
)

// MaxAttempts is how many times a job may be claimed. If every one of those
// workers vanishes mid-job (the job crashes whatever runs it, say), the job
// is marked 'failed' instead of being retried forever.
const MaxAttempts = 3

type Worker struct {
	store        *store.Store
	id           string
	pollInterval time.Duration

	// lease is how long a claim lasts without a heartbeat. heartbeatEvery
	// is a third of it, so two heartbeats in a row can fail (a database
	// blip, a slow moment) before the lease actually runs out.
	lease          time.Duration
	heartbeatEvery time.Duration

	lastOrphanSweep time.Time
}

func New(st *store.Store, id string, lease time.Duration) *Worker {
	return &Worker{
		store:          st,
		id:             id,
		pollInterval:   time.Second,
		lease:          lease,
		heartbeatEvery: lease / 3,
	}
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
// When there's nothing to claim it waits pollInterval before checking again.
func (w *Worker) Run(ctx context.Context) {
	log.Printf("worker %s: started (lease %v, heartbeat every %v)", w.id, w.lease, w.heartbeatEvery)
	for ctx.Err() == nil {
		w.cleanUpAfterDeadWorkers(ctx)

		job, err := w.store.ClaimNextPending(ctx, w.id, w.lease, MaxAttempts)
		if err != nil {
			log.Printf("worker %s: claim failed: %v", w.id, err)
			sleep(ctx, w.pollInterval)
			continue
		}
		if job == nil {
			sleep(ctx, w.pollInterval) // nothing to do
			continue
		}
		w.process(ctx, job)
	}
	log.Printf("worker %s: stopped", w.id)
}

// cleanUpAfterDeadWorkers is housekeeping that any worker can do, so that a
// worker that crashes never leaves a mess behind forever.
func (w *Worker) cleanUpAfterDeadWorkers(ctx context.Context) {
	// Jobs whose last allowed attempt also died: give up on them.
	if n, err := w.store.FailExhausted(ctx, MaxAttempts); err != nil {
		log.Printf("worker %s: marking exhausted jobs failed: %v", w.id, err)
	} else if n > 0 {
		log.Printf("worker %s: marked %d job(s) failed after %d attempts", w.id, n, MaxAttempts)
	}

	// Containers whose worker died before it could remove them. Once a
	// minute is plenty.
	if time.Since(w.lastOrphanSweep) < time.Minute {
		return
	}
	w.lastOrphanSweep = time.Now()
	if n, err := sandbox.RemoveOrphans(2 * time.Minute); err != nil {
		log.Printf("worker %s: removing orphaned containers: %v", w.id, err)
	} else if n > 0 {
		log.Printf("worker %s: removed %d orphaned container(s)", w.id, n)
	}
}

func (w *Worker) process(ctx context.Context, job *store.Job) {
	log.Printf("worker %s: judging %s (%s, attempt %d of %d)", w.id, job.ID, job.Language, job.Attempt, MaxAttempts)
	if err := w.store.RecordClaim(ctx, job.ID, w.id); err != nil {
		log.Printf("worker %s: recording claim of %s failed: %v", w.id, job.ID, err)
	}

	// jobCtx is the context for judging this one job. The heartbeat cancels
	// it if it finds we've lost the lease, which stops the judging (the
	// sandbox kills the container) instead of wasting time on a job that
	// isn't ours any more.
	jobCtx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()

	// The heartbeat runs alongside the judging, in its own goroutine.
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.heartbeat(jobCtx, job.ID, cancelJob)
	}()

	res, err := w.judgeJob(jobCtx, job)

	// Judging is over: stop the heartbeat, and wait until it has stopped.
	cancelJob()
	<-heartbeatDone

	if err != nil {
		// Our side failed (Docker trouble, lost lease, shutting down), not
		// the submission. Save nothing. We've stopped renewing the lease,
		// so in at most one lease the job can be claimed again, by any
		// worker, the same as if this worker had crashed.
		log.Printf("worker %s: judging %s failed, leaving it to be retried: %v", w.id, job.ID, err)
		return
	}
	if res.Verdict == judge.RuntimeError || res.Verdict == judge.CompileError {
		log.Printf("worker %s: %s %s output:\n%s", w.id, job.ID, res.Verdict, res.Stderr)
	}

	tests := make([]store.TestResult, len(res.Tests))
	for i, t := range res.Tests {
		tests[i] = store.TestResult{Number: t.Number, Verdict: string(t.Verdict), RuntimeMs: t.RuntimeMs}
	}
	err = w.store.FinishSubmission(ctx, job.ID, w.id, string(res.Verdict), res.RuntimeMs,
		clean(res.Stderr, 4<<10), clean(res.Stdout, 64<<10), tests)
	if errors.Is(err, store.ErrLeaseLost) {
		log.Printf("worker %s: %s is no longer ours (our lease ran out and another worker took it); throwing our result away", w.id, job.ID)
		return
	}
	if err != nil {
		log.Printf("worker %s: saving verdict for %s failed: %v", w.id, job.ID, err)
		return
	}
	if job.Mode == "run" {
		log.Printf("worker %s: %s -> %s (custom-input run, %d ms)", w.id, job.ID, res.Verdict, res.RuntimeMs)
	} else if n := res.FailedTest(); n > 0 {
		log.Printf("worker %s: %s -> %s on test %d (%d ms)", w.id, job.ID, res.Verdict, n, res.RuntimeMs)
	} else {
		log.Printf("worker %s: %s -> %s (%d tests, %d ms)", w.id, job.ID, res.Verdict, len(res.Tests), res.RuntimeMs)
	}
}

// heartbeat renews the job's lease every heartbeatEvery until ctx is
// cancelled. If a renewal finds the job is no longer ours, it calls lost()
// to stop the judging.
func (w *Worker) heartbeat(ctx context.Context, jobID string, lost func()) {
	ticker := time.NewTicker(w.heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		ours, err := w.store.ExtendLease(ctx, jobID, w.id, w.lease)
		switch {
		case ctx.Err() != nil:
			return // judging finished while we were renewing
		case err != nil:
			// Probably a database blip. Keep going: the lease has room for
			// a couple of missed heartbeats.
			log.Printf("worker %s: heartbeat for %s failed: %v", w.id, jobID, err)
		case !ours:
			log.Printf("worker %s: lost the lease on %s; stopping", w.id, jobID)
			lost()
			return
		}
	}
}

// defaultLimits apply to Playground runs, which belong to no problem.
var defaultLimits = judge.Limits{Time: 2 * time.Second, MemoryMB: 256}

// judgeJob either judges the code against every test case of its problem (a
// normal submission) or runs it once on the user's own input (a "run").
// Both use the problem's time and memory limits; a Playground run has no
// problem and uses defaultLimits.
func (w *Worker) judgeJob(ctx context.Context, job *store.Job) (judge.Result, error) {
	limits := defaultLimits
	var stored []store.TestCase
	if job.ProblemID != "" {
		problem, tcs, err := w.store.LoadProblem(ctx, job.ProblemID)
		if err != nil {
			return judge.Result{}, fmt.Errorf("loading problem: %w", err)
		}
		limits = judge.Limits{
			Time:     time.Duration(problem.TimeLimitMs) * time.Millisecond,
			MemoryMB: problem.MemoryLimitMb,
		}
		stored = tcs
	}

	if job.Mode == "run" {
		return judge.Run(ctx, job.Language, job.SourceCode, job.Input, limits)
	}

	if len(stored) == 0 {
		return judge.Result{}, fmt.Errorf("problem %s has no test cases", job.ProblemID)
	}
	tests := make([]judge.TestCase, len(stored))
	for i, tc := range stored {
		tests[i] = judge.TestCase{Input: tc.Input, Expected: tc.ExpectedOutput}
	}
	return judge.Judge(ctx, job.Language, job.SourceCode, tests, limits)
}

// clean turns the compiler's or program's output into something safe to
// store and show: at most limit bytes, and only valid UTF-8 with no NUL
// bytes. A program can print any bytes it likes, and Postgres text columns
// reject both of those, which would make saving the verdict fail.
func clean(s string, limit int) string {
	if len(s) > limit {
		s = s[:limit] + "\n... (cut)"
	}
	s = strings.ToValidUTF8(s, "?")
	return strings.ReplaceAll(s, "\x00", "")
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
