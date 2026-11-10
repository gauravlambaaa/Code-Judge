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
	"os/exec"
	"strings"
	"time"
)

const (
	// The container is killed at TimeLimit + startupGrace, measured from when
	// we run `docker run`. Creating and starting a container takes a few
	// hundred milliseconds, which shouldn't count against the program. We
	// decide TLE from the program's real running time (Result.Elapsed), so
	// this grace only controls when we stop waiting.
	startupGrace = 2 * time.Second

	// We keep at most this much stdout or stderr. Without a cap, a program
	// running `while True: print(1)` would fill the worker's memory.
	maxOutput = 1 << 20 // 1 MB
)

// Spec describes one program run.
type Spec struct {
	Image     string        // Docker image, e.g. "python:3.13-slim"
	Cmd       []string      // command to run inside the container
	Dir       string        // host directory, mounted read-only at /sandbox
	Stdin     string        // fed to the program on stdin
	TimeLimit time.Duration // how long the program may run
	MemoryMB  int           // memory limit; going over gets the program killed
}

// Result is what we observed. Interpreting it is the judge's job.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Elapsed   time.Duration // the program's running time, from Docker's own start/finish timestamps
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

		"-v", spec.Dir + ":/sandbox:ro", // the code, read-only
		"-w", "/sandbox",
		spec.Image,
	}
	args = append(args, spec.Cmd...)

	// However we leave this function, the container gets removed.
	defer removeContainer(name)

	// Note: exec.Command, not exec.CommandContext. Killing this process only
	// kills the docker CLI on our side; the container would keep running.
	// To stop the program we have to ask Docker to kill the container.
	cmd := exec.Command("docker", args...)
	cmd.Stdin = strings.NewReader(spec.Stdin)
	stdout := &cappedBuffer{max: maxOutput}
	stderr := &cappedBuffer{max: maxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

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
		killContainer(name)
		<-exited
	case <-ctx.Done():
		killContainer(name)
		<-exited
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

// killContainer sends SIGKILL to everything in the container. An error just
// means it had already stopped, so it's ignored.
func killContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "docker", "kill", name).Run()
}

func removeContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
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
