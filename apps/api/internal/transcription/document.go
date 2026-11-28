package transcription

import (
	"encoding/json"
	"regexp"
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
	// Corrections are content-free runtime diagnostics, not persisted schema.
	Corrections       EndCorrections `json:"-"`
	SchemaVersion     int            `json:"schemaVersion"`
	AssetID           string         `json:"assetId"`
	Provider          string         `json:"provider"`
	Model             string         `json:"model"`
	RequestedLanguage string         `json:"requestedLanguage"`
	Languages         []string       `json:"languages"`
	AudioStartMs      int64          `json:"audioStartMs"`
	AudioDurationMs   int64          `json:"audioDurationMs"`
	SourceDurationMs  int64          `json:"sourceDurationMs"`
	Transcript        string         `json:"transcript"`
	Words             []Word         `json:"words"`
}

type EndCorrections struct {
	Count           int
	MaxAdjustmentMs int64
}

func (d Document) JSON() ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, invalidOutput("DOCUMENT_ENCODING", -1)
	}
	if len(data) > MaxDocumentBytes {
		return nil, invalidOutput("DOCUMENT_SIZE_LIMIT", -1)
	}
	return data, nil
}
