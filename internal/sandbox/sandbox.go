// Package sandbox runs one command inside a locked-down Docker container
// and reports what happened. It knows nothing about verdicts or languages;
// the judge package decides what the result means.
package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// The container is killed at TimeLimit + startupGrace, measured from when
	// we run `docker run`. Creating and starting a container takes a few
	// hundred milliseconds, which shouldn't count against the program. We
	// decide TLE from the program's real running time (Result.Elapsed), so
	// this grace only controls when we stop waiting.
	startupGrace = 2 * time.Second

	// After we ask Docker to kill a container, we wait at most this long for
	// it to actually stop. A container thrashing at its memory limit (a
	// Python fork bomb does this) can take 20+ seconds just to die, and the
	// worker must never be stuck that long on one submission.
	killWait = 5 * time.Second

	// We keep at most this much stdout or stderr. Without a cap, a program
	// running `while True: print(1)` would fill the worker's memory.
	maxOutput = 1 << 20 // 1 MB
)

// backgroundRemovals counts containers still being removed in the
// background, so the program can wait for them before it exits.
var backgroundRemovals sync.WaitGroup

// WaitForCleanup blocks until every background container removal has
// finished. Call it before the program exits, so no containers are left
// behind.
func WaitForCleanup() {
	backgroundRemovals.Wait()
}

// Spec describes one program run.
type Spec struct {
	Image     string        // Docker image, e.g. "python:3.13-slim"
	Cmd       []string      // command to run inside the container
	Dir       string        // host directory, mounted at /sandbox
	Writable  bool          // mount Dir read-write (compilers need to write their output); otherwise read-only
	Stdin     string        // fed to the program on stdin
	TimeLimit time.Duration // how long the program may run
	MemoryMB  int           // memory limit; going over gets the program killed
}

// Result is what we observed. Interpreting it is the judge's job.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Elapsed   time.Duration // the program's running time, from Docker's own start/finish timestamps (or our own clock if it wouldn't die)
	Killed    bool          // we killed it because it was still running at the deadline
	OOMKilled bool          // the kernel killed it for going over the memory limit
}

// Run starts a container for spec, waits for it to finish (or kills it at
// the deadline), and removes it.
//
// It returns an error only when the sandbox itself failed: Docker isn't
// running, the image is missing, the worker is shutting down. Anything the
// program does wrong shows up in the Result instead.
func Run(ctx context.Context, spec Spec) (Result, error) {
	name, err := containerName()
	if err != nil {
		return Result{}, err
	}

	mount := spec.Dir + ":/sandbox:ro" // the code, read-only...
	if spec.Writable {
		mount = spec.Dir + ":/sandbox:rw" // ...unless a compiler needs to write next to it
	}

	args := []string{
		"run",
		"--name", name, // so we can kill, inspect and remove this exact container
		"-i", // keep stdin open so the test input reaches the program

		// Resource limits, enforced by the kernel through cgroups.
		fmt.Sprintf("--memory=%dm", spec.MemoryMB),
		fmt.Sprintf("--memory-swap=%dm", spec.MemoryMB), // same as --memory: no swap at all
		"--cpus=1",        // at most one CPU core's worth of time
		"--pids-limit=64", // at most 64 processes: stops fork bombs

		// Isolation.
		"--network=none",                   // no network interfaces except loopback
		"--read-only",                      // the container's filesystem can't be changed...
		"--tmpfs=/tmp:rw,size=16m",         // ...except a small scratch /tmp held in memory
		"--user=65534:65534",               // run as "nobody", not root
		"--cap-drop=ALL",                   // drop every special root power
		"--security-opt=no-new-privileges", // and never gain new ones (e.g. via setuid binaries)

		// Docker normally saves all output to a log file on disk too. We read
		// it directly, so turn that off; otherwise a print loop fills the disk.
		"--log-driver=none",

		"-v", mount,
		"-w", "/sandbox",
		spec.Image,
	}
	args = append(args, spec.Cmd...)

	// However we leave this function, the container gets removed. Normally
	// that takes a few milliseconds and happens right here. If the container
	// refused to die in time (see killWait), or Docker is too busy to answer
	// (it slows right down while a fork bomb is dying), we keep retrying in
	// the background so the worker can move on to the next job.
	removeInBackground := false
	defer func() {
		if removeInBackground || removeContainer(name, 10*time.Second) != nil {
			// WaitGroup.Go starts the goroutine and counts it until it returns.
			backgroundRemovals.Go(func() {
				if err := removeContainer(name, 5*time.Minute); err != nil {
					log.Printf("sandbox: could not remove container %s (%v); remove it with: docker rm -f %s", name, err, name)
				}
			})
		}
	}()

	// Note: exec.Command, not exec.CommandContext. Killing this process only
	// kills the docker CLI on our side; the container would keep running.
	// To stop the program we have to ask Docker to kill the container.
	cmd := exec.Command("docker", args...)
	cmd.Stdin = strings.NewReader(spec.Stdin)
	stdout := &cappedBuffer{max: maxOutput}
	stderr := &cappedBuffer{max: maxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start docker: %w", err)
	}
	// cmd.Wait blocks until the container exits, so run it in a goroutine
	// and use select to wait for whichever comes first: the program
	// finishing, the deadline, or the worker shutting down.
	exited := make(chan struct{})
	go func() {
		cmd.Wait() // the exit code we want comes from docker inspect below
		close(exited)
	}()

	deadline := time.NewTimer(spec.TimeLimit + startupGrace)
	defer deadline.Stop()

	var res Result
	select {
	case <-exited:
	case <-deadline.C:
		res.Killed = true
		if !killAndWait(name, exited) {
			// Still not dead. It's over the time limit whatever happens next,
			// so report that now and leave the cleanup to the background.
			// (We don't read stdout/stderr here: the docker CLI may still be
			// writing into them.)
			removeInBackground = true
			res.Elapsed = time.Since(start)
			return res, nil
		}
	case <-ctx.Done():
		killAndWait(name, exited)
		return Result{}, ctx.Err()
	}

	state, err := inspect(name)
	if err != nil {
		return Result{}, fmt.Errorf("inspect container: %w (docker said: %s)", err, stderr.String())
	}
	if state.StartedAt.IsZero() {
		// The container was created but the program never started: a bad
		// image, a bad command, a Docker problem. That's our fault, not the
		// submission's.
		return Result{}, fmt.Errorf("container never started (docker said: %s)", stderr.String())
	}

	// Docker hears "the process exited" and "the kernel killed it for using
	// too much memory" as two separate events, and they can arrive in either
	// order. If the exit arrives first, OOMKilled is still false right now.
	// So if the program died from SIGKILL (exit code 137 = 128 + 9) and we
	// weren't the ones who sent it, give the OOM event up to a second to land.
	for tries := 0; state.ExitCode == 137 && !state.OOMKilled && !res.Killed && tries < 10; tries++ {
		time.Sleep(100 * time.Millisecond)
		if state, err = inspect(name); err != nil {
			return Result{}, fmt.Errorf("inspect container: %w", err)
		}
	}

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	res.ExitCode = state.ExitCode
	res.OOMKilled = state.OOMKilled
	res.Elapsed = state.FinishedAt.Sub(state.StartedAt)
	return res, nil
}

// containerState is the part of `docker inspect` output we need.
type containerState struct {
	ExitCode   int
	OOMKilled  bool
	StartedAt  time.Time
	FinishedAt time.Time
}

func inspect(name string) (containerState, error) {
	out, err := exec.Command("docker", "inspect", "--format", "{{json .State}}", name).Output()
	if err != nil {
		return containerState{}, err
	}
	var s containerState
	if err := json.Unmarshal(out, &s); err != nil {
		return containerState{}, err
	}
	return s, nil
}

// killAndWait asks Docker to kill the container, then waits up to killWait
// for it to stop. It reports whether it stopped in time.
func killAndWait(name string, exited <-chan struct{}) bool {
	// `docker kill` itself can take many seconds on a thrashing container,
	// so run it in the background and wait on the container instead.
	go killContainer(name)
	select {
	case <-exited:
		return true
	case <-time.After(killWait):
		return false
	}
}

// killContainer sends SIGKILL to everything in the container. An error just
// means it had already stopped, so it's ignored.
func killContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	exec.CommandContext(ctx, "docker", "kill", name).Run()
}

// removeContainer force-removes the container (killing it first if needed),
// retrying until it's gone or patience runs out. One attempt isn't enough:
// Docker can take a long time to answer while another container is
// thrashing. It returns nil once the container is gone, or the last error.
func removeContainer(name string, patience time.Duration) error {
	deadline := time.Now().Add(patience)
	for {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput()
		cancel()
		if err == nil || strings.Contains(string(out), "No such container") {
			return nil
		}
		if time.Until(deadline) <= 0 {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func containerName() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "judge-" + hex.EncodeToString(b), nil
}

// cappedBuffer keeps the first max bytes written to it and quietly drops
// the rest. It always reports a full write, so the program never blocks or
// fails because we stopped keeping its output.
type cappedBuffer struct {
	buf strings.Builder
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}
