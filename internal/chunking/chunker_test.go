package chunking

import (
	"errors"
	"reflect"
	"strings"
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
			if got := chunkTexts(splitter.Split(tt.text)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("chunks = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWordSplitterSplitSpans(t *testing.T) {
	tests := []struct {
		name         string
		maxWords     int
		overlapWords int
		text         string
		want         []Chunk
	}{
		{
			name:         "offsets skip irregular whitespace",
			maxWords:     4,
			overlapWords: 1,
			text:         "one   two\nthree\tfour five",
			want: []Chunk{
				{Text: "one two three four", Start: 0, End: 20, Page: 1},
				{Text: "four five", Start: 16, End: 25, Page: 1},
			},
		},
		{
			name:         "chunks never cross a page and blank pages keep their number",
			maxWords:     4,
			overlapWords: 1,
			text:         "one two\fthree four five six seven\f\feight",
			want: []Chunk{
				{Text: "one two", Start: 0, End: 7, Page: 1},
				{Text: "three four five six", Start: 8, End: 27, Page: 2},
				{Text: "six seven", Start: 24, End: 33, Page: 2},
				{Text: "eight", Start: 35, End: 40, Page: 4},
			},
		},
		{
			name:         "leading form feed starts on page two",
			maxWords:     4,
			overlapWords: 1,
			text:         "\fone\f",
			want: []Chunk{
				{Text: "one", Start: 1, End: 4, Page: 2},
			},
		},
		{
			name:         "multi-byte characters use byte offsets",
			maxWords:     2,
			overlapWords: 1,
			text:         "café —  αβγ",
			want: []Chunk{
				{Text: "café —", Start: 0, End: 9, Page: 1},
				{Text: "— αβγ", Start: 6, End: 17, Page: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			splitter, err := NewWordSplitter(tt.maxWords, tt.overlapWords)
			if err != nil {
				t.Fatalf("create splitter: %v", err)
			}

			got := splitter.Split(tt.text)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("chunks = %#v, want %#v", got, tt.want)
			}

			for _, chunk := range got {
				span := tt.text[chunk.Start:chunk.End]
				if strings.ContainsRune(span, '\f') {
					t.Fatalf("chunk %q spans a page break", chunk.Text)
				}
				if normalized := strings.Join(strings.Fields(span), " "); normalized != chunk.Text {
					t.Fatalf("span %q normalizes to %q, want %q", span, normalized, chunk.Text)
				}
			}
		})
	}
}

func chunkTexts(chunks []Chunk) []string {
	if chunks == nil {
		return nil
	}

	texts := make([]string, len(chunks))
	for index, chunk := range chunks {
		texts[index] = chunk.Text
	}
	return texts
}
