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

type Store struct {
	pool *pgxpool.Pool

	// ClaimDelay pauses ClaimNextPending between finding a job and marking
	// it running. It's zero normally; the Step 4 race demo sets it (via
	// CLAIM_DELAY_MS) to make the race window wide enough to see.
	ClaimDelay time.Duration
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

// ClaimNextPending takes the oldest pending submission and marks it as
// running under workerID. It returns nil when the queue is empty.
//
// STEP 1 VERSION: this is only correct while there is a single worker.
// The SELECT and the UPDATE are two separate statements with nothing locking
// the row in between, so two workers can both find the same pending job
// before either marks it running. Step 4: you rewrite this function.
func (s *Store) ClaimNextPending(ctx context.Context, workerID string) (*Job, error) {
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

	_, err = s.pool.Exec(ctx,
		`UPDATE submissions
		 SET status = 'running', worker_id = $2, updated_at = now()
		 WHERE id = $1`,
		j.ID, workerID,
	)
	if err != nil {
		return nil, err
	}
	return &j, nil
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

// FinishSubmission records the verdict and marks the submission done.
func (s *Store) FinishSubmission(ctx context.Context, id, verdict string, runtimeMs int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE submissions
		 SET status = 'done', verdict = $2, runtime_ms = $3, updated_at = now()
		 WHERE id = $1`,
		id, verdict, runtimeMs,
	)
	return err
}

// isBadReference reports whether err means an id didn't point at a real row:
// either the text wasn't a valid UUID at all (Postgres error 22P02), or no
// row has that id and a foreign key check failed (23503).
func isBadReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "22P02" || pgErr.Code == "23503")
}
