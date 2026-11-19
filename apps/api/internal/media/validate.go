// Package media inspects immutable local media before downstream processing.
package media

import (
	"math"
	"strconv"
	"strings"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

const (
	MaxDurationSeconds       = 2 * 60 * 60
	MaxLongEdge              = 4096
	MaxShortEdge             = 2160
	MaxFileBytes       int64 = 2_000_000_000
)

// Rejection is a permanent policy failure, unlike a probe infrastructure error.
// It intentionally contains no filename, probe stderr, or user content.
type Rejection struct{ Code string }

func (r *Rejection) Error() string { return r.Code }

func reject(code string) error { return &Rejection{Code: code} }

type Rational struct{ Num, Den int64 }

type VideoStream struct {
	Index           int
	Codec           string
	Width, Height   int      // Coded dimensions; rotation is preserved separately.
	FPS             Rational // Average rate where available; not a claim of constant FPS.
	RotationDegrees int
}

type AudioStream struct {
	Index                int
	Codec                string
	SampleRate, Channels int
}

type Metadata struct {
	Container  string
	SizeBytes  int64
	DurationMs int64
	Video      []VideoStream
	Audio      []AudioStream
}

// ValidateProbeData applies media policy to probe metadata for an identified container.
func ValidateProbeData(data *ffprobe.ProbeData, container string) (Metadata, error) {
	var out Metadata
	if data == nil || data.Format == nil {
		return out, reject("INVALID_MEDIA_METADATA")
	}
	if container != "mp4" && container != "mov" && container != "webm" {
		return out, reject("UNSUPPORTED_MEDIA")
	}
	duration := data.Format.DurationSeconds
	if !validDuration(duration) {
		return out, reject("INVALID_MEDIA_METADATA")
	}
	if duration > MaxDurationSeconds {
		return out, reject("DURATION_LIMIT")
	}
	for _, stream := range data.Streams {
		if stream == nil {
			return Metadata{}, reject("INVALID_MEDIA_METADATA")
		}
		// Known stream durations must also obey the limit (not only the header).
		if stream.Duration != "" && stream.Duration != "N/A" {
			d, err := strconv.ParseFloat(stream.Duration, 64)
			if err != nil || !validDuration(d) {
				return Metadata{}, reject("INVALID_MEDIA_METADATA")
			}
			if d > MaxDurationSeconds {
				return Metadata{}, reject("DURATION_LIMIT")
			}
			duration = max(duration, d)
		}
		switch stream.CodecType {
		case "video":
			if stream.Disposition.AttachedPic != 0 {
				continue
			}
			switch stream.CodecName {
			case "h264", "hevc", "vp8", "vp9", "av1":
			default:
				return Metadata{}, reject("UNSUPPORTED_VIDEO_CODEC")
			}
			if stream.Width <= 0 || stream.Height <= 0 {
				return Metadata{}, reject("INVALID_MEDIA_METADATA")
			}
			if max(stream.Width, stream.Height) > MaxLongEdge || min(stream.Width, stream.Height) > MaxShortEdge {
				return Metadata{}, reject("RESOLUTION_LIMIT")
			}
			rate := stream.AvgFrameRate
			if rate == "" || rate == "0/0" || rate == "N/A" {
				rate = stream.RFrameRate
			}
			fps, ok := ParseFrameRate(rate)
			if !ok {
				return Metadata{}, reject("INVALID_MEDIA_METADATA")
			}
			rotation := 0
			if matrix, err := stream.SideDataList.GetDisplayMatrix(); err == nil {
				rotation = matrix.Rotation
			} else if tag, err := stream.TagList.GetString("rotate"); err == nil {
				var err error
				rotation, err = strconv.Atoi(tag)
				if err != nil {
					return Metadata{}, reject("INVALID_MEDIA_METADATA")
				}
			}
			out.Video = append(out.Video, VideoStream{
				Index: stream.Index, Codec: stream.CodecName, Width: stream.Width, Height: stream.Height, FPS: fps, RotationDegrees: rotation,
			})
		case "audio":
			switch stream.CodecName {
			case "aac", "mp3", "opus", "vorbis":
			default:
				if !strings.HasPrefix(stream.CodecName, "pcm_") {
					return Metadata{}, reject("UNSUPPORTED_AUDIO_CODEC")
				}
			}
			sampleRate, err := strconv.Atoi(stream.SampleRate)
			if err != nil || sampleRate <= 0 || stream.Channels <= 0 {
				return Metadata{}, reject("INVALID_MEDIA_METADATA")
			}
			out.Audio = append(out.Audio, AudioStream{Index: stream.Index, Codec: stream.CodecName, SampleRate: sampleRate, Channels: stream.Channels})
		}
	}
	if len(out.Video) == 0 {
		return Metadata{}, reject("VIDEO_REQUIRED")
	}
	if len(out.Audio) == 0 {
		return Metadata{}, reject("AUDIO_REQUIRED")
	}
	out.Container = container
	// Check the true duration before rounding to integer milliseconds.
	out.DurationMs = int64(math.Round(duration * 1000))
	if out.DurationMs <= 0 {
		return Metadata{}, reject("INVALID_MEDIA_METADATA")
	}
	return out, nil
}

func validDuration(d float64) bool { return d > 0 && !math.IsNaN(d) && !math.IsInf(d, 0) }

// ParseFrameRate parses and reduces a positive, bounded rational frame rate.
func ParseFrameRate(raw string) (Rational, bool) {
	num, den, ok := strings.Cut(raw, "/")
	if !ok {
		return Rational{}, false
	}
	n, e1 := strconv.ParseInt(num, 10, 64)
	d, e2 := strconv.ParseInt(den, 10, 64)
	if e1 != nil || e2 != nil || n <= 0 || d <= 0 {
		return Rational{}, false
	}
	a, b := n, d
	for b != 0 {
		a, b = b, a%b
	}
	n, d = n/a, d/a
	if n > math.MaxInt32 || d > math.MaxInt32 {
		return Rational{}, false
	}
	return Rational{Num: n, Den: d}, true
}
