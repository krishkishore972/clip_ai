package transcription_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func TestWordsForCuration(t *testing.T) {
	doc := transcription.Document{
		SchemaVersion:    1,
		SourceDurationMs: 1000,
		AudioStartMs:     -7,
		Words: []transcription.Word{
			{Text: "मेरे", StartMs: -7, EndMs: 560},
			{Text: "मित्र", StartMs: 320, EndMs: 480},
		},
	}

	words, err := doc.WordsForCuration()
	if err != nil {
		t.Fatal(err)
	}
	for index, word := range words {
		original := doc.Words[index]
		if word.Index != index || word.Text != original.Text ||
			word.StartMs != original.StartMs || word.EndMs != original.EndMs {
			t.Fatalf("word order, text or source timing changed at index %d", index)
		}
	}

	data, err := json.Marshal(words)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"index":0`) ||
		!strings.Contains(string(data), `"startMs":-7`) {
		t.Fatal("curation JSON field names or signed timing changed")
	}
	words[0].Text = "changed"
	if doc.Words[0].Text != "मेरे" {
		t.Fatal("projection mutated the transcript")
	}
}

func TestWordsForCurationRejectsInvalidDocument(t *testing.T) {
	for name, change := range map[string]func(*transcription.Document){
		"schema":        func(d *transcription.Document) { d.SchemaVersion = 2 },
		"duration":      func(d *transcription.Document) { d.SourceDurationMs = 0 },
		"empty":         func(d *transcription.Document) { d.Words = nil },
		"text":          func(d *transcription.Document) { d.Words[0].Text = " " },
		"zero interval": func(d *transcription.Document) { d.Words[0].EndMs = 0 },
		"source bounds": func(d *transcription.Document) { d.Words[0].EndMs = 1001 },
		"unordered": func(d *transcription.Document) {
			d.Words = append(d.Words, transcription.Word{
				Text: "earlier", StartMs: -1, EndMs: 100,
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			doc := transcription.Document{
				SchemaVersion:    1,
				SourceDurationMs: 1000,
				Words: []transcription.Word{
					{Text: "hello", StartMs: 0, EndMs: 500},
				},
			}
			change(&doc)
			if _, err := doc.WordsForCuration(); !errors.Is(err, transcription.ErrCurationDocument) {
				t.Fatalf("got %v, want ErrCurationDocument", err)
			}
		})
	}
}
