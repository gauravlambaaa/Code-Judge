// Package judge runs a submission against a test case and decides the verdict.
package judge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codejudge/internal/sandbox"
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

// Limits are a problem's resource limits.
type Limits struct {
	Time     time.Duration
	MemoryMB int
}

// language says how to run code written in one language.
type language struct {
	image    string   // Docker image with the interpreter or compiler
	fileName string   // what the source file has to be called
	run      []string // the command that runs the program inside the container
	// oomMessage is what the language prints to stderr when it refuses an
	// allocation itself, before the kernel has to step in. That's still MLE.
	oomMessage string
}

// Step 2 still only runs Python. Step 3 adds C++ and Java.
var languages = map[string]language{
	"python": {
		image:      "python:3.13-slim",
		fileName:   "main.py",
		run:        []string{"python3", "/sandbox/main.py"},
		oomMessage: "MemoryError",
	},
}

// Supported reports whether the judge can run a language.
func Supported(language string) bool {
	_, ok := languages[language]
	return ok
}

// Judge runs source on one test case inside the sandbox and classifies what
// happened.
//
// It returns an error only when the judge itself failed: Docker is down, a
// temp file couldn't be written, the worker is shutting down. Anything the
// submitted program does wrong (wrong output, crashing, running too long,
// using too much memory) is a verdict, not an error. The difference starts to
// matter in Step 5: an error means "this job should be tried again", a
// verdict means "this job is finished".
func Judge(ctx context.Context, language, source, input, expected string, limits Limits) (Result, error) {
	lang, ok := languages[language]
	if !ok {
		return Result{}, fmt.Errorf("language %q is not supported", language)
	}

	dir, err := os.MkdirTemp("", "submission-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	// MkdirTemp makes the directory private to us (mode 0700), but the
	// container runs as "nobody", which needs to be able to read the code.
	if err := os.Chmod(dir, 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, lang.fileName), []byte(source), 0o644); err != nil {
		return Result{}, err
	}

	run, err := sandbox.Run(ctx, sandbox.Spec{
		Image:     lang.image,
		Cmd:       lang.run,
		Dir:       dir,
		Stdin:     input,
		TimeLimit: limits.Time,
		MemoryMB:  limits.MemoryMB,
	})
	if err != nil {
		return Result{}, err
	}

	res := Result{RuntimeMs: int(run.Elapsed.Milliseconds()), Stderr: run.Stderr}
	// The order matters. First, the reasons the program was *stopped*
	// (time, then memory), and only then whether it crashed on its own:
	// a program killed for either limit also exits with a non-zero code
	// (137), which would otherwise look like an RE. Time comes before
	// memory because if it was already over the time limit when it ran
	// out of memory, the time limit is the one it broke first.
	switch {
	case run.Killed || run.Elapsed > limits.Time:
		res.Verdict = TimeLimitExceeded
	case run.OOMKilled || (run.ExitCode != 0 && strings.Contains(run.Stderr, lang.oomMessage)):
		res.Verdict = MemoryLimitExceeded
	case run.ExitCode != 0:
		res.Verdict = RuntimeError
	case !outputsMatch(run.Stdout, expected):
		res.Verdict = WrongAnswer
	default:
		res.Verdict = Accepted
	}
	return res, nil
}
