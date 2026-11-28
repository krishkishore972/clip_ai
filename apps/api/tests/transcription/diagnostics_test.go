package transcription_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func TestNormalizeOutputDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		reason string
		change func(*response.PreRecordedResponse, *transcription.Claim)
		index  int
	}{
		{"INPUT_SNAPSHOT", func(_ *response.PreRecordedResponse, c *transcription.Claim) { c.AudioSHA256 = "private-key" }, -1},
		{"RESPONSE_STRUCTURE", func(in *response.PreRecordedResponse, _ *transcription.Claim) { in.Metadata = nil }, -1},
		{"RESPONSE_CHANNEL_COUNT", func(in *response.PreRecordedResponse, _ *transcription.Claim) { in.Results.Channels = nil }, -1},
		{"RESPONSE_ALTERNATIVES", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives = nil
		}, -1},
		{"AUDIO_DURATION_INVALID", func(in *response.PreRecordedResponse, _ *transcription.Claim) { in.Metadata.Duration = math.NaN() }, -1},
		{"AUDIO_DURATION_MISMATCH", func(in *response.PreRecordedResponse, _ *transcription.Claim) { in.Metadata.Duration = 4 }, -1},
		{"TRANSCRIPT_TEXT", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Transcript = "\xff"
		}, -1},
		{"TRANSCRIPT_WORD_MISMATCH", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Transcript = ""
		}, -1},
		{"DOCUMENT_LANGUAGE", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Languages = []string{"private-language-value"}
		}, -1},
		{"WORD_TEXT", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].Word = ""
		}, 0},
		{"WORD_PUNCTUATION", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].PunctuatedWord = "\xff"
		}, 0},
		{"WORD_TIME_NONFINITE", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].Start = math.Inf(1)
		}, 0},
		{"WORD_START_NEGATIVE", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].Start = -.1
		}, 0},
		{"WORD_INTERVAL", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].End = .1
		}, 0},
		{"WORD_AUDIO_BOUNDS", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].End = 1.251
		}, 0},
		{"WORD_CONFIDENCE", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].Confidence = math.NaN()
		}, 0},
		{"WORD_LANGUAGE", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			in.Results.Channels[0].Alternatives[0].Words[0].Language = "private-language-value"
		}, 0},
		{"WORD_ROUNDED_INTERVAL", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			w := &in.Results.Channels[0].Alternatives[0].Words[0]
			w.Start, w.End = .10001, .10002
		}, 0},
		{"WORD_SOURCE_END", func(in *response.PreRecordedResponse, c *transcription.Claim) {
			c.SourceDurationMs = 700
			in.Results.Channels[0].Alternatives[0].Words[0].End = 1
		}, 0},
		{"WORD_ORDER", func(in *response.PreRecordedResponse, _ *transcription.Claim) {
			alt := &in.Results.Channels[0].Alternatives[0]
			alt.Words = append(alt.Words, response.Word{Word: "private-word", Start: .01, End: .05})
		}, 1},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			in, c := documentResponse(), documentClaim()
			tc.change(in, &c)
			_, err := transcription.Normalize(in, c)
			if !errors.Is(err, transcription.ErrResponse) {
				t.Fatalf("response classification changed: %v", err)
			}
			d := transcription.DiagnoseOutput(err)
			if d.Reason != tc.reason || d.WordIndex != tc.index {
				t.Fatalf("wrong diagnostic: %+v", d)
			}
			if d.Reason == "AUDIO_DURATION_MISMATCH" && (d.ExpectedDurationMs != 1001 || d.ObservedDurationMs != 4000) {
				t.Fatalf("wrong safe duration facts: %+v", d)
			}
			for _, secret := range []string{"private-key", "private-language-value", "private-word", "Hello."} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("diagnostic error exposed input")
				}
			}
		})
	}
}

func TestOutputDiagnosticDoesNotExposeWrappedErrors(t *testing.T) {
	in := documentResponse()
	in.Metadata.Duration = 4
	_, invalid := transcription.Normalize(in, documentClaim())
	err := errors.Join(errors.New("private-provider-body"), invalid)
	if got := transcription.DiagnoseOutput(err); got.Reason != "AUDIO_DURATION_MISMATCH" {
		t.Fatal("safe diagnostic lost in wrapper")
	}
	for _, err := range []error{nil, transcription.ErrResponse, errors.New("private-provider-body")} {
		got := transcription.DiagnoseOutput(err)
		if got.Reason != "OUTPUT_UNCLASSIFIED" || got.WordIndex != -1 {
			t.Fatal("unknown error exposed data")
		}
	}
}

func TestAudioBoundsDiagnosticPrecisionAndFiniteValues(t *testing.T) {
	for _, end := range []float64{1.250001, 1.5, math.MaxFloat64} {
		in, claim := documentResponse(), documentClaim()
		in.Results.Channels[0].Alternatives[0].Words[0].End = end
		_, err := transcription.Normalize(in, claim)
		if !errors.Is(err, transcription.ErrResponse) {
			t.Fatal("audio-bound rejection classification changed")
		}
		diagnostic := transcription.DiagnoseOutput(errors.Join(errors.New("private-provider-body"), err))
		if diagnostic.Reason != "WORD_AUDIO_BOUNDS" || diagnostic.WordIndex != 0 ||
			diagnostic.WordEndSeconds != end || diagnostic.AudioDurationSeconds != 1 ||
			diagnostic.OverrunSeconds != end-1 || diagnostic.AllowedOverrunMs != 250 {
			t.Fatalf("wrong numeric diagnostic: %+v", diagnostic)
		}
		if math.IsInf(diagnostic.OverrunSeconds, 0) || math.IsNaN(diagnostic.OverrunSeconds) {
			t.Fatal("diagnostic arithmetic overflowed")
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("diagnostic exposed raw error content")
		}
	}
	for _, end := range []float64{math.Inf(1), math.NaN()} {
		in := documentResponse()
		in.Results.Channels[0].Alternatives[0].Words[0].End = end
		_, err := transcription.Normalize(in, documentClaim())
		diagnostic := transcription.DiagnoseOutput(err)
		if diagnostic.Reason != "WORD_TIME_NONFINITE" || diagnostic.WordEndSeconds != 0 ||
			diagnostic.AudioDurationSeconds != 0 || diagnostic.OverrunSeconds != 0 || diagnostic.AllowedOverrunMs != 0 {
			t.Fatal("nonfinite times escaped the existing guard")
		}
	}
}

func TestDocumentSerializationDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		reason string
		doc    transcription.Document
	}{
		{"DOCUMENT_ENCODING", transcription.Document{Words: []transcription.Word{{Confidence: math.NaN()}}}},
		{"DOCUMENT_SIZE_LIMIT", transcription.Document{Transcript: strings.Repeat("x", transcription.MaxDocumentBytes+1)}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			_, err := tc.doc.JSON()
			if !errors.Is(err, transcription.ErrResponse) || transcription.DiagnoseOutput(err).Reason != tc.reason {
				t.Fatalf("missing serialization diagnostic: %v", err)
			}
		})
	}
}
