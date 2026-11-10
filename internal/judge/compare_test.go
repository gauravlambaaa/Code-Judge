package judge

import "testing"

func TestOutputsMatch(t *testing.T) {
	tests := []struct {
		name      string
		got, want string
		match     bool
	}{
		{"identical", "5\n", "5\n", true},
		{"no final newline", "5", "5\n", true},
		{"extra blank lines at the end", "5\n\n\n", "5\n", true},
		{"trailing spaces", "1 2 3  \n", "1 2 3\n", true},
		{"windows line endings", "1\r\n2\r\n", "1\n2\n", true},
		{"different number", "6\n", "5\n", false},
		{"extra space inside a line", "1  2\n", "1 2\n", false},
		{"leading space", " 5\n", "5\n", false},
		{"missing line", "1\n", "1\n2\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outputsMatch(tt.got, tt.want); got != tt.match {
				t.Errorf("outputsMatch(%q, %q) = %v, want %v", tt.got, tt.want, got, tt.match)
			}
		})
	}
}
