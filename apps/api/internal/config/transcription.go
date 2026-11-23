package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadTranscriptLanguage is used by the preparation producer, without reading
// provider credentials. Empty language means no transcription jobs are created.
func LoadTranscriptLanguage() (string, error) {
	enabled, err := transcriptionFlag("TRANSCRIPT_FILES_ENABLED")
	if err != nil || !enabled {
		return "", err
	}
	language := strings.TrimSpace(os.Getenv("TRANSCRIPTION_LANGUAGE"))
	if language == "" {
		language = "multi"
	}
	switch language {
	case "multi", "te", "hi", "en", "ta", "kn", "ml":
		return language, nil
	default:
		return "", errors.New("config: TRANSCRIPTION_LANGUAGE must be multi, te, hi, en, ta, kn or ml")
	}
}

type TranscriptionWorker struct {
	Enabled                                      bool
	DatabaseURL, Region, Bucket, TempDir, APIKey string
}

func LoadTranscriptionWorker() (TranscriptionWorker, error) {
	enabled, err := transcriptionFlag("TRANSCRIPTION_WORKER_ENABLED")
	if err != nil || !enabled {
		return TranscriptionWorker{}, err
	}
	language, err := LoadTranscriptLanguage()
	if err != nil {
		return TranscriptionWorker{}, err
	}
	if language == "" {
		return TranscriptionWorker{}, errors.New("config: transcription worker requires TRANSCRIPT_FILES_ENABLED")
	}
	cfg := TranscriptionWorker{Enabled: true}
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"DATABASE_URL", &cfg.DatabaseURL}, {"AWS_REGION", &cfg.Region},
		{"S3_BUCKET", &cfg.Bucket}, {"TRANSCRIPTION_TEMP_DIR", &cfg.TempDir},
		{"DEEPGRAM_API_KEY", &cfg.APIKey},
	} {
		value, err := required(field.key)
		if err != nil {
			return TranscriptionWorker{}, err
		}
		*field.dst = value
	}
	if !filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) == string(filepath.Separator) ||
		strings.ContainsAny(cfg.Bucket, "/\\ \t\r\n") {
		return TranscriptionWorker{}, errors.New("config: invalid transcription bucket or private absolute temporary directory")
	}
	return cfg, nil
}

func transcriptionFlag(key string) (bool, error) {
	raw, exists := os.LookupEnv(key)
	if !exists {
		return false, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, errors.New("config: " + key + " must be a boolean")
	}
	return value, nil
}
