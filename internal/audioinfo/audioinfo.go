package audioinfo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var (
	ErrUnsupported = errors.New("unsupported audio format")
	ErrInvalid     = errors.New("invalid audio file")
)

type Info struct {
	MediaType string
	Duration  time.Duration
}

func Inspect(data []byte, filename string) (Info, error) {
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		duration, err := mp4Duration(data)
		if err != nil {
			return Info{}, err
		}
		return Info{MediaType: "audio/mp4", Duration: duration}, nil
	}
	if len(data) >= 44 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		duration, err := wavDuration(data)
		if err != nil {
			return Info{}, err
		}
		return Info{MediaType: "audio/wav", Duration: duration}, nil
	}
	if strings.EqualFold(extension(filename), ".m4a") || strings.EqualFold(extension(filename), ".wav") {
		return Info{}, ErrInvalid
	}
	return Info{}, ErrUnsupported
}

func extension(filename string) string {
	index := strings.LastIndexByte(filename, '.')
	if index < 0 {
		return ""
	}
	return filename[index:]
}

func mp4Duration(data []byte) (time.Duration, error) {
	moov, ok := findBox(data, "moov")
	if !ok {
		return 0, fmt.Errorf("%w: MP4 moov box is missing", ErrInvalid)
	}
	mvhd, ok := findBox(moov, "mvhd")
	if !ok || len(mvhd) < 20 {
		return 0, fmt.Errorf("%w: MP4 duration metadata is missing", ErrInvalid)
	}

	version := mvhd[0]
	var timescale uint32
	var duration uint64
	switch version {
	case 0:
		if len(mvhd) < 20 {
			return 0, ErrInvalid
		}
		timescale = binary.BigEndian.Uint32(mvhd[12:16])
		duration = uint64(binary.BigEndian.Uint32(mvhd[16:20]))
	case 1:
		if len(mvhd) < 32 {
			return 0, ErrInvalid
		}
		timescale = binary.BigEndian.Uint32(mvhd[20:24])
		duration = binary.BigEndian.Uint64(mvhd[24:32])
	default:
		return 0, fmt.Errorf("%w: unsupported MP4 metadata version", ErrInvalid)
	}
	if timescale == 0 || duration == 0 {
		return 0, fmt.Errorf("%w: zero MP4 duration", ErrInvalid)
	}
	seconds := float64(duration) / float64(timescale)
	if seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, ErrInvalid
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// findBox returns the payload of the first matching ISO BMFF box.
func findBox(data []byte, wanted string) ([]byte, bool) {
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		headerSize := uint64(8)
		if size == 1 {
			if offset+16 > len(data) {
				return nil, false
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerSize = 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < headerSize || size > uint64(len(data)-offset) {
			return nil, false
		}
		if string(data[offset+4:offset+8]) == wanted {
			start := offset + int(headerSize)
			return data[start : offset+int(size)], true
		}
		offset += int(size)
	}
	return nil, false
}

func wavDuration(data []byte) (time.Duration, error) {
	var byteRate uint32
	var dataBytes uint32
	foundData := false
	for offset := 12; offset+8 <= len(data); {
		chunkSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		start := offset + 8
		end := start + int(chunkSize)
		// A stale/torn auxiliary chunk size (JUNK/LIST/FLLR) can overshoot
		// EOF while the recorder is finalizing the file. Stop walking instead
		// of rejecting the whole file; a fallback scan below recovers the
		// data chunk if it was skipped.
		if end > len(data) {
			break
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if chunkSize < 16 {
				return 0, ErrInvalid
			}
			byteRate = binary.LittleEndian.Uint32(data[start+8 : start+12])
		case "data":
			dataBytes = chunkSize
			foundData = true
		}
		offset = end + int(chunkSize%2)
	}
	if !foundData || byteRate == 0 {
		// The walk may have been derailed by a large/torn auxiliary chunk
		// before reaching fmt and/or data. Recover both from a scan: the
		// real fmt and data tags always precede the PCM payload, so the
		// first occurrences in the file are authoritative.
		if br, size, ok := scanWAVInfo(data); ok {
			if byteRate == 0 {
				byteRate = br
			}
			if !foundData {
				dataBytes = size
				foundData = true
			}
		}
	}
	if byteRate == 0 || !foundData || dataBytes == 0 {
		return 0, fmt.Errorf("%w: WAV metadata is missing", ErrInvalid)
	}
	return time.Duration(float64(dataBytes) / float64(byteRate) * float64(time.Second)), nil
}

// scanWAVInfo returns the byte rate and data size from the first "fmt " and
// "data" chunks found anywhere in the file. The real chunks always precede
// the PCM bytes, so the first occurrences are authoritative. Returns ok=false
// when either chunk is missing or truncated.
func scanWAVInfo(data []byte) (byteRate, dataSize uint32, ok bool) {
	for offset := 12; offset+8 <= len(data); offset++ {
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if offset+20 <= len(data) {
				byteRate = binary.LittleEndian.Uint32(data[offset+16 : offset+20])
			}
		case "data":
			dataSize = binary.LittleEndian.Uint32(data[offset+4 : offset+8])
			if byteRate > 0 {
				return byteRate, dataSize, true
			}
		}
	}
	return 0, 0, false
}
