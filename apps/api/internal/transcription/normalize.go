package transcription

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/krishkishore972/clip_ai/internal/media"
)

// MaxWordEndOverrunMs bounds ASR end-of-file correction, not timestamp drift.
const MaxWordEndOverrunMs = 2000

// Normalize validates Deepgram's mono prepared-audio response and maps all word
// times using source_ms = audio_start_ms + round(provider_seconds * 1000).
func Normalize(in *response.PreRecordedResponse, claim Claim) (Document, error) {
	if !claim.ValidSource() {
		return Document{}, invalidOutput("INPUT_SNAPSHOT", -1)
	}

	if in == nil || in.Metadata == nil || in.Results == nil {
		return Document{}, invalidOutput("RESPONSE_STRUCTURE", -1)
	}

	if len(in.Results.Channels) != 1 {
		return Document{}, invalidOutput("RESPONSE_CHANNEL_COUNT", -1)
	}

	if len(in.Results.Channels[0].Alternatives) == 0 {
		return Document{}, invalidOutput("RESPONSE_ALTERNATIVES", -1)
	}

	duration := in.Metadata.Duration

	if !finite(duration) || duration <= 0 || duration > 86400 {
		return Document{}, invalidOutput("AUDIO_DURATION_INVALID", -1)
	}

	// Prepared audio is mono 16 kHz signed-16-bit PCM. Account for the WAV
	// header and rounding, while rejecting a response for another input.
	expectedMs := claim.AudioSizeBytes * 1000 / 32000

	durationMs := int64(math.Round(duration * 1000))

	if durationMs > expectedMs+1000 || durationMs < expectedMs-1000 {
		return Document{}, durationMismatch(expectedMs, durationMs)
	}

	alt := in.Results.Channels[0].Alternatives[0]

	if !utf8.ValidString(alt.Transcript) || len(alt.Transcript) > MaxDocumentBytes/2 {
		return Document{}, invalidOutput("TRANSCRIPT_TEXT", -1)
	}
	if len(alt.Words) > MaxWords {
		return Document{}, invalidOutput("WORD_COUNT_LIMIT", -1)
	}
	if len(alt.Languages) > 64 {
		return Document{}, invalidOutput("LANGUAGE_COUNT_LIMIT", -1)
	}
	if (strings.TrimSpace(alt.Transcript) == "") != (len(alt.Words) == 0) {
		return Document{}, invalidOutput("TRANSCRIPT_WORD_MISMATCH", -1)
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
			return Document{}, invalidOutput("DOCUMENT_LANGUAGE", -1)
		}
	}

	var previousStart float64 = -1

	for index, w := range alt.Words {
		if !validText(w.Word) {
			return Document{}, invalidOutput("WORD_TEXT", index)
		}
		if !utf8.ValidString(w.PunctuatedWord) || len(w.PunctuatedWord) > 512 {
			return Document{}, invalidOutput("WORD_PUNCTUATION", index)
		}
		if !finite(w.Start) || !finite(w.End) {
			return Document{}, invalidOutput("WORD_TIME_NONFINITE", index)
		}
		if w.Start < 0 {
			return Document{}, invalidOutput("WORD_START_NEGATIVE", index)
		}
		if w.End <= w.Start {
			return Document{}, invalidOutput("WORD_INTERVAL", index)
		}
		if w.Start < previousStart {
			return Document{}, invalidOutput("WORD_ORDER", index)
		}
		if w.End > duration+float64(MaxWordEndOverrunMs)/1000 {
			return Document{}, wordAudioBounds(index, w.End, duration)
		}
		if !finite(w.Confidence) || w.Confidence < 0 || w.Confidence > 1 {
			return Document{}, invalidOutput("WORD_CONFIDENCE", index)
		}
		if w.Language != "" && !validLanguageTag(w.Language) {
			return Document{}, invalidOutput("WORD_LANGUAGE", index)
		}
		
		start := claim.AudioStartMs + int64(math.Round(w.Start*1000))
		end := claim.AudioStartMs + int64(math.Round(w.End*1000))

		if end <= start {
			return Document{}, invalidOutput("WORD_ROUNDED_INTERVAL", index)
		}
		// Check the ORIGINAL end against both limits before any correction.
		// Otherwise clipping to audio first could hide a larger source overrun.
		sourceEndMs := float64(claim.AudioStartMs) + w.End*1000
		if sourceEndMs > float64(claim.SourceDurationMs)+MaxWordEndOverrunMs {
			return Document{}, invalidOutput("WORD_SOURCE_END", index)
		}
		boundary := min(claim.AudioStartMs+durationMs, claim.SourceDurationMs)
		if w.End > duration || sourceEndMs > float64(claim.SourceDurationMs) {
			// Require actual overlap before rounding: a rounded audio boundary
			// must not make a word wholly outside the media appear correctable.
			sourceStartMs := float64(claim.AudioStartMs) + w.Start*1000
			if w.Start >= duration || sourceStartMs >= float64(claim.SourceDurationMs) {
				return Document{}, invalidOutput("WORD_CORRECTION_START_BOUNDS", index)
			}
			end = min(end, boundary)
			if end <= start {
				return Document{}, invalidOutput("WORD_ROUNDED_INTERVAL", index)
			}
			doc.Corrections.Count++
			adjustment := int64(math.Ceil(sourceEndMs - float64(end)))
			doc.Corrections.MaxAdjustmentMs = max(doc.Corrections.MaxAdjustmentMs, adjustment)
		}
		if start < -media.MaxAudioStartMs {
			return Document{}, invalidOutput("WORD_SOURCE_START", index)
		}
		doc.Words = append(doc.Words, Word{
			Text: w.Word, PunctuatedWord: w.PunctuatedWord, StartMs: start,
			EndMs: end, Confidence: w.Confidence, Language: w.Language,
		})
		previousStart = w.Start
	}
	return doc, nil
}

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func validText(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && len(s) <= 512
}
func validLanguageTag(s string) bool {
	return len(s) <= 32 && regexpLanguage.MatchString(s)
}

var regexpLanguage = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
