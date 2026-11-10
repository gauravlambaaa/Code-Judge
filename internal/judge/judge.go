// Package judge runs a submission against a test case and decides the verdict.
package judge

import (
	"context"
	"fmt"
	"time"
)

type Verdict string

const (
	Accepted            Verdict = "AC"
	WrongAnswer         Verdict = "WA"
	TimeLimitExceeded   Verdict = "TLE"
	MemoryLimitExceeded Verdict = "MLE"
	RuntimeError        Verdict = "RE"
	CompileError        Verdict = "CE"
)

// Result is the outcome of judging one test case.
type Result struct {
	Verdict   Verdict
	RuntimeMs int
	Stderr    string // what the program printed to stderr; handy when debugging an RE
}

// Supported reports whether the judge can run a language yet.
// Step 1 only runs Python. Step 3 adds C++ and Java.
func Supported(language string) bool {
	return language == "python"
}

// Judge runs source on one test case and classifies what happened.
//
// It returns an error only when the judge itself failed: it couldn't write a
// temp file, python3 isn't installed, the worker is shutting down. Anything
// the submitted program does wrong (wrong output, crashing, running too long)
// is a verdict, not an error. The difference starts to matter in Step 5: an
// error means "this job should be tried again", a verdict means "this job is
// finished".
func Judge(ctx context.Context, language, source, input, expected string, timeLimit time.Duration) (Result, error) {
	if !Supported(language) {
		return Result{}, fmt.Errorf("language %q is not supported yet", language)
	}

	run, err := runPython(ctx, source, input, timeLimit)
	if err != nil {
		return Result{}, err
	}

	res := Result{RuntimeMs: int(run.Elapsed.Milliseconds()), Stderr: run.Stderr}
	// The order of these checks matters. A program we killed for running too
	// long also has a non-zero exit code, so TLE has to be checked before RE.
	// And output is only worth comparing if the program finished normally.
	switch {
	case run.TimedOut:
		res.Verdict = TimeLimitExceeded
	case run.ExitCode != 0:
		res.Verdict = RuntimeError
	case !outputsMatch(run.Stdout, expected):
		res.Verdict = WrongAnswer
	default:
		res.Verdict = Accepted
	}
	return res, nil
}
