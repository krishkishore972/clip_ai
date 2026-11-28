package transcription_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func TestNormalizeBoundedEndCorrections(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		duration, start, end float64
		offset, source, want int64
		count                int
		reason               string
	}{
		{name: "unchanged", duration: 1, start: .1, end: .5, offset: -7, source: 1000, want: 493},
		{name: "audio limit inclusive", duration: 1, start: .1, end: 3, source: 4000, want: 1000, count: 1},
		{name: "audio limit exceeded", duration: 1, start: .1, end: 3.000001, source: 4000, reason: "WORD_AUDIO_BOUNDS"},
		{name: "source limit inclusive", duration: 3, start: .1, end: 3, source: 1000, want: 1000, count: 1},
		{name: "source limit exceeded", duration: 3.1, start: .1, end: 3.000001, source: 1000, reason: "WORD_SOURCE_END"},
		{name: "audio clipping must not hide source overrun", duration: 1, start: .1, end: 3, source: 900, reason: "WORD_SOURCE_END"},
		{name: "earlier source boundary", duration: 1, start: .1, end: 1.1, source: 900, want: 900, count: 1},
		{name: "negative offset", duration: 1, start: .1, end: 1.1, offset: -100, source: 1000, want: 900, count: 1},
		{name: "start at audio end", duration: 1, start: 1, end: 1.1, source: 2000, reason: "WORD_CORRECTION_START_BOUNDS"},
		{name: "start after audio end", duration: 1, start: 1.05, end: 1.1, source: 2000, reason: "WORD_CORRECTION_START_BOUNDS"},
		{name: "start at source end", duration: 2, start: .9, end: 1.1, offset: 100, source: 1000, reason: "WORD_CORRECTION_START_BOUNDS"},
		{name: "start after source end", duration: 2, start: .95, end: 1.1, offset: 100, source: 1000, reason: "WORD_CORRECTION_START_BOUNDS"},
		{name: "fractional audio boundary", duration: 1.0006, start: 1.0007, end: 1.1, source: 2000, reason: "WORD_CORRECTION_START_BOUNDS"},
		{name: "rounding collapses interval", duration: 1, start: .9999, end: 1.1, source: 2000, reason: "WORD_ROUNDED_INTERVAL"},
		{name: "reported overrun synthetic regression", duration: 402.117, start: 401.77194, end: 402.25195, source: 402117, want: 402117, count: 1},
		{name: "695ms overrun regression", duration: 141.118, start: 140.613, end: 141.813, source: 141118, want: 141118, count: 1},
		{name: "775ms overrun regression", duration: 141.118, start: 140.613, end: 141.893, source: 141118, want: 141118, count: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, claim := documentResponse(), documentClaim()
			in.Metadata.Duration = tc.duration
			claim.AudioSizeBytes = int64(tc.duration*32000) + 44
			claim.AudioStartMs, claim.SourceDurationMs = tc.offset, tc.source
			word := &in.Results.Channels[0].Alternatives[0].Words[0]
			word.Start, word.End = tc.start, tc.end
			doc, err := transcription.Normalize(in, claim)
			if tc.reason != "" {
				if err == nil || transcription.DiagnoseOutput(err).Reason != tc.reason {
					t.Fatalf("expected %s, got %v", tc.reason, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if doc.Words[0].EndMs != tc.want || doc.Corrections.Count != tc.count {
				t.Fatalf("wrong correction: end=%d stats=%+v", doc.Words[0].EndMs, doc.Corrections)
			}
			if word.Start != tc.start || word.End != tc.end || doc.Words[0].Text != word.Word {
				t.Fatal("input or word text changed")
			}
			if tc.count > 0 && doc.Corrections.MaxAdjustmentMs <= 0 {
				t.Fatal("missing adjustment diagnostic")
			}
			if tc.name == "reported overrun synthetic regression" && doc.Corrections.MaxAdjustmentMs != 135 {
				t.Fatal("wrong maximum adjustment")
			}
			data, err := doc.JSON()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "Corrections") || strings.Contains(string(data), "Adjustment") {
				t.Fatal("runtime diagnostics persisted")
			}
			var persisted transcription.Document
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			words, err := persisted.WordsForCuration()
			if err != nil || words[0].EndMs != tc.want {
				t.Fatal("curation does not receive corrected persisted timing")
			}
		})
	}
}
