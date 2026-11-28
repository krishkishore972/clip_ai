package curation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/krishkishore972/clip_ai/internal/media"
)

const (
	Model         = "openai/gpt-oss-120b"
	PromptVersion = "v2"
)

// Proposal contains only model-proposed word ranges and display metadata.
// The model never supplies authoritative timestamps or persistent IDs.
type Proposal struct {
	StartWordIndex        int    `json:"startWordIndex"`
	EndWordIndexExclusive int    `json:"endWordIndexExclusive"`
	Title                 string `json:"title"`
	Hook                  string `json:"hook"`
	Reason                string `json:"reason"`
}

type candidate struct {
	Proposal
	startMs, endMs int64
}

// Publication can only be populated by ValidateResult. Its private data binds
// the validated ranges and derived timing to the exact durable claim.
type Publication struct {
	claim      Claim
	candidates []candidate
	valid      bool
}

// ValidateResult verifies the exact stored transcript bytes before deriving
// candidate timestamps. It deliberately does not import transcription (whose
// store imports curation), call S3 or accept caller-supplied timestamps.
// Empty transcripts are valid only with an empty proposal set.
func ValidateResult(c Claim, transcript []byte, proposals []Proposal) (*Publication, error) {
	if !c.valid() || len(transcript) == 0 || len(transcript) > 16*1024*1024 ||
		int64(len(transcript)) != c.TranscriptSizeBytes || !utf8.Valid(transcript) ||
		len(proposals) > c.CandidateCount {
		return nil, ErrResult
	}
	hash := sha256.Sum256(transcript)
	if hex.EncodeToString(hash[:]) != c.TranscriptSHA256 {
		return nil, ErrResult
	}
	// Read only the versioned fields needed for authoritative range validation.
	// Other normalized transcript fields remain private artifact data.
	var doc struct {
		SchemaVersion    int    `json:"schemaVersion"`
		AssetID          string `json:"assetId"`
		SourceDurationMs int64  `json:"sourceDurationMs"`
		Words            []struct {
			Text    string `json:"word"`
			StartMs *int64 `json:"startMs"`
			EndMs   *int64 `json:"endMs"`
		} `json:"words"`
	}
	if err := json.Unmarshal(transcript, &doc); err != nil ||
		doc.SchemaVersion != 1 || doc.AssetID != c.AssetID ||
		doc.SourceDurationMs != c.SourceDurationMs || doc.Words == nil ||
		len(doc.Words) != c.TranscriptWordCount {
		return nil, ErrResult
	}
	for i, w := range doc.Words {
		if w.StartMs == nil || w.EndMs == nil || !utf8.ValidString(w.Text) ||
			strings.TrimSpace(w.Text) == "" || strings.ContainsRune(w.Text, 0) ||
			len(w.Text) > 512 || *w.StartMs < -media.MaxAudioStartMs ||
			*w.EndMs <= *w.StartMs || *w.EndMs > c.SourceDurationMs ||
			(i > 0 && *w.StartMs < *doc.Words[i-1].StartMs) {
			return nil, ErrResult
		}
	}
	result := &Publication{claim: c, valid: true, candidates: make([]candidate, 0, len(proposals))}
	seen := make(map[[2]int]bool)
	for _, p := range proposals {
		key := [2]int{p.StartWordIndex, p.EndWordIndexExclusive}
		if p.StartWordIndex < 0 || p.EndWordIndexExclusive <= p.StartWordIndex ||
			p.EndWordIndexExclusive > len(doc.Words) || seen[key] ||
			!validMetadata(p.Title) || !validMetadata(p.Hook) || !validMetadata(p.Reason) {
			return nil, ErrResult
		}
		seen[key] = true
		start := *doc.Words[p.StartWordIndex].StartMs
		end := *doc.Words[p.StartWordIndex].EndMs
		for _, word := range doc.Words[p.StartWordIndex:p.EndWordIndexExclusive] {
			end = max(end, *word.EndMs)
		}
		if start < 0 || end > c.SourceDurationMs ||
			end-start < c.MinDurationMs || end-start > c.MaxDurationMs {
			return nil, ErrResult
		}
		result.candidates = append(result.candidates, candidate{Proposal: p, startMs: start, endMs: end})
	}
	return result, nil
}

func validMetadata(text string) bool {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || strings.ContainsRune(text, 0) {
		return false
	}
	units := 0
	for _, r := range text {
		units++
		if r > 0xffff {
			units++
		}
		if units > 1000 {
			return false
		}
	}
	return true
}
