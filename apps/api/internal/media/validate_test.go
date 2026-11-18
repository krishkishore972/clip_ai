package media

import (
	"errors"
	"math"
	"testing"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

func validProbe() *ffprobe.ProbeData {
	return &ffprobe.ProbeData{
		Format: &ffprobe.Format{DurationSeconds: 10},
		Streams: []*ffprobe.Stream{
			{Index: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080, AvgFrameRate: "30000/1001"},
			{Index: 1, CodecType: "audio", CodecName: "aac", SampleRate: "48000", Channels: 2},
		},
	}
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Code != code {
		t.Fatalf("expected %s; got %v", code, err)
	}
}

func TestValidationPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*ffprobe.ProbeData)
		code   string
	}{
		{"valid", func(p *ffprobe.ProbeData) {}, ""},
		{"exact two hours", func(p *ffprobe.ProbeData) { p.Format.DurationSeconds = 7200 }, ""},
		{"over duration before rounding", func(p *ffprobe.ProbeData) { p.Format.DurationSeconds = 7200.0001 }, "DURATION_LIMIT"},
		{"stream over duration", func(p *ffprobe.ProbeData) { p.Streams[0].Duration = "7201" }, "DURATION_LIMIT"},
		{"zero duration", func(p *ffprobe.ProbeData) { p.Format.DurationSeconds = 0 }, "INVALID_MEDIA_METADATA"},
		{"negative duration", func(p *ffprobe.ProbeData) { p.Format.DurationSeconds = -1 }, "INVALID_MEDIA_METADATA"},
		{"nan duration", func(p *ffprobe.ProbeData) { p.Format.DurationSeconds = math.NaN() }, "INVALID_MEDIA_METADATA"},
		{"infinite duration", func(p *ffprobe.ProbeData) { p.Format.DurationSeconds = math.Inf(1) }, "INVALID_MEDIA_METADATA"},
		{"bad stream duration", func(p *ffprobe.ProbeData) { p.Streams[0].Duration = "NaN" }, "INVALID_MEDIA_METADATA"},
		{"DCI 4K", func(p *ffprobe.ProbeData) { p.Streams[0].Width = 4096; p.Streams[0].Height = 2160 }, ""},
		{"portrait 4K", func(p *ffprobe.ProbeData) { p.Streams[0].Width = 2160; p.Streams[0].Height = 4096 }, ""},
		{"UHD", func(p *ffprobe.ProbeData) { p.Streams[0].Width = 3840; p.Streams[0].Height = 2160 }, ""},
		{"square 4K", func(p *ffprobe.ProbeData) { p.Streams[0].Width = 4096; p.Streams[0].Height = 4096 }, "RESOLUTION_LIMIT"},
		{"long edge exceeded", func(p *ffprobe.ProbeData) { p.Streams[0].Width = 4097 }, "RESOLUTION_LIMIT"},
		{"short edge exceeded", func(p *ffprobe.ProbeData) { p.Streams[0].Width = 4096; p.Streams[0].Height = 2161 }, "RESOLUTION_LIMIT"},
		{"missing dimensions", func(p *ffprobe.ProbeData) { p.Streams[0].Width = 0 }, "INVALID_MEDIA_METADATA"},
		{"no audio", func(p *ffprobe.ProbeData) { p.Streams = p.Streams[:1] }, "AUDIO_REQUIRED"},
		{"audio only", func(p *ffprobe.ProbeData) { p.Streams = p.Streams[1:] }, "VIDEO_REQUIRED"},
		{"cover art only", func(p *ffprobe.ProbeData) { p.Streams[0].Disposition.AttachedPic = 1 }, "VIDEO_REQUIRED"},
		{"unsupported video", func(p *ffprobe.ProbeData) { p.Streams[0].CodecName = "mpeg4" }, "UNSUPPORTED_VIDEO_CODEC"},
		{"unsupported audio", func(p *ffprobe.ProbeData) { p.Streams[1].CodecName = "flac" }, "UNSUPPORTED_AUDIO_CODEC"},
		{"missing audio metadata", func(p *ffprobe.ProbeData) { p.Streams[1].SampleRate = "0" }, "INVALID_MEDIA_METADATA"},
		{"missing format", func(p *ffprobe.ProbeData) { p.Format = nil }, "INVALID_MEDIA_METADATA"},
		{"nil stream", func(p *ffprobe.ProbeData) { p.Streams = append(p.Streams, nil) }, "INVALID_MEDIA_METADATA"},
		{"invalid fps", func(p *ffprobe.ProbeData) { p.Streams[0].AvgFrameRate = "1/0" }, "INVALID_MEDIA_METADATA"},
		{"fps fallback", func(p *ffprobe.ProbeData) { p.Streams[0].AvgFrameRate = "0/0"; p.Streams[0].RFrameRate = "24/1" }, ""},
		{"later video exceeds limit", func(p *ffprobe.ProbeData) { s := *p.Streams[0]; s.Width = 8192; p.Streams = append(p.Streams, &s) }, "RESOLUTION_LIMIT"},
		{"later audio unsupported", func(p *ffprobe.ProbeData) { s := *p.Streams[1]; s.CodecName = "ac3"; p.Streams = append(p.Streams, &s) }, "UNSUPPORTED_AUDIO_CODEC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := validProbe()
			tc.mutate(p)
			_, err := validate(p, "mp4")
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataAndAllCodecs(t *testing.T) {
	t.Parallel()
	for _, video := range []string{"h264", "hevc", "vp8", "vp9", "av1"} {
		for _, audio := range []string{"aac", "mp3", "opus", "vorbis", "pcm_s16le", "pcm_s24be", "pcm_f32le"} {
			p := validProbe()
			p.Streams[0].CodecName = video
			p.Streams[1].CodecName = audio
			p.Format.DurationSeconds = 10.1234
			p.Streams[0].TagList = ffprobe.Tags{"rotate": "90"}
			out, err := validate(p, "mov")
			if err != nil {
				t.Fatal(err)
			}
			if out.DurationMs != 10123 || out.Video[0].FPS != (Rational{30000, 1001}) || out.Video[0].RotationDegrees != 90 {
				t.Fatal("metadata must retain integer timing, rational FPS and rotation")
			}
		}
	}
	for _, raw := range []string{"", "1", "0/1", "-1/1", "1/0", "1/-1", "NaN/1", "1/2/3", "9223372036854775808/1", "2147483648/1"} {
		if _, ok := frameRate(raw); ok {
			t.Fatalf("accepted invalid FPS %q", raw)
		}
	}
	if fps, ok := frameRate("60000/2002"); !ok || fps != (Rational{30000, 1001}) {
		t.Fatal("expected reduced rational FPS")
	}
	if _, err := validate(nil, "mp4"); err == nil {
		t.Fatal("accepted nil probe")
	}
	if _, err := validate(validProbe(), "avi"); err == nil {
		t.Fatal("accepted unsupported container")
	}
}

func TestWebMHeader(t *testing.T) {
	t.Parallel()
	valid := []byte{0x1a, 0x45, 0xdf, 0xa3, 0x87, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}
	if !webmHeader(valid) {
		t.Fatal("valid WebM DocType rejected")
	}
	for _, b := range [][]byte{nil, valid[:5], valid[:10], append([]byte("webm"), valid...), {0x1a, 0x45, 0xdf, 0xa3, 0xff}, {0x1a, 0x45, 0xdf, 0xa3, 0x80}, {0x1a, 0x45, 0xdf, 0xa3, 0x81, 0}} {
		if webmHeader(b) {
			t.Fatal("malformed EBML accepted")
		}
	}
}
