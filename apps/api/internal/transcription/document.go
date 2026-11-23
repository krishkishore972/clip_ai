package transcription

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/krishkishore972/clip_ai/internal/media"
)

const (
	MaxDocumentBytes = 16 * 1024 * 1024
	MaxWords         = 200_000
	MaxAttempts      = 2
	TaskType         = "media:transcribe-file"
	Queue            = "transcription"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var checksum = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Word timestamps refer to the original source, not WAV sample zero.
type Word struct {
	Text           string  `json:"word"`
	PunctuatedWord string  `json:"punctuatedWord,omitempty"`
	StartMs        int64   `json:"startMs"`
	EndMs          int64   `json:"endMs"`
	Confidence     float64 `json:"confidence"`
	Language       string  `json:"language,omitempty"`
}

// Document is the private, versioned input for future server-side analysis.
// It contains no bucket/key/URL, owner identity, capability or provider request ID.
type Document struct {
	SchemaVersion     int      `json:"schemaVersion"`
	AssetID           string   `json:"assetId"`
	Provider          string   `json:"provider"`
	Model             string   `json:"model"`
	RequestedLanguage string   `json:"requestedLanguage"`
	Languages         []string `json:"languages"`
	AudioStartMs      int64    `json:"audioStartMs"`
	AudioDurationMs   int64    `json:"audioDurationMs"`
	SourceDurationMs  int64    `json:"sourceDurationMs"`
	Transcript        string   `json:"transcript"`
	Words             []Word   `json:"words"`
}

// Normalize validates Deepgram's mono prepared-audio response and maps all word
// times using source_ms = audio_start_ms + round(provider_seconds * 1000).
func Normalize(in *response.PreRecordedResponse, claim Claim) (Document, error) {
	if !claim.ValidSource() || in == nil || in.Metadata == nil || in.Results == nil ||
		len(in.Results.Channels) != 1 || len(in.Results.Channels[0].Alternatives) == 0 {
		return Document{}, ErrResponse
	}
	duration := in.Metadata.Duration
	if !finite(duration) || duration <= 0 || duration > 86400 {
		return Document{}, ErrResponse
	}
	// Prepared audio is mono 16 kHz signed-16-bit PCM. Account for the WAV
	// header and rounding, while rejecting a response for another input.
	expectedMs := claim.AudioSizeBytes * 1000 / 32000
	durationMs := int64(math.Round(duration * 1000))
	if durationMs > expectedMs+1000 || durationMs < expectedMs-1000 {
		return Document{}, ErrResponse
	}
	alt := in.Results.Channels[0].Alternatives[0]
	if !utf8.ValidString(alt.Transcript) || len(alt.Transcript) > MaxDocumentBytes/2 ||
		len(alt.Words) > MaxWords || len(alt.Languages) > 64 ||
		(strings.TrimSpace(alt.Transcript) == "") != (len(alt.Words) == 0) {
		return Document{}, ErrResponse
	}
	doc := Document{
		SchemaVersion: 1, AssetID: claim.AssetID, Provider: "deepgram",
		Model: "nova-3", RequestedLanguage: claim.Language,
		Languages: append([]string{}, alt.Languages...), AudioStartMs: claim.AudioStartMs,
		AudioDurationMs: durationMs, SourceDurationMs: claim.SourceDurationMs,
		Transcript: alt.Transcript, Words: make([]Word, 0, len(alt.Words)),
	}
	for _, lang := range doc.Languages {
		if !validLanguageTag(lang) {
			return Document{}, ErrResponse
		}
	}
	var previousStart float64 = -1
	for _, w := range alt.Words {
		if !validText(w.Word) || !utf8.ValidString(w.PunctuatedWord) ||
			len(w.PunctuatedWord) > 512 || !finite(w.Start) || !finite(w.End) ||
			w.Start < 0 || w.End <= w.Start || w.Start < previousStart ||
			w.End > duration+0.1 || !finite(w.Confidence) ||
			w.Confidence < 0 || w.Confidence > 1 ||
			(w.Language != "" && !validLanguageTag(w.Language)) {
			return Document{}, ErrResponse
		}
		start := claim.AudioStartMs + int64(math.Round(w.Start*1000))
		end := claim.AudioStartMs + int64(math.Round(w.End*1000))
		if end <= start || end > claim.SourceDurationMs || start < -media.MaxAudioStartMs {
			return Document{}, ErrResponse
		}
		doc.Words = append(doc.Words, Word{
			Text: w.Word, PunctuatedWord: w.PunctuatedWord, StartMs: start,
			EndMs: end, Confidence: w.Confidence, Language: w.Language,
		})
		previousStart = w.Start
	}
	return doc, nil
}

func (d Document) JSON() ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil || len(data) > MaxDocumentBytes {
		return nil, ErrResponse
	}
	return data, nil
}

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func validText(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && len(s) <= 512
}
func validLanguageTag(s string) bool {
	return len(s) <= 32 && regexpLanguage.MatchString(s)
}

var regexpLanguage = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
