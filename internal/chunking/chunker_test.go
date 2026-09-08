package chunking

import (
	"errors"
	"reflect"
	"testing"
)

func TestNewWordSplitterRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name         string
		maxWords     int
		overlapWords int
	}{
		{name: "zero chunk size", maxWords: 0, overlapWords: 0},
		{name: "negative overlap", maxWords: 4, overlapWords: -1},
		{name: "overlap equals chunk size", maxWords: 4, overlapWords: 4},
		{name: "overlap exceeds chunk size", maxWords: 4, overlapWords: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWordSplitter(tt.maxWords, tt.overlapWords)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
			}
		})
	}
}

func TestWordSplitterSplit(t *testing.T) {
	splitter, err := NewWordSplitter(4, 1)
	if err != nil {
		t.Fatalf("create splitter: %v", err)
	}

	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "blank text",
			text: "  \n\t ",
			want: nil,
		},
		{
			name: "short text",
			text: "one two three",
			want: []string{"one two three"},
		},
		{
			name: "exact chunk size",
			text: "one two three four",
			want: []string{"one two three four"},
		},
		{
			name: "overlapping chunks",
			text: "one two three four five six seven eight",
			want: []string{
				"one two three four",
				"four five six seven",
				"seven eight",
			},
		},
		{
			name: "whitespace normalization",
			text: "one   two\nthree\tfour",
			want: []string{"one two three four"},
		},
		{
			name: "unicode words",
			text: "hello 世界 from Recall",
			want: []string{"hello 世界 from Recall"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitter.Split(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("chunks = %#v, want %#v", got, tt.want)
			}
		})
	}
}
