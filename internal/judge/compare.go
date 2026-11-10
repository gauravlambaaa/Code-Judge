package judge

import "strings"

// outputsMatch compares a program's output with the expected output.
// It ignores differences nobody would call a wrong answer:
//   - Windows line endings (\r\n) vs Unix ones (\n)
//   - spaces or tabs at the end of a line
//   - blank lines at the very end
//
// Everything else must match exactly, including spaces inside a line.
func outputsMatch(got, want string) bool {
	return normalize(got) == normalize(want)
}

func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}
