package curation_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/curation"
)

func resultFixture(t *testing.T, words string) (curation.Claim, []byte) {
	t.Helper()
	data := []byte(`{"schemaVersion":1,"assetId":"result-asset","sourceDurationMs":10000,"words":` + words + `}`)
	if !json.Valid(data) {
		t.Fatal("invalid test document")
	}
	hash := sha256.Sum256(data)
	return curation.Claim{
		AssetID: "result-asset", UserID: "owner", Token: "claim-one", Attempts: 1,
		TranscriptBucket: "private", TranscriptKey: "dev-uploads/result-asset/derived/v1/output/transcript.json",
		TranscriptSHA256: hex.EncodeToString(hash[:]), TranscriptSizeBytes: int64(len(data)),
		TranscriptWordCount: 3, SourceDurationMs: 10000,
		CandidateCount: 5, MinDurationMs: 100, MaxDurationMs: 5000,
		Model: curation.Model, PromptVersion: curation.PromptVersion,
	}, data
}

func proposal(start, end int) curation.Proposal {
	return curation.Proposal{
		StartWordIndex: start, EndWordIndexExclusive: end,
		Title: "Title", Hook: "Hook", Reason: "Reason",
	}
}

func TestValidateResult(t *testing.T) {
	t.Parallel()
	words := `[{"word":"one","startMs":0,"endMs":2000},{"word":"two","startMs":500,"endMs":1000},{"word":"three","startMs":1000,"endMs":3000}]`
	c, data := resultFixture(t, words)
	t.Run("overlap and decreasing ends allowed", func(t *testing.T) {
		if _, err := curation.ValidateResult(c, data, []curation.Proposal{proposal(0, 2), proposal(1, 3)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("maximum end determines duration", func(t *testing.T) {
		c := c
		c.MaxDurationMs = 1500
		if _, err := curation.ValidateResult(c, data, []curation.Proposal{proposal(0, 2)}); !errors.Is(err, curation.ErrResult) {
			t.Fatal("accepted range whose earlier word exceeds duration ceiling")
		}
	})
	for name, proposals := range map[string][]curation.Proposal{
		"negative index":   {proposal(-1, 2)},
		"empty range":      {proposal(1, 1)},
		"end past words":   {proposal(0, 4)},
		"duplicate ranges": {proposal(0, 2), proposal(0, 2)},
		"too many":         {proposal(0, 1), proposal(0, 2), proposal(0, 3), proposal(1, 2), proposal(1, 3), proposal(2, 3)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := curation.ValidateResult(c, data, proposals); !errors.Is(err, curation.ErrResult) {
				t.Fatal("invalid proposals accepted")
			}
		})
	}
	for name, text := range map[string]string{
		"blank": " \t\n", "NUL": "secret\x00", "invalid UTF8": "\xff",
		"long": strings.Repeat("x", 1001), "UTF16": strings.Repeat("😀", 501),
	} {
		for _, field := range []string{"title", "hook", "reason"} {
			t.Run(name+"/"+field, func(t *testing.T) {
				p := proposal(0, 2)
				switch field {
				case "title":
					p.Title = text
				case "hook":
					p.Hook = text
				case "reason":
					p.Reason = text
				}
				if _, err := curation.ValidateResult(c, data, []curation.Proposal{p}); !errors.Is(err, curation.ErrResult) {
					t.Fatal("invalid metadata accepted")
				}
			})
		}
	}
	t.Run("UTF16 exact boundary", func(t *testing.T) {
		p := proposal(0, 2)
		p.Title = strings.Repeat("😀", 500)
		if _, err := curation.ValidateResult(c, data, []curation.Proposal{p}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("artifact binding", func(t *testing.T) {
		for _, mutate := range []func(*curation.Claim){
			func(c *curation.Claim) { c.TranscriptSHA256 = strings.Repeat("d", 64) },
			func(c *curation.Claim) { c.TranscriptSizeBytes++ },
			func(c *curation.Claim) { c.AssetID = "other" },
			func(c *curation.Claim) { c.SourceDurationMs++ },
			func(c *curation.Claim) { c.TranscriptWordCount++ },
			func(c *curation.Claim) { c.CandidateCount = 0 },
			func(c *curation.Claim) { c.Model = "unapproved" },
			func(c *curation.Claim) { c.PromptVersion = "v2" },
		} {
			changed := c
			mutate(&changed)
			if _, err := curation.ValidateResult(changed, data, nil); !errors.Is(err, curation.ErrResult) {
				t.Fatal("changed binding accepted")
			}
		}
	})
	t.Run("empty document", func(t *testing.T) {
		c, data := resultFixture(t, `[]`)
		c.TranscriptWordCount = 0
		if _, err := curation.ValidateResult(c, data, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := curation.ValidateResult(c, data, []curation.Proposal{proposal(0, 1)}); !errors.Is(err, curation.ErrResult) {
			t.Fatal("empty transcript produced candidate")
		}
	})
	t.Run("artifact parser fails closed even with matching digest", func(t *testing.T) {
		for _, changed := range [][]byte{
			[]byte(strings.Replace(string(data), `"schemaVersion":1`, `"schemaVersion":2`, 1)),
			[]byte(strings.Replace(string(data), `"assetId":"result-asset"`, `"assetId":"other"`, 1)),
			append(append([]byte{}, data...), []byte(` {}`)...),
			[]byte(`{"schemaVersion":1,"words":`),
			[]byte(strings.Replace(string(data), `"one"`, "\"\xff\"", 1)),
		} {
			c := c
			hash := sha256.Sum256(changed)
			c.TranscriptSHA256 = hex.EncodeToString(hash[:])
			c.TranscriptSizeBytes = int64(len(changed))
			if _, err := curation.ValidateResult(c, changed, nil); !errors.Is(err, curation.ErrResult) {
				t.Fatal("malformed or wrong-identity artifact accepted")
			}
		}
	})
	t.Run("candidate minimum duration", func(t *testing.T) {
		c := c
		c.MinDurationMs = 2500
		if _, err := curation.ValidateResult(c, data, []curation.Proposal{proposal(0, 2)}); !errors.Is(err, curation.ErrResult) {
			t.Fatal("short candidate accepted")
		}
	})
	for name, words := range map[string]string{
		"negative selected start": `[{"word":"one","startMs":-1,"endMs":1000}]`,
		"missing timing":          `[{"word":"one","endMs":1000}]`,
		"blank text":              `[{"word":" ","startMs":0,"endMs":1000}]`,
		"long word":               `[{"word":"` + strings.Repeat("x", 513) + `","startMs":0,"endMs":1000}]`,
		"nonpositive interval":    `[{"word":"one","startMs":1000,"endMs":1000}]`,
		"out of source":           `[{"word":"one","startMs":0,"endMs":10001}]`,
		"unordered":               `[{"word":"one","startMs":10,"endMs":1000},{"word":"two","startMs":0,"endMs":1000}]`,
	} {
		t.Run(name, func(t *testing.T) {
			c, data := resultFixture(t, words)
			c.TranscriptWordCount = 1
			if name == "unordered" {
				c.TranscriptWordCount = 2
			}
			if _, err := curation.ValidateResult(c, data, []curation.Proposal{proposal(0, 1)}); !errors.Is(err, curation.ErrResult) {
				t.Fatal("invalid word selection accepted")
			}
		})
	}
}
