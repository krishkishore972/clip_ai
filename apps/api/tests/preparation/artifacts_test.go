package preparation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

func TestPreparationArtifactsValidation(t *testing.T) {
	outputID := strings.Repeat("a", 32)
	valid := preparation.Artifacts{
		Bucket:         "fixture-bucket",
		AudioKey:       "dev-uploads/asset/derived/v1/" + outputID + "/audio.wav",
		AudioSHA256:    strings.Repeat("b", 64),
		SourceSHA256:   strings.Repeat("c", 64),
		AudioSizeBytes: 1024,
		AudioStartMs:   -7,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*preparation.Artifacts)
		valid  bool
	}{
		{name: "signed origin", valid: true},
		{name: "positive origin", mutate: func(a *preparation.Artifacts) { a.AudioStartMs = media.MaxAudioStartMs }, valid: true},
		{name: "negative boundary", mutate: func(a *preparation.Artifacts) { a.AudioStartMs = -media.MaxAudioStartMs }, valid: true},
		{name: "wrong bucket", mutate: func(a *preparation.Artifacts) { a.Bucket = "other-bucket" }},
		{name: "original key", mutate: func(a *preparation.Artifacts) { a.AudioKey = "dev-uploads/asset/source.mp4" }},
		{name: "other asset", mutate: func(a *preparation.Artifacts) {
			a.AudioKey = strings.ReplaceAll(a.AudioKey, "/asset/", "/other/")
		}},
		{name: "other output", mutate: func(a *preparation.Artifacts) {
			a.AudioKey = strings.ReplaceAll(a.AudioKey, outputID, strings.Repeat("d", 32))
		}},
		{name: "invalid audio hash", mutate: func(a *preparation.Artifacts) { a.AudioSHA256 = "invalid" }},
		{name: "invalid source hash", mutate: func(a *preparation.Artifacts) { a.SourceSHA256 = "invalid" }},
		{name: "empty output", mutate: func(a *preparation.Artifacts) { a.AudioSizeBytes = 0 }},
		{name: "oversized output", mutate: func(a *preparation.Artifacts) { a.AudioSizeBytes = media.MaxPreparedAudioBytes + 1 }},
		{name: "origin too positive", mutate: func(a *preparation.Artifacts) { a.AudioStartMs = media.MaxAudioStartMs + 1 }},
		{name: "origin too negative", mutate: func(a *preparation.Artifacts) { a.AudioStartMs = -media.MaxAudioStartMs - 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifacts := valid
			if tc.mutate != nil {
				tc.mutate(&artifacts)
			}
			err := artifacts.Validate("asset", "fixture-bucket", outputID)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, preparation.ErrArguments) {
				t.Fatalf("invalid artifacts accepted: %v", err)
			}
		})
	}
	var absent *preparation.Artifacts
	if err := absent.Validate("asset", "fixture-bucket", outputID); !errors.Is(err, preparation.ErrArguments) {
		t.Fatalf("nil artifacts accepted: %v", err)
	}
	if err := valid.Validate("asset", "fixture-bucket", "invalid-output"); !errors.Is(err, preparation.ErrArguments) {
		t.Fatalf("invalid output namespace accepted: %v", err)
	}
}
