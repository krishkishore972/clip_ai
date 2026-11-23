package config_test

import (
	"testing"

	"github.com/krishkishore972/clip_ai/internal/config"
)

func TestTranscriptionConfigDefaultsAndLanguage(t *testing.T) {
	t.Setenv("TRANSCRIPT_FILES_ENABLED", "false")
	t.Setenv("TRANSCRIPTION_WORKER_ENABLED", "false")
	if lang, err := config.LoadTranscriptLanguage(); err != nil || lang != "" {
		t.Fatalf("producer not disabled: %v", err)
	}
	if cfg, err := config.LoadTranscriptionWorker(); err != nil || cfg.Enabled {
		t.Fatalf("worker not disabled: %v", err)
	}
	t.Setenv("TRANSCRIPT_FILES_ENABLED", "true")
	t.Setenv("TRANSCRIPTION_LANGUAGE", "")
	if lang, err := config.LoadTranscriptLanguage(); err != nil || lang != "multi" {
		t.Fatal("multilingual default lost")
	}
	for _, lang := range []string{"multi", "te", "hi", "en", "ta", "kn", "ml"} {
		t.Setenv("TRANSCRIPTION_LANGUAGE", lang)
		if got, err := config.LoadTranscriptLanguage(); err != nil || got != lang {
			t.Fatalf("language rejected: %v", err)
		}
	}
	t.Setenv("TRANSCRIPTION_LANGUAGE", "us")
	if _, err := config.LoadTranscriptLanguage(); err == nil {
		t.Fatal("invalid language accepted")
	}
}

func TestTranscriptionWorkerConfigFailsClosed(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "AWS_REGION", "S3_BUCKET", "TRANSCRIPTION_TEMP_DIR", "DEEPGRAM_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("TRANSCRIPTION_WORKER_ENABLED", "true")
	t.Setenv("TRANSCRIPT_FILES_ENABLED", "false")
	if _, err := config.LoadTranscriptionWorker(); err == nil {
		t.Fatal("worker accepted disabled producer")
	}
	t.Setenv("TRANSCRIPT_FILES_ENABLED", "true")
	t.Setenv("TRANSCRIPTION_LANGUAGE", "multi")
	if _, err := config.LoadTranscriptionWorker(); err == nil {
		t.Fatal("missing dependencies accepted")
	}
	t.Setenv("DATABASE_URL", "postgres://offline")
	t.Setenv("AWS_REGION", "offline")
	t.Setenv("S3_BUCKET", "private")
	t.Setenv("TRANSCRIPTION_TEMP_DIR", "/var/tmp/offline-transcription")
	t.Setenv("DEEPGRAM_API_KEY", "offline")
	if cfg, err := config.LoadTranscriptionWorker(); err != nil || !cfg.Enabled {
		t.Fatalf("valid worker config rejected: %v", err)
	}
	t.Setenv("TRANSCRIPTION_TEMP_DIR", "/")
	if _, err := config.LoadTranscriptionWorker(); err == nil {
		t.Fatal("root temporary directory accepted")
	}
	t.Setenv("TRANSCRIPT_FILES_ENABLED", "invalid")
	if _, err := config.LoadTranscriptLanguage(); err == nil {
		t.Fatal("invalid producer flag accepted")
	}
	t.Setenv("TRANSCRIPTION_WORKER_ENABLED", "invalid")
	if _, err := config.LoadTranscriptionWorker(); err == nil {
		t.Fatal("invalid worker flag accepted")
	}
}
