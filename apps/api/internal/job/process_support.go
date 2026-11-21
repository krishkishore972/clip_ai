package job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

const (
	ValidationTaskType = "media:validate"
	ValidationQueue    = "validation"
)

var (
	processAssetIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	processCodePattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// VideoDownloader reads only from the worker's configured private bucket.
type VideoDownloader interface {
	Download(context.Context, string, string, int64) error
}

type DerivedUploader interface {
	UploadDerived(context.Context, string, string, string, string) (storage.DerivedObject, error)
}

func validProcessAssetID(id string) bool {
	return processAssetIDPattern.MatchString(id)
}

func prepareProcessRoot(root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return "", errors.New("media process: temporary root must be a private absolute directory")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", errors.New("media process: temporary root unavailable")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("media process: temporary root must be private and not a symlink")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("media process: temporary root unavailable")
	}
	return resolved, nil
}

func hashPreparationFile(ctx context.Context, path string, size int64, root string) (string, error) {
	if filepath.Dir(path) != root || !filepath.IsAbs(path) {
		return "", errors.New("invalid output path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return "", errors.New("invalid output file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("output unavailable")
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("output read failed")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validStoredMetadata(m *media.Metadata) bool {
	if m == nil || m.SizeBytes <= 0 || m.SizeBytes > media.MaxFileBytes ||
		m.DurationMs <= 0 || m.DurationMs > media.MaxDurationSeconds*1000 ||
		len(m.Video) == 0 || len(m.Audio) == 0 {
		return false
	}
	for _, v := range m.Video {
		if v.Index < 0 || v.Width <= 0 || v.Height <= 0 ||
			v.FPS.Num <= 0 || v.FPS.Den <= 0 || strings.TrimSpace(v.Codec) == "" {
			return false
		}
	}
	for _, a := range m.Audio {
		if a.Index < 0 || strings.TrimSpace(a.Codec) == "" ||
			a.SampleRate <= 0 || a.Channels <= 0 {
			return false
		}
	}
	return true
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
