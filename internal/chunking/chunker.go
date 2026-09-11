package chunking

import (
	"errors"
	"strings"
	"unicode"
)

var ErrInvalidConfig = errors.New("chunk size must be positive and overlap must be smaller than chunk size")

// WordSplitter divides text into fixed-size word windows with overlap.
type WordSplitter struct {
	maxWords     int
	overlapWords int
}

type Chunk struct {
	Text  string
	Start int
	End   int
	Page  int
}

type word struct {
	start int
	end   int
	page  int
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
func (s WordSplitter) Split(text string) []Chunk {
	words := findWords(text)

	var chunks []Chunk
	for first := 0; first < len(words); {
		last := first
		for last < len(words) && words[last].page == words[first].page {
			last++
		}
		chunks = s.appendWindows(chunks, text, words[first:last])
		first = last
	}

	return chunks
}

func (s WordSplitter) appendWindows(chunks []Chunk, text string, words []word) []Chunk {
	step := s.maxWords - s.overlapWords
	for start := 0; start < len(words); start += step {
		end := min(start+s.maxWords, len(words))
		window := words[start:end]
		chunks = append(chunks, Chunk{
			Text:  joinWords(text, window),
			Start: window[0].start,
			End:   window[len(window)-1].end,
			Page:  window[0].page,
		})
		if end == len(words) {
			break
		}
	}

	return chunks
}

func findWords(text string) []word {
	var words []word
	page := 1
	start := -1

	for index, r := range text {
		if !unicode.IsSpace(r) {
			if start < 0 {
				start = index
			}
			continue
		}

		if start >= 0 {
			words = append(words, word{start: start, end: index, page: page})
			start = -1
		}
		if r == '\f' {
			page++
		}
	}

	if start >= 0 {
		words = append(words, word{start: start, end: len(text), page: page})
	}

	return words
}

func joinWords(text string, words []word) string {
	var builder strings.Builder
	for index, w := range words {
		if index > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(text[w.start:w.end])
	}
	return builder.String()
}
