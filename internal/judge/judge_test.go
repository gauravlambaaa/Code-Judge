package judge

import (
	"context"
	"encoding/json"
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
		{"ce_bomb.cpp", CompileError},
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

			oneTest := []TestCase{{Input: "2 3\n", Expected: "5\n"}}
			res, err := Judge(context.Background(), language, string(source), oneTest, limits)
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

// TestFirstFailureWins judges against all of the sum problem's test cases
// (testdata/sum/problem.json) and checks that tests run in order, that the
// first failing test decides the verdict, and that judging stops there.
func TestFirstFailureWins(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs programs in Docker")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not available:", err)
	}
	t.Cleanup(sandbox.WaitForCleanup)

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sum", "problem.json"))
	if err != nil {
		t.Fatal(err)
	}
	var problem struct {
		TestCases []struct {
			Input          string `json:"input"`
			ExpectedOutput string `json:"expected_output"`
		} `json:"test_cases"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatal(err)
	}
	var cases []TestCase
	for _, tc := range problem.TestCases {
		cases = append(cases, TestCase{Input: tc.Input, Expected: tc.ExpectedOutput})
	}
	if len(cases) != 3 {
		t.Fatalf("problem.json has %d test cases; this test expects 3", len(cases))
	}

	tests := []struct {
		file, language string
		want           Verdict
		wantPerTest    []Verdict // one per test that should have run
	}{
		{"ac.cpp", "cpp", Accepted, []Verdict{Accepted, Accepted, Accepted}},
		{"wa_overflow.cpp", "cpp", WrongAnswer, []Verdict{Accepted, Accepted, WrongAnswer}},
		{"tle.py", "python", TimeLimitExceeded, []Verdict{TimeLimitExceeded}}, // stops after test 1
		{"ce.cpp", "cpp", CompileError, nil},                                  // no test ever runs
	}
	limits := Limits{Time: 2 * time.Second, MemoryMB: 256}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sum", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			res, err := Judge(context.Background(), tt.language, string(source), cases, limits)
			if err != nil {
				t.Fatal(err)
			}
			var got []Verdict
			for _, r := range res.Tests {
				got = append(got, r.Verdict)
			}
			if res.Verdict != tt.want || len(got) != len(tt.wantPerTest) {
				t.Fatalf("verdict %s with per-test %v, want %s with %v", res.Verdict, got, tt.want, tt.wantPerTest)
			}
			for i := range got {
				if got[i] != tt.wantPerTest[i] || res.Tests[i].Number != i+1 {
					t.Errorf("test %d: got %s (number %d), want %s", i+1, got[i], res.Tests[i].Number, tt.wantPerTest[i])
				}
			}
			t.Logf("%s, failed test %d, per test %v", res.Verdict, res.FailedTest(), got)
		})
	}
}
