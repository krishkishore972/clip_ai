package upload_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

func TestUploadClipPreferencesValidation(t *testing.T) {
	t.Parallel()
	for name, preferences := range map[string]upload.ClipPreferences{
		"missing":      {},
		"too many":     {CandidateCount: 6, MinDurationMs: 15000, MaxDurationMs: 60000},
		"zero count":   {CandidateCount: 0, MinDurationMs: 15000, MaxDurationMs: 60000},
		"negative":     {CandidateCount: 1, MinDurationMs: -1, MaxDurationMs: 60000},
		"inverted":     {CandidateCount: 1, MinDurationMs: 60000, MaxDurationMs: 15000},
		"over ceiling": {CandidateCount: 1, MinDurationMs: 15000, MaxDurationMs: 60001},
	} {
		t.Run(name, func(t *testing.T) {
			s, f := testService(t)
			_, err := s.Create(ownerContext(), upload.CreateUploadRequest{
				Filename: "sample.mp4", SizeBytes: 1024, ContentType: "video/mp4",
				ClipPreferences: preferences,
			})
			if !errors.Is(err, upload.ErrInvalid) || len(f.calls) != 0 {
				t.Fatal("invalid preferences reached S3")
			}
		})
	}
	if !(upload.ClipPreferences{CandidateCount: 1, MinDurationMs: 1, MaxDurationMs: 1}).Valid() {
		t.Fatal("inclusive minimum bounds rejected")
	}
}

func TestSignedPreferencesReachCompletion(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	expected := upload.ClipPreferences{CandidateCount: 2, MinDurationMs: 12000, MaxDurationMs: 45000}
	state, err := s.Create(ownerContext(), upload.CreateUploadRequest{
		Filename: "sample.mp4", SizeBytes: 1024, ContentType: "video/mp4",
		ClipPreferences: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := decodeTokenFixture(t, state.UploadToken, testConfig().SigningSecret)
	raw, err := json.Marshal(claims["clipPreferences"])
	if err != nil {
		t.Fatal(err)
	}
	var signed upload.ClipPreferences
	if err := json.Unmarshal(raw, &signed); err != nil || signed != expected {
		t.Fatal("new capability does not bind the user's exact preferences")
	}
	f.parts = []storage.Part{{Number: 1, ETag: "tag", SizeBytes: 1024}}
	in := upload.CompleteUploadRequest{Parts: []upload.CompleteUploadPart{{PartNumber: 1, ETag: "tag"}}}
	if _, err := s.Complete(ownerContext(), accessFor(state), in); err != nil {
		t.Fatal(err)
	}
	if len(f.records.saved) != 1 || f.records.saved[0].ClipPreferences != expected {
		t.Fatal("completion did not persist signed preferences")
	}
	f.records.err = upload.ErrConflict
	if _, err := s.Complete(ownerContext(), accessFor(state), in); !errors.Is(err, upload.ErrConflict) {
		t.Fatal("completion must preserve preference conflict classification")
	}
}

func TestClipPreferenceTokenTamperingAndRequiredField(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, 1024)
	claims := decodeTokenFixture(t, state.UploadToken, testConfig().SigningSecret)
	claims["clipPreferences"] = map[string]any{
		"candidateCount": 6, "minDurationMs": 15000, "maxDurationMs": 60000,
	}
	data, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	_, signature, _ := strings.Cut(state.UploadToken, ".")
	tampered := base64.RawURLEncoding.EncodeToString(data) + "." + signature
	before := len(f.calls)
	for _, token := range []string{
		tampered,
		signTokenFixture(t, claims, testConfig().SigningSecret),
	} {
		if _, err := s.Get(ownerContext(), upload.Access{UploadID: state.UploadID, Token: token}); !errors.Is(err, upload.ErrToken) {
			t.Fatal("tampered or invalid signed preferences accepted")
		}
	}
	if len(f.calls) != before {
		t.Fatal("invalid preferences reached S3")
	}
	delete(claims, "clipPreferences")
	missing := upload.Access{
		UploadID: state.UploadID,
		Token:    signTokenFixture(t, claims, testConfig().SigningSecret),
	}
	if _, err := s.Get(ownerContext(), missing); !errors.Is(err, upload.ErrToken) {
		t.Fatal("capability without preferences accepted")
	}
	if len(f.calls) != before {
		t.Fatal("preference-less capability reached S3")
	}
}
