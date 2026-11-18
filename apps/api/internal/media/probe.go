package media

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

var (
	ErrLocalFile        = errors.New("media: expected a readable local regular file")
	ErrProbeUnavailable = errors.New("media: ffprobe executable unavailable")
	ErrProbeFailed      = errors.New("media: probe failed")
	ErrProbeTimeout     = errors.New("media: probe deadline exceeded")
	ErrProbeConfig      = errors.New("media: probe timeout must be positive")
)

// ProbeFile uses go-ffprobe; it never rewrites or deletes the input. The caller
// owns the local file (eventually an isolated worker download) and its cleanup.
// ffprobe must be on PATH. No global binary-path mutations are performed.
// This is metadata inspection, not a full decode or a process sandbox.
func ProbeFile(ctx context.Context, path string, timeout time.Duration) (Metadata, error) {
	if timeout <= 0 {
		return Metadata{}, ErrProbeConfig
	}
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil || path == "" {
		return Metadata{}, ErrLocalFile
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return Metadata{}, ErrLocalFile
	}
	if info.Size() <= 0 {
		return Metadata{}, reject("INVALID_MEDIA")
	}
	if info.Size() > MaxFileBytes {
		return Metadata{}, reject("FILE_TOO_LARGE")
	}
	file, err := os.Open(abs)
	if err != nil {
		return Metadata{}, ErrLocalFile
	}
	if err := file.Close(); err != nil {
		return Metadata{}, ErrLocalFile
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return Metadata{}, ErrProbeUnavailable
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Only report required metadata. Do not emit filenames, arbitrary tags,
	// chapters or packets into the library's in-memory output buffer.
	data, err := ffprobe.ProbeURL(probeCtx, abs,
		"-protocol_whitelist", "file",
		"-format_whitelist", "mov,matroska,webm",
		"-max_alloc", "67108864",
		"-show_entries", "format=format_name,duration:format_tags=major_brand:stream=index,codec_type,codec_name,width,height,avg_frame_rate,r_frame_rate,duration,sample_rate,channels:stream_disposition=attached_pic:stream_tags=rotate:stream_side_data=side_data_type,rotation:chapter=",
	)
	if err != nil {
		if ctx.Err() != nil {
			return Metadata{}, ctx.Err()
		}
		if probeCtx.Err() != nil {
			return Metadata{}, ErrProbeTimeout
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return Metadata{}, reject("INVALID_MEDIA")
		}
		// Library errors embed stderr: never return or log them verbatim.
		return Metadata{}, ErrProbeFailed
	}
	container, err := identifyContainer(abs, data)
	if err != nil {
		return Metadata{}, err
	}
	metadata, err := validate(data, container)
	if err != nil {
		return Metadata{}, err
	}
	metadata.SizeBytes = info.Size()
	return metadata, nil
}
