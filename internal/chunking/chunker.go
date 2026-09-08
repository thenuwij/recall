package chunking

import (
	"errors"
	"strings"
)

var ErrInvalidConfig = errors.New("chunk size must be positive and overlap must be smaller than chunk size")

// WordSplitter divides text into fixed-size word windows with overlap.
type WordSplitter struct {
	maxWords     int
	overlapWords int
}

// NewWordSplitter creates a splitter with validated chunking limits.
func NewWordSplitter(maxWords, overlapWords int) (WordSplitter, error) {
	if maxWords <= 0 || overlapWords < 0 || overlapWords >= maxWords {
		return WordSplitter{}, ErrInvalidConfig
	}

	return WordSplitter{
		maxWords:     maxWords,
		overlapWords: overlapWords,
	}, nil
}

// Split returns ordered chunks and normalizes whitespace between words.
func (s WordSplitter) Split(text string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	step := s.maxWords - s.overlapWords
	chunks := make([]string, 0, (len(words)+step-1)/step)
	for start := 0; start < len(words); start += step {
		end := min(start+s.maxWords, len(words))
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			break
		}
	}

	return chunks
}
