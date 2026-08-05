package audioinfo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestInspectMP4(t *testing.T) {
	mvhd := make([]byte, 20)
	binary.BigEndian.PutUint32(mvhd[12:16], 1000)
	binary.BigEndian.PutUint32(mvhd[16:20], 12500)
	data := append(box("ftyp", []byte("M4A \x00\x00\x00\x00")), box("moov", box("mvhd", mvhd))...)

	info, err := Inspect(data, "recording.m4a")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if info.MediaType != "audio/mp4" {
		t.Fatalf("MediaType = %q", info.MediaType)
	}
	if info.Duration != 12500*time.Millisecond {
		t.Fatalf("Duration = %s", info.Duration)
	}
}

func TestInspectWAV(t *testing.T) {
	data := testWAV(2 * time.Second)
	info, err := Inspect(data, "recording.wav")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if info.Duration != 2*time.Second {
		t.Fatalf("Duration = %s", info.Duration)
	}
}

func TestInspectRejectsExtensionSpoofing(t *testing.T) {
	_, err := Inspect([]byte("not audio"), "recording.m4a")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func box(kind string, payload []byte) []byte {
	data := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(data[:4], uint32(len(data)))
	copy(data[4:8], kind)
	copy(data[8:], payload)
	return data
}

func testWAV(duration time.Duration) []byte {
	const byteRate = 32000
	dataSize := uint32(duration.Seconds() * byteRate)
	var output bytes.Buffer
	output.WriteString("RIFF")
	_ = binary.Write(&output, binary.LittleEndian, uint32(36)+dataSize)
	output.WriteString("WAVEfmt ")
	_ = binary.Write(&output, binary.LittleEndian, uint32(16))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint32(16000))
	_ = binary.Write(&output, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&output, binary.LittleEndian, uint16(2))
	_ = binary.Write(&output, binary.LittleEndian, uint16(16))
	output.WriteString("data")
	_ = binary.Write(&output, binary.LittleEndian, dataSize)
	output.Write(make([]byte, dataSize))
	return output.Bytes()
}

// wavWithChunks builds a canonical WAV with optional extra RIFF chunks
// (JUNK/LIST/etc.) inserted between WAVE and fmt.
func wavWithChunks(auxChunks ...[]byte) []byte {
	const byteRate = 32000
	dataSize := uint32(3 * byteRate) // 3 seconds
	var output bytes.Buffer
	output.WriteString("RIFF")
	totalSize := uint32(4) // "WAVE"
	for _, chunk := range auxChunks {
		totalSize += uint32(len(chunk))
	}
	totalSize += 8 + 16 + 8 + dataSize // fmt header+body, data header+payload
	_ = binary.Write(&output, binary.LittleEndian, totalSize)
	output.WriteString("WAVE")
	for _, chunk := range auxChunks {
		output.Write(chunk)
	}
	output.WriteString("fmt ")
	_ = binary.Write(&output, binary.LittleEndian, uint32(16))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint32(16000))
	_ = binary.Write(&output, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&output, binary.LittleEndian, uint16(2))
	_ = binary.Write(&output, binary.LittleEndian, uint16(16))
	output.WriteString("data")
	_ = binary.Write(&output, binary.LittleEndian, dataSize)
	output.Write(make([]byte, dataSize))
	return output.Bytes()
}

// junkChunk returns a JUNK chunk whose header declares declaredSize bytes
// while only payloadBytes are actually present (the overshoot simulates a
// stale/torn size field read while the recorder was finalizing the file).
func junkChunk(declaredSize, payloadBytes uint32) []byte {
	out := make([]byte, 8+payloadBytes)
	copy(out, "JUNK")
	binary.LittleEndian.PutUint32(out[4:8], declaredSize)
	return out
}

func TestInspectWAVWithJUNK(t *testing.T) {
	data := wavWithChunks(junkChunk(28, 28))
	info, err := Inspect(data, "recording.wav")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if info.Duration != 3*time.Second {
		t.Fatalf("Duration = %s", info.Duration)
	}
}

func TestInspectWAVWithTornJUNK(t *testing.T) {
	// A JUNK chunk declaring a size that overshoots the file used to make
	// wavDuration reject the whole file; the scan fallback must recover
	// fmt + data and still derive the duration.
	data := wavWithChunks(junkChunk(0xFFFFF000, 28))
	info, err := Inspect(data, "recording.wav")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if info.Duration != 3*time.Second {
		t.Fatalf("Duration = %s", info.Duration)
	}
}
