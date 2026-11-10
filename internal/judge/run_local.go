package judge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runResult is what we saw when we ran a program once.
type runResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Elapsed  time.Duration
}

// runPython runs source with this machine's python3, feeding input on stdin.
//
// STEP 1 ONLY: this runs untrusted code directly on your machine, with no
// limits on memory, files or network. Step 2 replaces it with a Docker
// container.
func runPython(ctx context.Context, source, input string, timeLimit time.Duration) (runResult, error) {
	dir, err := os.MkdirTemp("", "submission-*")
	if err != nil {
		return runResult{}, err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "main.py")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		return runResult{}, err
	}

	// runCtx is cancelled once the time limit passes. exec.CommandContext
	// watches it, and when it's cancelled it kills the process (SIGKILL).
	runCtx, cancel := context.WithTimeout(ctx, timeLimit)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "python3", path)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Killing python3 doesn't kill any child processes it started, and those
	// children can keep our stdout pipe open, so Wait would never return.
	// WaitDelay makes Wait stop waiting on the pipes 500 ms after the kill.
	cmd.WaitDelay = 500 * time.Millisecond

	start := time.Now()
	err = cmd.Run()
	res := runResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Elapsed: time.Since(start),
	}

	// The worker itself is shutting down. That isn't the program's fault,
	// so it's an error rather than a verdict.
	if ctx.Err() != nil {
		return runResult{}, ctx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		// The program never ran: python3 is missing, the OS refused to
		// start the process, and so on.
		return runResult{}, err
	}
	return res, nil
}
