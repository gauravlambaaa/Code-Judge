package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests start real containers, so they need Docker and the
// python:3.13-slim image. They're skipped if Docker isn't available.

const pythonImage = "python:3.13-slim"

// runPython writes source to a fresh directory and runs it in the sandbox.
func runPython(t *testing.T, source string, timeLimit time.Duration, memoryMB int) Result {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not available:", err)
	}
	dir := t.TempDir()
	// The container runs as "nobody", which must be able to read the code.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Spec{
		Image:     pythonImage,
		Cmd:       []string{"python3", "/sandbox/main.py"},
		Dir:       dir,
		TimeLimit: timeLimit,
		MemoryMB:  memoryMB,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The program reads its own cgroup limit files from inside the container,
// so this checks the limits the kernel is actually enforcing.
func TestLimitsAreApplied(t *testing.T) {
	res := runPython(t, `
import os
for f in ["memory.max", "memory.swap.max", "cpu.max", "pids.max"]:
    print(f, open("/sys/fs/cgroup/" + f).read().strip())
print("uid", os.getuid())
print("net", sorted(os.listdir("/sys/class/net")))
`, 5*time.Second, 128)
	t.Logf("what the program saw from inside the container:\n%s", res.Stdout)

	want := []string{
		"memory.max 134217728",  // 128 MB in bytes
		"memory.swap.max 0",     // no swap
		"cpu.max 100000 100000", // 100 ms of CPU per 100 ms: one core
		"pids.max 64",
		"uid 65534",  // nobody
		"net ['lo']", // only loopback: no network
	}
	for _, w := range want {
		if !strings.Contains(res.Stdout, w) {
			t.Errorf("stdout is missing %q\nstdout:\n%s\nstderr:\n%s", w, res.Stdout, res.Stderr)
		}
	}
}

func TestInfiniteLoopIsKilled(t *testing.T) {
	start := time.Now()
	res := runPython(t, "while True: pass\n", 1*time.Second, 128)
	if !res.Killed {
		t.Errorf("Killed = false, want true")
	}
	if res.Elapsed < 1*time.Second {
		t.Errorf("Elapsed = %v, want more than the 1 s limit", res.Elapsed)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("Run took %v; the deadline kill didn't work", took)
	}
}

func TestMemoryBombIsOOMKilled(t *testing.T) {
	res := runPython(t, `
chunks = []
while True:
    chunks.append(b"x" * (10 * 1024 * 1024))
`, 10*time.Second, 64)
	if !res.OOMKilled {
		t.Errorf("OOMKilled = false, want true (exit code %d, stderr %q)", res.ExitCode, res.Stderr)
	}
	if res.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137 (128 + SIGKILL)", res.ExitCode)
	}
}

func TestElapsedIsTheProgramsOwnTime(t *testing.T) {
	res := runPython(t, "import time; time.sleep(0.5)\n", 5*time.Second, 128)
	// Container startup is several hundred ms. If it were included, this
	// would be well over 0.7 s.
	if res.Elapsed < 500*time.Millisecond || res.Elapsed > 700*time.Millisecond {
		t.Errorf("Elapsed = %v, want about 500 ms", res.Elapsed)
	}
}

func TestOutputIsCapped(t *testing.T) {
	res := runPython(t, "import sys\nwhile True: sys.stdout.write('x' * 65536)\n", 1*time.Second, 128)
	if len(res.Stdout) != maxOutput {
		t.Errorf("len(Stdout) = %d, want %d", len(res.Stdout), maxOutput)
	}
}

// A Python fork bomb fills the memory limit with 64 processes and thrashes,
// which can make the container take 20+ seconds to die. Run must still
// return soon after the deadline instead of waiting for it.
func TestForkBombDoesNotHoldTheWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: waits for a thrashing container to be cleaned up")
	}
	// Run returns quickly, but the container may still be dying in the
	// background. Wait for it at the end so the test leaves nothing behind.
	t.Cleanup(WaitForCleanup)

	start := time.Now()
	runPython(t, "import os\nwhile True:\n    os.fork()\n", 1*time.Second, 256)
	limit := 1*time.Second + startupGrace + killWait + 3*time.Second // 3 s of slack for Docker itself
	if took := time.Since(start); took > limit {
		t.Errorf("Run took %v, want at most %v", took, limit)
	}
}
