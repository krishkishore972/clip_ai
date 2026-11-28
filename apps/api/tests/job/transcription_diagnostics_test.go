package job_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func TestTranscriptionLogsSafeValidationDiagnostic(t *testing.T) {
	for _, scenario := range []struct {
		name, stage, reason string
		wordIndex           bool
	}{
		{"response", "response", "OUTPUT_UNCLASSIFIED", false},
		{"duration", "normalization", "AUDIO_DURATION_MISMATCH", false},
		{"word", "normalization", "WORD_INTERVAL", true},
		{"audio bounds", "normalization", "WORD_AUDIO_BOUNDS", true},
		{"serialization", "serialization", "DOCUMENT_SIZE_LIMIT", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			claim, data, result := transcriptFixture(t)
			claim.Token, claim.OutputID = "private-claim-token", "private-output-id"
			provider := &transcriptProviderStub{result: result}
			switch scenario.name {
			case "response":
				provider.err = errors.Join(errors.New("private-credentials"), transcription.ErrResponse)
			case "duration":
				result.Metadata.Duration = 4
			case "word":
				word := &result.Results.Channels[0].Alternatives[0].Words[0]
				word.Word = "private-word"
				word.End = word.Start
			case "audio bounds":
				result.Results.Channels[0].Alternatives[0].Words[0].End = 3.5
			case "serialization":
				alt := &result.Results.Channels[0].Alternatives[0]
				alt.Words = make([]response.Word, 20000)
				for i := range alt.Words {
					alt.Words[i] = response.Word{
						Word: strings.Repeat("x", 512), PunctuatedWord: strings.Repeat("y", 512),
						Start: .1, End: .5, Confidence: .9,
					}
				}
			}
			store := &transcriptStoreStub{claim: claim}
			var logs bytes.Buffer
			handler, _ := transcriptHandler(t, store, processLogDownloader{data: data}, &transcriptUploaderStub{}, provider, &logs)
			task := asynq.NewTask(transcription.TaskType, []byte(`{"assetId":"asset-test"}`))
			if err := handler.ProcessTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			if store.pub != nil || store.code != "TRANSCRIPTION_INVALID_OUTPUT" || store.retry || store.finishes != 1 {
				t.Fatal("diagnostics changed failure or retry behavior")
			}
			found := 0
			decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
			for {
				var line map[string]any
				err := decoder.Decode(&line)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("invalid structured log: %v", err)
				}
				if line["msg"] != "transcription output validation failed" {
					continue
				}
				found++
				if line["validation_stage"] != scenario.stage || line["reason"] != scenario.reason ||
					line["asset_id"] != claim.AssetID || line["code"] != "TRANSCRIPTION_INVALID_OUTPUT" {
					t.Fatalf("wrong diagnostic fields: %+v", line)
				}
				_, hasIndex := line["word_index"]
				if hasIndex != scenario.wordIndex || hasIndex && line["word_index"] != float64(0) {
					t.Fatal("wrong word index")
				}
				if scenario.name == "duration" &&
					(line["expected_duration_ms"] != float64(1001) || line["observed_duration_ms"] != float64(4000)) {
					t.Fatal("safe duration facts missing")
				}
				for field, expected := range map[string]float64{
					"word_end_seconds": 3.5, "audio_duration_seconds": 1,
					"overrun_seconds": 2.5, "allowed_overrun_ms": 2000,
				} {
					value, present := line[field]
					if scenario.name == "audio bounds" {
						if !present || value != expected {
							t.Fatalf("wrong audio-bound fact %s: %v", field, value)
						}
					} else if present {
						t.Fatalf("unrelated failure included %s", field)
					}
				}
			}
			if found != 1 {
				t.Fatal("expected exactly one reason-specific diagnostic")
			}
			for _, secret := range []string{"private-credentials", "private-word", "Private text.", claim.AudioKey, claim.Token, claim.OutputID} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("private data in diagnostic: %q", secret)
				}
			}
		})
	}
}

func TestTranscriptionPublishesCorrectedEndsWithSafeDiagnostics(t *testing.T) {
	claim, data, result := transcriptFixture(t)
	alt := &result.Results.Channels[0].Alternatives[0]
	alt.Words[0].End = 1.2
	alt.Words = append(alt.Words, response.Word{
		Word: "private-second", Start: .9, End: 1.1, Confidence: .9,
	})
	store := &transcriptStoreStub{claim: claim}
	uploader := &transcriptUploaderStub{}
	var logs bytes.Buffer
	handler, _ := transcriptHandler(t, store, processLogDownloader{data: data}, uploader,
		&transcriptProviderStub{result: result}, &logs)
	task := asynq.NewTask(transcription.TaskType, []byte(`{"assetId":"asset-test"}`))
	if err := handler.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if store.pub == nil || store.code != "" || store.finishes != 1 {
		t.Fatal("corrected response was not published")
	}
	var doc transcription.Document
	if err := json.Unmarshal(uploader.data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Words) != 2 || doc.Words[0].EndMs != 993 || doc.Words[1].EndMs != 993 {
		t.Fatal("uploaded document did not preserve both corrected words")
	}
	found := 0
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var line map[string]any
		err := decoder.Decode(&line)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if line["msg"] == "transcription word ends corrected" {
			found++
			if line["correction_count"] != float64(2) || line["max_adjustment_ms"] != float64(200) {
				t.Fatal("wrong correction diagnostics")
			}
		}
	}
	if found != 1 {
		t.Fatal("expected one correction summary")
	}
	for _, secret := range []string{"Private text.", "private-second", claim.AudioKey, claim.Token} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("correction logs exposed private data")
		}
	}
}
