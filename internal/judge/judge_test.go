package judge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"codejudge/internal/sandbox"
)

// TestVerdicts judges the programs in testdata/sum and checks that each gets
// the verdict its file name promises. (fork.py is left out: its verdict
// depends on which limit it happens to hit first.)
//
// It needs Docker and the three language images and takes about a minute,
// so `go test -short` skips it.
func TestVerdicts(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs about 20 programs in Docker")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not available:", err)
	}
	// Containers that were slow to go away are removed in the background;
	// wait for them at the end so the test leaves nothing behind.
	t.Cleanup(sandbox.WaitForCleanup)

	tests := []struct {
		file string
		want Verdict
	}{
		{"ac.py", Accepted},
		{"wa.py", WrongAnswer},
		{"ce.py", CompileError},
		{"re.py", RuntimeError},
		{"tle.py", TimeLimitExceeded},
		{"mle.py", MemoryLimitExceeded},
		{"mle_huge.py", MemoryLimitExceeded},
		{"net.py", RuntimeError},
		{"write.py", RuntimeError},

		{"ac.cpp", Accepted},
		{"ce.cpp", CompileError},
		{"re.cpp", RuntimeError},
		{"tle.cpp", TimeLimitExceeded},
		{"mle.cpp", MemoryLimitExceeded},

		{"ac.java", Accepted},
		{"ce.java", CompileError},
		{"re.java", RuntimeError},
		{"tle.java", TimeLimitExceeded},
		{"mle.java", MemoryLimitExceeded},
	}
	languageOf := map[string]string{".py": "python", ".cpp": "cpp", ".java": "java"}
	limits := Limits{Time: 2 * time.Second, MemoryMB: 256}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sum", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			language := languageOf[filepath.Ext(tt.file)]

			res, err := Judge(context.Background(), language, string(source), "2 3\n", "5\n", limits)
			if err != nil {
				t.Fatal(err)
			}
			if res.Verdict != tt.want {
				t.Errorf("verdict = %s, want %s (runtime %d ms)\noutput:\n%s", res.Verdict, tt.want, res.RuntimeMs, res.Stderr)
			}
			t.Logf("%s in %d ms", res.Verdict, res.RuntimeMs)
		})
	}
}
