package preparation

import (
	"regexp"
	"strings"

	"github.com/krishkishore972/clip_ai/internal/media"
)

var (
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	outputPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Validate binds the published derivative to its trusted source and output
// namespace. It accepts signed source-time origins and never exposes storage
// locations in errors. Callers must keep output IDs distinct from claim tokens.
func (a *Artifacts) Validate(assetID, bucket, outputID string) error {
	if a == nil || !media.ValidAssetID(assetID) ||
		strings.TrimSpace(bucket) == "" || a.Bucket != bucket ||
		!outputPattern.MatchString(outputID) {
		return ErrArguments
	}
	audioKey := "dev-uploads/" + assetID + "/derived/v1/" + outputID + "/audio.wav"
	if a.AudioKey != audioKey ||
		!hashPattern.MatchString(a.AudioSHA256) ||
		!hashPattern.MatchString(a.SourceSHA256) ||
		a.AudioSizeBytes <= 0 || a.AudioSizeBytes > media.MaxPreparedAudioBytes ||
		a.AudioStartMs < -media.MaxAudioStartMs || a.AudioStartMs > media.MaxAudioStartMs {
		return ErrArguments
	}
	return nil
}
