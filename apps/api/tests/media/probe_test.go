package media_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
)

// All real media is generated locally and tiny. No user uploads or S3 access.
func fixture(t *testing.T, path string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "0.25", "-threads", "1"}
	base = append(base, args...)
	base = append(base, path)
	if output, err := exec.CommandContext(ctx, "ffmpeg", base...).CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic fixture: %v: %s", err, output)
	}
}

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("install ffmpeg/ffprobe to run generated-media fixtures")
		}
	}
}

func TestProbeGeneratedMedia(t *testing.T) {
	requireTools(t)
	for _, tc := range []struct {
		name, ext, video, audio string
		options                 []string
	}{
		{"H264 AAC MP4", ".mp4", "libx264", "aac", nil},
		{"HEVC AAC MP4", ".mp4", "libx265", "aac", []string{"-x265-params", "pools=none:frame-threads=1:log-level=error"}},
		{"H264 PCM MOV", ".mov", "libx264", "pcm_s16le", nil},
		{"H264 MP3 MOV", ".mov", "libx264", "libmp3lame", nil},
		{"VP8 Vorbis WebM", ".webm", "libvpx", "libvorbis", []string{"-deadline", "realtime", "-cpu-used", "8"}},
		{"VP9 Opus WebM", ".webm", "libvpx-vp9", "libopus", []string{"-deadline", "realtime", "-cpu-used", "8"}},
		{"AV1 Opus WebM", ".webm", "libaom-av1", "libopus", []string{"-cpu-used", "8"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture"+tc.ext)
			args := append([]string{"-c:v", tc.video, "-c:a", tc.audio}, tc.options...)
			fixture(t, path, args...)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// A misleading extension must not change the detected format.
			actual := path + ".untrusted"
			if !filepath.IsAbs(path) || !filepath.IsAbs(actual) {
				t.Fatal("expected absolute test paths")
			}
			if err := os.Rename(path, actual); err != nil {
				t.Fatal(err)
			}
			out, err := media.ProbeFile(context.Background(), actual, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if out.DurationMs <= 0 || out.DurationMs > 1000 || out.SizeBytes != int64(len(before)) || len(out.Video) != 1 || len(out.Audio) != 1 || out.Video[0].Width != 64 || out.Video[0].Height != 48 {
				t.Fatalf("unexpected synthetic metadata: %+v", out)
			}
			after, err := os.ReadFile(actual)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("probe changed source bytes")
			}
		})
	}
}

func TestProbeRejectedMedia(t *testing.T) {
	requireTools(t)
	for _, tc := range []struct {
		name, ext, code string
		args            []string
	}{
		{"no audio", ".mp4", "AUDIO_REQUIRED", []string{"-an", "-c:v", "libx264"}},
		{"audio only", ".mp4", "VIDEO_REQUIRED", []string{"-vn", "-c:a", "aac"}},
		{"unsupported video", ".mp4", "UNSUPPORTED_VIDEO_CODEC", []string{"-c:v", "mpeg4", "-c:a", "aac"}},
		{"Matroska is not WebM", ".mkv", "UNSUPPORTED_MEDIA", []string{"-c:v", "libx264", "-c:a", "aac"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture"+tc.ext)
			fixture(t, path, tc.args...)
			_, err := media.ProbeFile(context.Background(), path, 10*time.Second)
			requireCode(t, err, tc.code)
		})
	}
	path := filepath.Join(t.TempDir(), "renamed.mp4")
	if err := os.WriteFile(path, []byte("not a video; private content must not appear in errors"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := media.ProbeFile(context.Background(), path, 10*time.Second)
	requireCode(t, err, "INVALID_MEDIA")
}

func TestProbeFileGuards(t *testing.T) {
	ctx := context.Background()
	if _, err := media.ProbeFile(ctx, "", 0); !errors.Is(err, media.ErrProbeConfig) {
		t.Fatal(err)
	}
	for _, path := range []string{"", "https://example.invalid/video.mp4", t.TempDir()} {
		if _, err := media.ProbeFile(ctx, path, time.Second); !errors.Is(err, media.ErrLocalFile) {
			t.Fatal(err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := media.ProbeFile(cancelled, "", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "empty.mp4")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := media.ProbeFile(ctx, path, time.Second)
	requireCode(t, err, "INVALID_MEDIA")
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate(media.MaxFileBytes + 1)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("create sparse oversized fixture failed")
	}
	_, err = media.ProbeFile(ctx, path, time.Second)
	requireCode(t, err, "FILE_TOO_LARGE")
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := media.ProbeFile(ctx, link, time.Second); !errors.Is(err, media.ErrLocalFile) {
		t.Fatal(err)
	}
}

func TestProbeRotationAndTimeout(t *testing.T) {
	requireTools(t)
	path := filepath.Join(t.TempDir(), "source.mp4")
	fixture(t, path, "-c:v", "libx264", "-c:a", "aac")
	rotated := path + ".rotated.mp4"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-display_rotation", "90", "-i", path, "-c", "copy", rotated).CombinedOutput(); err != nil {
		t.Fatalf("rotate fixture: %v %s", err, output)
	}
	out, err := media.ProbeFile(context.Background(), rotated, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if out.Video[0].RotationDegrees != 90 {
		t.Fatalf("rotation lost: %d", out.Video[0].RotationDegrees)
	}
	if _, err := media.ProbeFile(context.Background(), path, time.Nanosecond); !errors.Is(err, media.ErrProbeTimeout) {
		t.Fatalf("expected bounded probe timeout, got %v", err)
	}
}

func TestProbeUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.mp4")
	if err := os.WriteFile(path, []byte("synthetic input"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := media.ProbeFile(context.Background(), path, time.Second); !errors.Is(err, media.ErrProbeUnavailable) {
		t.Fatalf("expected missing executable error, got %v", err)
	}
}
