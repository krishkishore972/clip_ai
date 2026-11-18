package media

import (
	"bytes"
	"io"
	"os"
	"strings"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

func identifyContainer(path string, data *ffprobe.ProbeData) (string, error) {
	if data == nil || data.Format == nil {
		return "", reject("INVALID_MEDIA_METADATA")
	}
	name := data.Format.FormatName
	if name == "mov,mp4,m4a,3gp,3g2,mj2" || name == "mov" || name == "mp4" {
		brand, err := data.Format.TagList.GetString("major_brand")
		if err != nil || brand == "qt  " {
			return "mov", nil
		}
		switch strings.TrimSpace(brand) {
		case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "iso7", "iso8", "iso9", "mp41", "mp42", "avc1", "hvc1", "hev1", "av01", "M4V", "MSNV":
			return "mp4", nil
		default:
			return "", reject("UNSUPPORTED_MEDIA")
		}
	}
	if name == "matroska,webm" || name == "webm" {
		// FFprobe shares one demuxer name for WebM and Matroska. Check the EBML
		// header's DocType, not an extension or a substring anywhere in the file.
		file, err := os.Open(path)
		if err != nil {
			return "", ErrLocalFile
		}
		defer file.Close()
		header, err := io.ReadAll(io.LimitReader(file, 64<<10))
		if err != nil {
			return "", ErrLocalFile
		}
		if webmHeader(header) {
			return "webm", nil
		}
	}
	return "", reject("UNSUPPORTED_MEDIA")
}

func webmHeader(data []byte) bool {
	if len(data) < 5 || !bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return false
	}
	size, n, ok := vint(data[4:], false)
	if !ok || size > uint64(len(data)-4-n) {
		return false
	}
	data = data[4+n : 4+n+int(size)]
	for len(data) > 0 {
		id, n, ok := vint(data, true)
		if !ok {
			return false
		}
		data = data[n:]
		size, n, ok := vint(data, false)
		if !ok || size > uint64(len(data)-n) {
			return false
		}
		data = data[n:]
		if id == 0x4282 {
			return string(data[:int(size)]) == "webm"
		}
		data = data[int(size):]
	}
	return false
}

// EBML variable-width integer. IDs retain their marker bit; sizes do not.
func vint(data []byte, id bool) (uint64, int, bool) {
	if len(data) == 0 || data[0] == 0 {
		return 0, 0, false
	}
	n, mask := 1, byte(0x80)
	for data[0]&mask == 0 {
		n++
		mask >>= 1
	}
	if n > len(data) || (id && n > 4) {
		return 0, 0, false
	}
	value := uint64(data[0])
	if !id {
		value &= uint64(mask - 1)
	}
	for _, b := range data[1:n] {
		value = value<<8 | uint64(b)
	}
	return value, n, true
}
