// Package store holds every SQL query the judge runs. Nothing outside this
// package writes SQL, so when the queue logic changes in Steps 4 and 5, this
// is the only file that changes with it.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means an id didn't match any row.
var ErrNotFound = errors.New("not found")

// ErrLeaseLost means a job isn't ours any more: our lease ran out and
// another worker claimed it.
var ErrLeaseLost = errors.New("lease lost: another worker owns this job now")

type Store struct {
	pool *pgxpool.Pool

	// ClaimDelay pauses ClaimNextPending between finding a job and marking
	// it running. It's zero normally; the Step 4 race demo sets it (via
	// CLAIM_DELAY_MS) to make the race window wide enough to see.
	ClaimDelay time.Duration

	// UnsafeClaim switches ClaimNextPending back to the Step 1 version,
	// which has the race. Only for showing the race (UNSAFE_CLAIM=1).
	UnsafeClaim bool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type TestCase struct {
	Input          string
	ExpectedOutput string
}

type Problem struct {
	ID            string
	TimeLimitMs   int
	MemoryLimitMb int
}

// Submission is what a client sees when it polls for a result.
type Submission struct {
	Status    string
	Verdict   *string // nil (SQL NULL) until the submission is judged
	RuntimeMs *int
}

// Job is a submission that a worker has claimed and is about to judge.
type Job struct {
	ID         string
	ProblemID  string
	Language   string
	SourceCode string
	Attempt    int // 1 the first time a job is claimed, 2 if it's being retried, ...
}

// CreateProblem inserts a problem and its test cases in one transaction, so a
// problem can never exist without its tests, or with only some of them.
func (s *Store) CreateProblem(ctx context.Context, title string, timeLimitMs, memoryLimitMb int, tests []TestCase) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	// Rollback after a successful Commit does nothing, so this only undoes
	// the work when we return early because of an error.
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO problems (title, time_limit_ms, memory_limit_mb)
		 VALUES ($1, $2, $3)
		 RETURNING id`,
		title, timeLimitMs, memoryLimitMb,
	).Scan(&id)
	if err != nil {
		return "", err
	}

	for i, tc := range tests {
		_, err = tx.Exec(ctx,
			`INSERT INTO test_cases (problem_id, ordinal, input, expected_output)
			 VALUES ($1, $2, $3, $4)`,
			id, i+1, tc.Input, tc.ExpectedOutput,
		)
		if err != nil {
			return "", err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// CreateSubmission puts a submission on the queue. The new row gets
// status = 'pending' from the column default, and that is what makes workers
// pick it up. Returns ErrNotFound if problemID isn't a real problem.
func (s *Store) CreateSubmission(ctx context.Context, problemID, language, source string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO submissions (problem_id, language, source_code)
		 VALUES ($1, $2, $3)
		 RETURNING id`,
		problemID, language, source,
	).Scan(&id)
	if isBadReference(err) {
		return "", ErrNotFound
	}
	return id, err
}

// GetSubmission returns a submission's current status and verdict.
func (s *Store) GetSubmission(ctx context.Context, id string) (Submission, error) {
	var sub Submission
	err := s.pool.QueryRow(ctx,
		`SELECT status, verdict, runtime_ms
		 FROM submissions
		 WHERE id = $1`,
		id,
	).Scan(&sub.Status, &sub.Verdict, &sub.RuntimeMs)
	if errors.Is(err, pgx.ErrNoRows) || isBadReference(err) {
		return Submission{}, ErrNotFound
	}
	return sub, err
}

// ClaimNextPending claims the oldest job that needs a worker and marks it as
// running under workerID, with a lease that runs out after `lease` unless
// the worker keeps renewing it (ExtendLease). It returns nil when there's
// nothing to claim. It's safe with any number of workers calling it at once.
//
// "Needs a worker" means either:
//   - pending: nobody has claimed it yet, or
//   - running, but its lease has run out: whoever claimed it stopped
//     renewing the lease, so they're presumably dead.
//
// Either way, only if it has been claimed fewer than maxAttempts times.
func (s *Store) ClaimNextPending(ctx context.Context, workerID string, lease time.Duration, maxAttempts int) (*Job, error) {
	if s.UnsafeClaim {
		return s.claimNextPendingUnsafe(ctx, workerID, lease)
	}

	// Everything below happens inside one transaction, on one connection.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// If we return before Commit (an error, or nothing to claim), this ends
	// the transaction and releases any lock we hold. After a Commit it does
	// nothing.
	defer tx.Rollback(ctx)

	var j Job
	// FOR UPDATE: lock the row we pick until this transaction ends.
	// SKIP LOCKED: if another worker has already locked a row, don't wait
	// for it; pass over it and take the next free one.
	// Note tx.QueryRow, not s.pool.QueryRow: the pool would run this on a
	// different connection, outside our transaction.
	// now() is the database's clock, so lease times never depend on whose
	// computer's clock is right.
	err = tx.QueryRow(ctx,
		`SELECT id, problem_id, language, source_code
		 FROM submissions
		 WHERE (status = 'pending'
		        OR (status = 'running' AND lease_expires_at < now()))
		   AND retry_count < $1
		 ORDER BY created_at
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`,
		maxAttempts,
	).Scan(&j.ID, &j.ProblemID, &j.Language, &j.SourceCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // every claimable row is taken or locked by someone else
	}
	if err != nil {
		return nil, err
	}

	// Race demo: the same pause as the unsafe version. Here it's harmless,
	// because we hold the row's lock: every other worker skips this row.
	time.Sleep(s.ClaimDelay)

	// Still inside the transaction, so still holding the lock: mark the job
	// taken, start our lease, and count the attempt. When COMMIT releases
	// the lock a moment later, the row already says 'running' with a fresh
	// lease, so no other worker's WHERE clause can match it.
	err = tx.QueryRow(ctx,
		`UPDATE submissions
		 SET status = 'running',
		     worker_id = $2,
		     lease_expires_at = now() + make_interval(secs => $3),
		     retry_count = retry_count + 1,
		     updated_at = now()
		 WHERE id = $1
		 RETURNING retry_count`,
		j.ID, workerID, lease.Seconds(),
	).Scan(&j.Attempt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &j, nil
}

// claimNextPendingUnsafe is the Step 1 version, kept only so the race can be
// shown side by side with the fix (UNSAFE_CLAIM=1). Never use it for real.
//
// The SELECT and the UPDATE are two separate statements on whatever
// connection the pool hands out, with nothing locking the row in between.
// So two workers can both find the same pending job before either marks it
// running, and both judge it.
func (s *Store) claimNextPendingUnsafe(ctx context.Context, workerID string, lease time.Duration) (*Job, error) {
	var j Job
	err := s.pool.QueryRow(ctx,
		`SELECT id, problem_id, language, source_code
		 FROM submissions
		 WHERE status = 'pending'
		 ORDER BY created_at
		 LIMIT 1`,
	).Scan(&j.ID, &j.ProblemID, &j.Language, &j.SourceCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Race demo: widen the gap between "found it" and "marked it taken".
	// (Zero, so no pause, unless CLAIM_DELAY_MS is set.)
	time.Sleep(s.ClaimDelay)

	err = s.pool.QueryRow(ctx,
		`UPDATE submissions
		 SET status = 'running',
		     worker_id = $2,
		     lease_expires_at = now() + make_interval(secs => $3),
		     retry_count = retry_count + 1,
		     updated_at = now()
		 WHERE id = $1
		 RETURNING retry_count`,
		j.ID, workerID, lease.Seconds(),
	).Scan(&j.Attempt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// ExtendLease pushes a job's lease forward to now + lease: the heartbeat.
// It only does so if the job is still ours (still running under our worker
// id), and reports whether it was. false means our lease ran out at some
// point and another worker has claimed the job since.
func (s *Store) ExtendLease(ctx context.Context, id, workerID string, lease time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE submissions
		 SET lease_expires_at = now() + make_interval(secs => $3), updated_at = now()
		 WHERE id = $1 AND worker_id = $2 AND status = 'running'`,
		id, workerID, lease.Seconds(),
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FailExhausted gives up on jobs that will never finish: their lease ran
// out on their last allowed attempt (they've been claimed maxAttempts times
// and the last worker vanished too). They're marked 'failed' instead of
// being retried forever. Returns how many it marked.
func (s *Store) FailExhausted(ctx context.Context, maxAttempts int) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE submissions
		 SET status = 'failed', lease_expires_at = NULL, updated_at = now()
		 WHERE status = 'running'
		   AND lease_expires_at < now()
		   AND retry_count >= $1`,
		maxAttempts,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RecordClaim logs that workerID claimed a submission, in the claims table.
// Judging doesn't need it: it's evidence. If two workers ever claim the
// same submission, it shows up there as two rows.
func (s *Store) RecordClaim(ctx context.Context, submissionID, workerID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO claims (submission_id, worker_id) VALUES ($1, $2)`,
		submissionID, workerID,
	)
	return err
}

// LoadProblem returns a problem's limits and its test cases, in order.
func (s *Store) LoadProblem(ctx context.Context, problemID string) (Problem, []TestCase, error) {
	p := Problem{ID: problemID}
	err := s.pool.QueryRow(ctx,
		`SELECT time_limit_ms, memory_limit_mb
		 FROM problems
		 WHERE id = $1`,
		problemID,
	).Scan(&p.TimeLimitMs, &p.MemoryLimitMb)
	if err != nil {
		return Problem{}, nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT input, expected_output
		 FROM test_cases
		 WHERE problem_id = $1
		 ORDER BY ordinal`,
		problemID,
	)
	if err != nil {
		return Problem{}, nil, err
	}
	// CollectRows reads every row into a TestCase (columns map to struct
	// fields by position) and closes rows when it's done.
	tests, err := pgx.CollectRows(rows, pgx.RowToStructByPos[TestCase])
	if err != nil {
		return Problem{}, nil, err
	}
	return p, tests, nil
}

// FinishSubmission records the verdict and marks the submission done, but
// only if the job is still ours. If it isn't (our lease ran out and another
// worker claimed it), it saves nothing and returns ErrLeaseLost: the other
// worker's result is the one that counts.
func (s *Store) FinishSubmission(ctx context.Context, id, workerID, verdict string, runtimeMs int) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE submissions
		 SET status = 'done', verdict = $3, runtime_ms = $4,
		     lease_expires_at = NULL, updated_at = now()
		 WHERE id = $1 AND worker_id = $2 AND status = 'running'`,
		id, workerID, verdict, runtimeMs,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// isBadReference reports whether err means an id didn't point at a real row:
// either the text wasn't a valid UUID at all (Postgres error 22P02), or no
// row has that id and a foreign key check failed (23503).
func isBadReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "22P02" || pgErr.Code == "23503")
}
