package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const MaxPreparedAudioBytes int64 = 240_000_000

var (
	ErrPreparationUnavailable = errors.New("media: preparation executable unavailable")
	ErrPreparationFailed      = errors.New("media: preparation failed")
	ErrPreparationTimeout     = errors.New("media: preparation deadline exceeded")
	ErrPreparationConfig      = errors.New("media: invalid preparation configuration")
)

type PreparedAudio struct {
	Path                 string
	SizeBytes            int64
	SHA256               string
	DurationMs           int64
	SampleRate, Channels int
}

type PreparedMedia struct {
	Audio        PreparedAudio
	SourceSHA256 string
}

// PrepareFile creates audio.wav directly in a private caller-owned attempt
// directory. The name must be absent; an existing file is never replaced.
// The source and all parent directories must belong to the isolated worker, not
// an untrusted concurrent filesystem writer. The original is only opened
// read-only. The caller owns cleanup of the returned file and any failed
// attempt directory.
func PrepareFile(ctx context.Context, sourcePath, outputDir string, timeout time.Duration) (out PreparedMedia, result error) {
	if timeout <= 0 || outputDir == "" {
		return out, ErrPreparationConfig
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	source, err := filepath.Abs(sourcePath)
	if err != nil || sourcePath == "" {
		return out, ErrLocalFile
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return out, ErrLocalFile
	}
	root, err := filepath.Abs(outputDir)
	if err != nil {
		return out, ErrPreparationConfig
	}
	dirInfo, err := os.Lstat(root)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || dirInfo.Mode().Perm()&0077 != 0 {
		return out, ErrPreparationConfig
	}
	audioPath := filepath.Join(root, "audio.wav")
	// The worker provides one unique attempt directory. Refuse an existing
	// destination (including a source alias or symlink). FFmpeg -n
	// independently protects against an intervening creation.
	if _, err := os.Lstat(audioPath); !errors.Is(err, os.ErrNotExist) {
		return out, ErrPreparationConfig
	}
	if info.Size() <= 0 {
		return out, reject("INVALID_MEDIA")
	}
	before, _, err := preparationHash(ctx, source, MaxFileBytes)
	if err != nil {
		return out, err
	}
	defer func() {
		// Check even after encoding failure, but honor cancellation rather than
		// spending unbounded time reading a source after the job deadline.
		after, _, hashErr := preparationHash(ctx, source, MaxFileBytes)
		current, statErr := os.Lstat(source)
		if ctx.Err() != nil {
			out = PreparedMedia{}
			result = preparationContextError(ctx)
			return
		}
		if hashErr != nil || statErr != nil || !os.SameFile(info, current) || before != after {
			out = PreparedMedia{}
			result = reject("SOURCE_CHANGED")
		}
	}()
	metadata, err := ProbeFile(ctx, source, min(timeout, time.Minute))
	if err != nil {
		return out, err
	}
	if len(metadata.Video) == 0 || len(metadata.Audio) == 0 {
		return out, reject("INVALID_MEDIA")
	}
	details, err := preparationProbe(ctx, source, "mov,matroska,webm")
	if err != nil {
		return out, err
	}
	video := metadata.Video[0]
	audio := metadata.Audio[0]
	durationMs, err := preparationTimeline(details, video.Index, audio.Index, metadata.DurationMs)
	if err != nil {
		return out, err
	}
	duration := strconv.FormatFloat(float64(durationMs)/1000, 'f', 3, 64)
	audioFilter := "aresample=16000:async=1:first_pts=0,apad=whole_dur=" + duration + ",atrim=duration=" + duration
	inputArgs := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-n", "-xerror",
		"-err_detect", "explode",
		"-max_alloc", "67108864",
		"-filter_threads", "2", "-filter_complex_threads", "2", "-threads", "2",
		"-protocol_whitelist", "file", "-format_whitelist", "mov,matroska,webm",
		"-copyts", "-start_at_zero", "-i", source,
	}
	// The output is audio-only, but preparation must still strictly decode the
	// selected video stream so damaged video packets cannot be hidden by the
	// later ASR-only derivative.
	videoArgs := append(append([]string{}, inputArgs...),
		"-map", fmt.Sprintf("0:%d", video.Index), "-an", "-sn", "-dn", "-f", "null", "-",
	)
	if err := preparationCommand(ctx, "ffmpeg", videoArgs, nil); err != nil {
		return out, err
	}
	audioArgs := append(append([]string{}, inputArgs...),
		"-map", fmt.Sprintf("0:%d", audio.Index),
		"-map_metadata", "-1", "-map_chapters", "-1", "-vn", "-af", audioFilter,
		"-c:a", "pcm_s16le", "-threads:a", "2", "-ar", "16000", "-ac", "1",
		"-t", duration, "-fs", strconv.FormatInt(MaxPreparedAudioBytes, 10), "-f", "wav", audioPath,
	)
	if err := preparationCommand(ctx, "ffmpeg", audioArgs, nil); err != nil {
		return out, err
	}
	audioProbe, err := preparationProbe(ctx, audioPath, "wav")
	if err != nil {
		return out, err
	}
	audioDuration, err := verifyAudio(audioProbe, durationMs)
	if err != nil {
		return out, err
	}
	audioHash, audioSize, err := preparationHash(ctx, audioPath, MaxPreparedAudioBytes)
	if err != nil {
		return out, err
	}
	if os.Chmod(audioPath, 0600) != nil {
		return out, ErrPreparationFailed
	}
	return PreparedMedia{SourceSHA256: before, Audio: PreparedAudio{Path: audioPath, SizeBytes: audioSize, SHA256: audioHash, DurationMs: audioDuration, SampleRate: 16000, Channels: 1}}, nil
}

// cappedPreparationBuffer continues draining subprocess output after its cap.
// Neither subprocess stderr nor filenames escape through returned errors.
type cappedPreparationBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedPreparationBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > b.limit-b.data.Len() {
		b.overflow = true
		p = p[:max(0, b.limit-b.data.Len())]
	}
	_, _ = b.data.Write(p)
	return n, nil
}
func preparationContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrPreparationTimeout
	}
	return ctx.Err()
}
func preparationCommand(ctx context.Context, binary string, args []string, stdout io.Writer) error {
	if ctx.Err() != nil {
		return preparationContextError(ctx)
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return ErrPreparationUnavailable
	}
	command := exec.CommandContext(ctx, path, args...)
	command.WaitDelay = 2 * time.Second
	command.Stdin = nil
	command.Stdout = stdout
	command.Stderr = &cappedPreparationBuffer{limit: 32 * 1024}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return preparationContextError(ctx)
		}
		return ErrPreparationFailed
	}
	return nil
}
func preparationHash(ctx context.Context, path string, capBytes int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, ErrPreparationFailed
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return "", 0, ErrPreparationFailed
	}
	if info.Size() > capBytes {
		return "", 0, reject("PREPARATION_SIZE_LIMIT")
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if ctx.Err() != nil {
			return "", 0, preparationContextError(ctx)
		}
		n, readErr := f.Read(buffer)
		total += int64(n)
		if total > capBytes {
			return "", 0, reject("PREPARATION_SIZE_LIMIT")
		}
		_, _ = hash.Write(buffer[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, ErrPreparationFailed
		}
	}
	if total != info.Size() {
		return "", 0, reject("SOURCE_CHANGED")
	}
	return hex.EncodeToString(hash.Sum(nil)), total, nil
}

type preparationStream struct {
	Index      int    `json:"index"`
	CodecType  string `json:"codec_type"`
	CodecName  string `json:"codec_name"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	SampleRate string `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Duration   string `json:"duration"`
	StartTime  string `json:"start_time"`
}
type preparationProbeData struct {
	Streams []preparationStream `json:"streams"`
	Format  struct {
		Duration  string `json:"duration"`
		StartTime string `json:"start_time"`
	} `json:"format"`
}

func preparationProbe(ctx context.Context, path, formats string) (preparationProbeData, error) {
	var data preparationProbeData
	probeCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	output := &cappedPreparationBuffer{limit: 128 * 1024}
	err := preparationCommand(probeCtx, "ffprobe", []string{"-v", "error", "-protocol_whitelist", "file", "-format_whitelist", formats, "-max_alloc", "67108864", "-show_entries", "format=duration,start_time:stream=index,codec_type,codec_name,width,height,duration,start_time,sample_rate,channels", "-of", "json", path}, output)
	if err != nil {
		return data, err
	}
	if output.overflow || json.Unmarshal(output.data.Bytes(), &data) != nil {
		return data, reject("PREPARATION_INVALID_OUTPUT")
	}
	return data, nil
}
func preparationDuration(raw string) int64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || !validDuration(value) {
		return 0
	}
	return int64(math.Round(value * 1000))
}

// verifyAudio checks the only output this pipeline still produces.
func verifyAudio(audio preparationProbeData, duration int64) (int64, error) {
	invalid := func() (int64, error) { return 0, reject("PREPARATION_INVALID_OUTPUT") }
	if len(audio.Streams) != 1 {
		return invalid()
	}
	wav := audio.Streams[0]
	if wav.CodecType != "audio" || wav.CodecName != "pcm_s16le" || wav.SampleRate != "16000" || wav.Channels != 1 {
		return invalid()
	}
	if wav.StartTime != "" {
		start, err := strconv.ParseFloat(wav.StartTime, 64)
		if err != nil || math.IsNaN(start) || math.IsInf(start, 0) || math.Abs(start) > 0.001 {
			return invalid()
		}
	}
	ad := preparationDuration(audio.Format.Duration)
	if ad <= 0 || math.Abs(float64(ad-duration)) > 100 {
		return invalid()
	}
	if wav.Duration != "" && math.Abs(float64(preparationDuration(wav.Duration)-duration)) > 100 {
		return invalid()
	}
	return ad, nil
}

// FFmpeg -start_at_zero subtracts one demuxer origin from every input track.
// Some MP4 headers include that origin in format.duration, so prefer selected
// stream endpoints when available rather than padding an absolute time.
func preparationTimeline(data preparationProbeData, videoIndex, audioIndex int, fallback int64) (int64, error) {
	origin, err := strconv.ParseFloat(data.Format.StartTime, 64)
	if err != nil || math.IsNaN(origin) || math.IsInf(origin, 0) {
		return 0, reject("UNSUPPORTED_SOURCE_TIMELINE")
	}
	end := math.Inf(-1)
	known := 0
	for _, stream := range data.Streams {
		if stream.Index != videoIndex && stream.Index != audioIndex {
			continue
		}
		start, startErr := strconv.ParseFloat(stream.StartTime, 64)
		duration, durationErr := strconv.ParseFloat(stream.Duration, 64)
		if startErr != nil || durationErr != nil || !validDuration(duration) || math.IsNaN(start) || math.IsInf(start, 0) {
			continue
		}
		end = max(end, start+duration)
		known++
	}
	if known != 2 {
		// WebM commonly omits stream durations. Its finite format duration is
		// usable only for an origin close to zero; ambiguous origins fail closed.
		if math.Abs(origin) > 0.1 {
			return 0, reject("UNSUPPORTED_SOURCE_TIMELINE")
		}
		return fallback, nil
	}
	duration := end - origin
	if !validDuration(duration) || duration > MaxDurationSeconds {
		return 0, reject("UNSUPPORTED_SOURCE_TIMELINE")
	}
	return int64(math.Round(duration * 1000)), nil
}
