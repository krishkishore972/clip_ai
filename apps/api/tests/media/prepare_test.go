package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
)

func prepareFixture(t *testing.T, path string, extra ...string) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-n", "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=24:duration=1.2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1.2", "-c:v", "libx264", "-threads", "2", "-c:a", "aac"}
	args = append(args, extra...)
	args = append(args, path)
	fixtureCommand(t, args...)
}
func fixtureCommand(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture command failed: %v %s", err, out)
	}
}
func privatePreparationDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestPrepareRealFixtures(t *testing.T) {
	requireTools(t)
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{name: "landscape"},
		{name: "portrait", extra: []string{"-vf", "transpose=clock"}},
		{name: "VFR", extra: []string{"-vf", "select='not(eq(mod(n,3),1))'", "-fps_mode", "vfr"}},
		{name: "non-square pixels", extra: []string{"-vf", "setsar=2"}},
		{name: "720p cap", extra: []string{"-vf", "scale=1440:864"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "original.mp4")
			prepareFixture(t, path, tc.extra...)
			out := assertPrepared(t, path)
			if out.Audio.StartMs != 0 {
				t.Fatalf("zero-origin fixture reported %d ms", out.Audio.StartMs)
			}
		})
	}
}
func assertPrepared(t *testing.T, path string) media.PreparedMedia {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(before)
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	outputDir := privatePreparationDir(t)
	output, err := media.PrepareFile(context.Background(), path, outputDir, 20*time.Second)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(after) != sum || output.SourceSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("original changed or source checksum incorrect")
	}
	if output.Audio.Path != filepath.Join(outputDir, "audio.wav") {
		t.Fatal("audio output must be a direct child of the caller attempt directory")
	}
	if output.Audio.Channels != 1 || output.Audio.SampleRate != 16000 || output.Audio.DurationMs <= 0 {
		t.Fatalf("wrong metadata: %+v", output)
	}
	b, err := os.ReadFile(output.Audio.Path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(b)
	if output.Audio.Path == path || output.Audio.SizeBytes != int64(len(b)) || output.Audio.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("wrong output bytes/checksum")
	}
	info, err := os.Stat(output.Audio.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("non-private output")
	}
	return output
}
func TestPrepareRotationAndOffsets(t *testing.T) {
	requireTools(t)
	t.Run("rotation", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "source.mp4")
		rotated := filepath.Join(t.TempDir(), "rotated.mp4")
		prepareFixture(t, source)
		fixtureCommand(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-display_rotation", "90", "-i", source, "-c", "copy", rotated)
		assertPrepared(t, rotated)
	})
	for _, late := range []string{"audio", "video"} {
		t.Run(late+" starts late", func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "offset.mp4")
			args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
			if late == "video" {
				args = append(args, "-itsoffset", "0.4")
			}
			args = append(args, "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=30:duration=1.2")
			if late == "audio" {
				args = append(args, "-itsoffset", "0.4")
			}
			args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1.2", "-c:v", "libx264", "-threads", "2", "-c:a", "aac", source)
			fixtureCommand(t, args...)
			out := assertPrepared(t, source)
			if out.Audio.StartMs != 0 {
				t.Fatalf("offset fixture reported %d ms origin", out.Audio.StartMs)
			}
			// Inspect decoded PCM, not just container metadata: a delayed audio
			// stream must keep leading silence on the shared source timeline.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			samples, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-i", out.Audio.Path, "-f", "s16le", "-c:a", "pcm_s16le", "pipe:1").Output()
			if err != nil {
				t.Fatal(err)
			}
			energy := func(start, end int) int64 {
				var total int64
				for i := start * 32; i < end*32 && i+1 < len(samples); i += 2 {
					x := int16(uint16(samples[i]) | uint16(samples[i+1])<<8)
					if x < 0 {
						total -= int64(x)
					} else {
						total += int64(x)
					}
				}
				return total
			}
			if late == "audio" && (energy(0, 200) > 1000 || energy(700, 900) < 100000) {
				t.Fatal("delayed audio lost shared-timeline silence")
			}
			if late == "video" && energy(0, 200) < 100000 {
				t.Fatal("early audio incorrectly delayed")
			}
		})
	}
}
func TestPreparePermanentRejectionsAndGuards(t *testing.T) {
	requireTools(t)
	root := privatePreparationDir(t)
	corrupt := filepath.Join(t.TempDir(), "corrupt.mp4")
	if err := os.WriteFile(corrupt, []byte("private corrupt input"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := media.PrepareFile(context.Background(), corrupt, root, 10*time.Second)
	requireCode(t, err, "INVALID_MEDIA")
	missingAudio := filepath.Join(t.TempDir(), "no-audio.mp4")
	prepareFixture(t, missingAudio, "-an")
	_, err = media.PrepareFile(context.Background(), missingAudio, root, 10*time.Second)
	requireCode(t, err, "AUDIO_REQUIRED")
	if _, err := media.PrepareFile(context.Background(), "https://example.invalid/source", root, time.Second); !errors.Is(err, media.ErrLocalFile) {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.mp4")
	prepareFixture(t, source)
	_, err = media.PrepareFile(context.Background(), source, root, time.Nanosecond)
	if !errors.Is(err, media.ErrPreparationTimeout) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = media.PrepareFile(ctx, source, root, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.mp4")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	_, err = media.PrepareFile(context.Background(), link, root, time.Second)
	if !errors.Is(err, media.ErrLocalFile) {
		t.Fatal(err)
	}
}

func TestPrepareAttachedArtworkAndAbsoluteOrigin(t *testing.T) {
	requireTools(t)
	t.Run("attached cover is not main video", func(t *testing.T) {
		root := t.TempDir()
		source, cover, combined := filepath.Join(root, "source.mp4"), filepath.Join(root, "cover.png"), filepath.Join(root, "combined.mp4")
		prepareFixture(t, source)
		fixtureCommand(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "color=red:size=32x32", "-frames:v", "1", "-threads", "1", cover)
		fixtureCommand(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", source, "-i", cover, "-map", "1:v", "-map", "0:v", "-map", "0:a", "-c", "copy", "-disposition:v:0", "attached_pic", combined)
		assertPrepared(t, combined)
	})
	t.Run("shared nonzero origin", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "offset.mp4")
		prepareFixture(t, source, "-output_ts_offset", "10")
		out := assertPrepared(t, source)
		if out.Audio.DurationMs < 1100 || out.Audio.DurationMs > 1300 {
			t.Fatalf("absolute source origin padded output: %+v", out)
		}
		// AAC priming places the earliest audio sample slightly before the
		// nominal 10s video offset, so the recorded origin is the real
		// format.start_time rather than the requested constant.
		if out.Audio.StartMs < 9900 || out.Audio.StartMs > 10000 {
			t.Fatalf("source origin not recorded: %+v", out.Audio)
		}
	})
}

func TestPrepareRefusesExistingDestinationAndSourceCollision(t *testing.T) {
	requireTools(t)
	source := filepath.Join(t.TempDir(), "source.mp4")
	prepareFixture(t, source)
	for _, name := range []string{"audio.wav"} {
		t.Run(name, func(t *testing.T) {
			root := privatePreparationDir(t)
			destination := filepath.Join(root, name)
			sentinel := []byte("existing private output must remain unchanged")
			if err := os.WriteFile(destination, sentinel, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := media.PrepareFile(context.Background(), source, root, 10*time.Second); !errors.Is(err, media.ErrPreparationConfig) {
				t.Fatalf("expected existing-destination refusal: %v", err)
			}
			after, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(after, sentinel) {
				t.Fatal("existing destination changed")
			}
			original, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			collisionRoot := privatePreparationDir(t)
			collision := filepath.Join(collisionRoot, name)
			if err := os.WriteFile(collision, original, 0400); err != nil {
				t.Fatal(err)
			}
			if _, err := media.PrepareFile(context.Background(), collision, collisionRoot, 10*time.Second); !errors.Is(err, media.ErrPreparationConfig) {
				t.Fatalf("expected source-collision refusal: %v", err)
			}
			after, err = os.ReadFile(collision)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("colliding original changed")
			}
		})
	}
}

func TestPrepareRejectsIntactHeaderTruncatedPayload(t *testing.T) {
	requireTools(t)
	source := filepath.Join(t.TempDir(), "faststart.mp4")
	prepareFixture(t, source, "-movflags", "+faststart")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	moov, mdat := bytes.Index(data, []byte("moov")), bytes.Index(data, []byte("mdat"))
	if moov < 0 || mdat < moov || mdat+8 >= len(data) {
		t.Fatal("fixture must have an intact faststart header and media payload")
	}
	damaged := filepath.Join(t.TempDir(), "truncated.mp4")
	// Keep the complete moov/sample tables but cut within mdat. FFprobe can
	// still report authoritative-looking header durations despite missing packets.
	if err := os.WriteFile(damaged, data[:mdat+(len(data)-mdat)/2], 0400); err != nil {
		t.Fatal(err)
	}
	metadata, err := media.ProbeFile(context.Background(), damaged, 10*time.Second)
	if err != nil || metadata.DurationMs <= 0 || len(metadata.Video) != 1 || len(metadata.Audio) != 1 {
		t.Fatalf("fixture must pass metadata validation before full decode: %+v %v", metadata, err)
	}
	before, err := os.ReadFile(damaged)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := media.PrepareFile(context.Background(), damaged, privatePreparationDir(t), 10*time.Second); err == nil {
		t.Fatalf("truncated source was concealed/padded into a successful output: %+v", output)
	}
	after, err := os.ReadFile(damaged)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("damaged original changed")
	}
}
