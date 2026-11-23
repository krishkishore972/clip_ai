package transcription_test

import (
	"math"
	"strings"
	"testing"

	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func documentClaim() transcription.Claim {
	return transcription.Claim{
		AssetID: "asset-1", Bucket: "private", AudioKey: "dev-uploads/asset-1/derived/v1/output/audio.wav",
		AudioSHA256: strings.Repeat("a", 64), AudioSizeBytes: 32044, AudioStartMs: -7,
		SourceDurationMs: 1000, Language: "multi",
	}
}

func documentResponse() *response.PreRecordedResponse {
	return &response.PreRecordedResponse{
		Metadata: &response.Metadata{Duration: 1},
		Results: &response.Result{Channels: []response.Channel{{
			Alternatives: []response.Alternative{{
				Transcript: "Hello.", Languages: []string{"en"},
				Words: []response.Word{{Word: "hello", PunctuatedWord: "Hello.", Start: .1, End: .5, Confidence: .9, Language: "en"}},
			}},
		}}},
	}
}

func TestNormalizeTranscript(t *testing.T) {
	doc, err := transcription.Normalize(documentResponse(), documentClaim())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Words[0].StartMs != 93 || doc.Words[0].EndMs != 493 || doc.AudioStartMs != -7 ||
		doc.SchemaVersion != 1 || doc.RequestedLanguage != "multi" || doc.Words[0].Language != "en" {
		t.Fatalf("source time/language mapping drift: %+v", doc)
	}
	data, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"audio.wav", "private", "request_id", "claim_token"} {
		if strings.Contains(string(data), private) {
			t.Fatal("document exposes private storage/provider metadata")
		}
	}
}

func TestNormalizeRejectsInvalidWords(t *testing.T) {
	for name, change := range map[string]func(*response.Word){
		"nan":           func(w *response.Word) { w.Start = math.NaN() },
		"negative":      func(w *response.Word) { w.Start = -.1 },
		"inverted":      func(w *response.Word) { w.End = .01 },
		"zero duration": func(w *response.Word) { w.End = w.Start },
		"source bounds": func(w *response.Word) { w.End = 1.1 },
		"confidence":    func(w *response.Word) { w.Confidence = 1.1 },
		"empty":         func(w *response.Word) { w.Word = "" },
		"utf8":          func(w *response.Word) { w.Word = "\xff" },
		"language":      func(w *response.Word) { w.Language = "https://private" },
	} {
		t.Run(name, func(t *testing.T) {
			in := documentResponse()
			change(&in.Results.Channels[0].Alternatives[0].Words[0])
			if _, err := transcription.Normalize(in, documentClaim()); err == nil {
				t.Fatal("invalid provider word accepted")
			}
		})
	}
	in := documentResponse()
	alt := &in.Results.Channels[0].Alternatives[0]
	alt.Words = append(alt.Words, response.Word{Word: "second", Start: .01, End: .05})
	if _, err := transcription.Normalize(in, documentClaim()); err == nil {
		t.Fatal("unordered words accepted")
	}
}

func TestNormalizeSilenceAndInvalidResponses(t *testing.T) {
	in := documentResponse()
	in.Results.Channels[0].Alternatives[0] = response.Alternative{}
	doc, err := transcription.Normalize(in, documentClaim())
	if err != nil || len(doc.Words) != 0 || doc.Transcript != "" {
		t.Fatalf("silence rejected: %v", err)
	}
	for _, in := range []*response.PreRecordedResponse{nil, {}, {Metadata: &response.Metadata{}}, {
		Metadata: &response.Metadata{Duration: math.Inf(1)}, Results: documentResponse().Results,
	}} {
		if _, err := transcription.Normalize(in, documentClaim()); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	in = documentResponse()
	in.Metadata.Duration = 40
	if _, err := transcription.Normalize(in, documentClaim()); err == nil {
		t.Fatal("different audio duration accepted")
	}
}
