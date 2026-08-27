package synth

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const maxWAVBytes = 64 << 20

func DecodeWAV(data []byte) (Audio, error) {
	if len(data) < 12 || len(data) > maxWAVBytes || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return Audio{}, errors.New("invalid WAV container")
	}
	var format []byte
	var pcm []byte
	for offset := 12; offset+8 <= len(data); {
		name := string(data[offset : offset+4])
		rawSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		size := int(rawSize)
		offset += 8
		// eSpeak NG cannot seek when --stdout writes to a pipe. It marks the
		// final data chunk as 0x7ffff000 and streams PCM until EOF. Accept only
		// that documented sentinel on the final data chunk; all other truncated
		// chunks remain malformed.
		if name == "data" && rawSize == 0x7ffff000 {
			size = len(data) - offset
		}
		if size < 0 || offset+size > len(data) {
			return Audio{}, errors.New("invalid WAV chunk size")
		}
		chunk := data[offset : offset+size]
		switch name {
		case "fmt ":
			format = append([]byte(nil), chunk...)
		case "data":
			pcm = append([]byte(nil), chunk...)
		}
		offset += size
		if size%2 == 1 {
			offset++
		}
	}
	if len(format) < 16 || len(pcm) == 0 {
		return Audio{}, errors.New("WAV lacks format or data")
	}
	if binary.LittleEndian.Uint16(format[0:2]) != 1 {
		return Audio{}, errors.New("WAV is not PCM")
	}
	channels := int(binary.LittleEndian.Uint16(format[2:4]))
	sampleRate := int(binary.LittleEndian.Uint32(format[4:8]))
	bits := int(binary.LittleEndian.Uint16(format[14:16]))
	if channels < 1 || channels > 8 || sampleRate < 8_000 || sampleRate > 192_000 || bits != 16 {
		return Audio{}, fmt.Errorf("unsupported WAV format channels=%d rate=%d bits=%d", channels, sampleRate, bits)
	}
	bytesPerSecond := sampleRate * channels * (bits / 8)
	duration := time.Duration(float64(len(pcm)) / float64(bytesPerSecond) * float64(time.Second))
	return Audio{SampleRate: sampleRate, Channels: channels, BitsPerSample: bits, PCM: pcm, Duration: duration}, nil
}

func EncodeWAV(audio Audio) ([]byte, error) {
	if audio.SampleRate < 8_000 || audio.SampleRate > 192_000 || audio.Channels < 1 || audio.Channels > 8 || audio.BitsPerSample != 16 {
		return nil, errors.New("unsupported audio format")
	}
	if len(audio.PCM) > maxWAVBytes {
		return nil, errors.New("PCM exceeds WAV limit")
	}
	var output bytes.Buffer
	dataSize := uint32(len(audio.PCM))
	_ = binary.Write(&output, binary.LittleEndian, [4]byte{'R', 'I', 'F', 'F'})
	_ = binary.Write(&output, binary.LittleEndian, uint32(36)+dataSize)
	_ = binary.Write(&output, binary.LittleEndian, [4]byte{'W', 'A', 'V', 'E'})
	_ = binary.Write(&output, binary.LittleEndian, [4]byte{'f', 'm', 't', ' '})
	_ = binary.Write(&output, binary.LittleEndian, uint32(16))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint16(audio.Channels))
	_ = binary.Write(&output, binary.LittleEndian, uint32(audio.SampleRate))
	byteRate := audio.SampleRate * audio.Channels * audio.BitsPerSample / 8
	_ = binary.Write(&output, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&output, binary.LittleEndian, uint16(audio.Channels*audio.BitsPerSample/8))
	_ = binary.Write(&output, binary.LittleEndian, uint16(audio.BitsPerSample))
	_ = binary.Write(&output, binary.LittleEndian, [4]byte{'d', 'a', 't', 'a'})
	_ = binary.Write(&output, binary.LittleEndian, dataSize)
	_, _ = output.Write(audio.PCM)
	return output.Bytes(), nil
}
