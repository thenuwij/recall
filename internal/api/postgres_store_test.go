package api

import "testing"

func TestFormatVector(t *testing.T) {
	if got, want := formatVector([]float32{1, -2.5, 0}), "[1,-2.5,0]"; got != want {
		t.Fatalf("formatVector() = %q, want %q", got, want)
	}
}
