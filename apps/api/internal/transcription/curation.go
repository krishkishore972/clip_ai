package transcription

import (
	"errors"

	"github.com/krishkishore972/clip_ai/internal/media"
)

var ErrCurationDocument = errors.New("transcription: invalid document for curation")

// CurationWord is the provider-independent word representation sent to the LLM
// service. Index is its zero-based position in the immutable document word list.
// Times are already source-aligned integer milliseconds, not provider seconds.
// StartMs may be negative when prepared audio starts before source time zero.
type CurationWord struct {
	Index   int    `json:"index"`
	Text    string `json:"text"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

// WordsForCuration projects a normalized transcript without changing its word
// order, text or timing. Call Normalize first for raw Deepgram responses; never
// apply the audio offset a second time. Empty transcripts cannot be curated.
func (d Document) WordsForCuration() ([]CurationWord, error) {
	if d.SchemaVersion != 1 || d.SourceDurationMs <= 0 ||
		len(d.Words) == 0 || len(d.Words) > MaxWords {
		return nil, ErrCurationDocument
	}

	words := make([]CurationWord, 0, len(d.Words))
	for index, word := range d.Words {
		if !validText(word.Text) ||
			word.StartMs < -media.MaxAudioStartMs ||
			word.EndMs <= word.StartMs ||
			word.EndMs > d.SourceDurationMs ||
			(index > 0 && word.StartMs < d.Words[index-1].StartMs) {
			return nil, ErrCurationDocument
		}

		// Overlapping intervals and decreasing end times are valid ASR output.
		words = append(words, CurationWord{
			Index:   index,
			Text:    word.Text,
			StartMs: word.StartMs,
			EndMs:   word.EndMs,
		})
	}

	return words, nil
}
