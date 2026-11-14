// Package judge compiles a submission, runs it against a problem's test
// cases, and decides the verdict.
package judge

import (
	"context"
	"errors"
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

// TestCase is one input, and the output a correct program prints for it.
type TestCase struct {
	Input    string
	Expected string
}

// TestResult is the outcome of one test case.
type TestResult struct {
	Number    int // 1 for the first test case, 2 for the second, ...
	Verdict   Verdict
	RuntimeMs int
}

// Result is the outcome of judging a submission.
type Result struct {
	Verdict   Verdict      // AC if every test passed; otherwise the first failing test's verdict
	RuntimeMs int          // the slowest test's running time
	Stderr    string       // the compiler's errors for a CE, or the failing test's stderr
	Tests     []TestResult // one per test that ran; judging stops at the first failure
}

// FailedTest returns the number of the test that decided a failing verdict,
// or 0 if there isn't one (AC, or a CE where no test ran).
func (r Result) FailedTest() int {
	if r.Verdict == Accepted || len(r.Tests) == 0 {
		return 0
	}
	return r.Tests[len(r.Tests)-1].Number
}

// Limits are a problem's resource limits. The time limit applies to each
// test case separately.
type Limits struct {
	Time     time.Duration
	MemoryMB int
}

// Compiling is untrusted work too: code can be written to make the compiler
// run for ages or eat memory. So it runs in the sandbox as well, with its own
// limits, more generous than a program's because compilers are heavy.
const (
	compileTimeLimit = 15 * time.Second
	compileMemoryMB  = 1024
)

// language says how to compile and run code written in one language.
type language struct {
	image    string   // Docker image with the compiler / interpreter
	fileName string   // what the source file has to be called
	compile  []string // the command that checks/compiles the source; a non-zero exit means CE
	run      []string // the command that runs the program
	// oomMessage is what the language prints to stderr when it refuses an
	// allocation itself, before the kernel has to step in. That's still MLE.
	oomMessage string
}

var languages = map[string]language{
	"cpp": {
		image:    "gcc:14",
		fileName: "main.cpp",
		// -pipe passes data between the compiler's stages through pipes
		// instead of temp files, because /tmp in the sandbox is small.
		compile:    []string{"g++", "-O2", "-std=c++17", "-pipe", "-o", "/sandbox/main", "/sandbox/main.cpp"},
		run:        []string{"/sandbox/main"},
		oomMessage: "std::bad_alloc",
	},
	"java": {
		image: "eclipse-temurin:21-jdk",
		// Java requires a public class to live in a file with the same name,
		// so submissions must use `public class Main`.
		fileName: "Main.java",
		// -XX:-UsePerfData stops the JVM writing a stats file to /tmp, and
		// SerialGC is the simplest garbage collector: one thread, quick start.
		compile: []string{"javac", "-J-XX:-UsePerfData", "-J-XX:+UseSerialGC", "-d", "/sandbox", "/sandbox/Main.java"},
		// By default the JVM caps its heap at 25% of the container's memory,
		// which would turn a 256 MB limit into 64 MB for the program. 75%
		// leaves the rest for the JVM itself.
		run:        []string{"java", "-XX:-UsePerfData", "-XX:+UseSerialGC", "-XX:MaxRAMPercentage=75", "-cp", "/sandbox", "Main"},
		oomMessage: "java.lang.OutOfMemoryError",
	},
	"python": {
		image:    "python:3.13-slim",
		fileName: "main.py",
		// Python has no compiler, but compile() parses the file without
		// running it, so a syntax error is a CE instead of an RE.
		compile:    []string{"python3", "-c", "import sys; compile(open(sys.argv[1]).read(), sys.argv[1], 'exec')", "/sandbox/main.py"},
		run:        []string{"python3", "/sandbox/main.py"},
		oomMessage: "MemoryError",
	},
}

// Supported reports whether the judge can run a language.
func Supported(language string) bool {
	_, ok := languages[language]
	return ok
}

// Judge compiles source once, then runs it on each test case in order, all
// inside the sandbox. The first test that fails decides the verdict, and the
// remaining tests aren't run. If every test passes, the verdict is AC.
//
// It returns an error only when the judge itself failed: Docker is down, a
// temp file couldn't be written, the worker is shutting down. Anything the
// submitted code does wrong (doesn't compile, wrong output, crashes, runs
// too long, uses too much memory) is a verdict, not an error. An error means
// "this job should be tried again"; a verdict means "this job is finished".
func Judge(ctx context.Context, language, source string, tests []TestCase, limits Limits) (Result, error) {
	lang, ok := languages[language]
	if !ok {
		return Result{}, fmt.Errorf("language %q is not supported", language)
	}
	if len(tests) == 0 {
		return Result{}, errors.New("no test cases to judge against")
	}

	dir, err := os.MkdirTemp("", "submission-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	// The containers run as "nobody". MkdirTemp makes the directory private
	// to us (0700). 0777 lets the compiler, running as nobody, write its
	// output (the C++ binary, Java's .class file) next to the source. The
	// run step mounts the directory read-only, so the program itself still
	// can't change anything in it.
	if err := os.Chmod(dir, 0o777); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, lang.fileName), []byte(source), 0o644); err != nil {
		return Result{}, err
	}

	// Phase 1: compile, once. If this fails, nothing runs.
	compiled, err := sandbox.Run(ctx, sandbox.Spec{
		Image:     lang.image,
		Cmd:       lang.compile,
		Dir:       dir,
		Writable:  true,
		TimeLimit: compileTimeLimit,
		MemoryMB:  compileMemoryMB,
	})
	if err != nil {
		return Result{}, err
	}
	if compiled.Killed {
		return Result{Verdict: CompileError, Stderr: "compilation took too long"}, nil
	}
	if compiled.ExitCode != 0 {
		return Result{Verdict: CompileError, Stderr: compiled.Stderr + compiled.Stdout}, nil
	}

	// Phase 2: run the compiled program on each test case, in order.
	var res Result
	for i, tc := range tests {
		verdict, run, err := runTest(ctx, lang, dir, tc, limits)
		if err != nil {
			return Result{}, err
		}
		ms := int(run.Elapsed.Milliseconds())
		res.Tests = append(res.Tests, TestResult{Number: i + 1, Verdict: verdict, RuntimeMs: ms})
		res.RuntimeMs = max(res.RuntimeMs, ms)

		if verdict != Accepted {
			// The first failing test decides the verdict. Running the rest
			// can't change it, so don't spend the time.
			res.Verdict = verdict
			res.Stderr = run.Stderr
			return res, nil
		}
	}
	res.Verdict = Accepted
	return res, nil
}

// runTest runs the already-compiled program on one test case and classifies
// what happened.
func runTest(ctx context.Context, lang language, dir string, tc TestCase, limits Limits) (Verdict, sandbox.Result, error) {
	run, err := sandbox.Run(ctx, sandbox.Spec{
		Image:     lang.image,
		Cmd:       lang.run,
		Dir:       dir,
		Stdin:     tc.Input,
		TimeLimit: limits.Time,
		MemoryMB:  limits.MemoryMB,
	})
	if err != nil {
		return "", sandbox.Result{}, err
	}

	// The order matters. First, the reasons the program was *stopped*
	// (time, then memory), and only then whether it crashed on its own:
	// a program killed for either limit also exits with a non-zero code
	// (137), which would otherwise look like an RE. Time comes before
	// memory because if it was already over the time limit when it ran
	// out of memory, the time limit is the one it broke first.
	switch {
	case run.Killed || run.Elapsed > limits.Time:
		return TimeLimitExceeded, run, nil
	case run.OOMKilled || (run.ExitCode != 0 && strings.Contains(run.Stderr, lang.oomMessage)):
		return MemoryLimitExceeded, run, nil
	case run.ExitCode != 0:
		return RuntimeError, run, nil
	case !outputsMatch(run.Stdout, tc.Expected):
		return WrongAnswer, run, nil
	default:
		return Accepted, run, nil
	}
}
