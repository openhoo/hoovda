package synth

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const maxWAVBytes = 64 << 20

// MaxPCMBytes leaves room for the canonical WAV header inside the container
// limit. Recorders must enforce this before allocating timeline silence.
const MaxPCMBytes = maxWAVBytes - 44

func (audio Audio) Validate() error {
	if audio.SampleRate < 8_000 || audio.SampleRate > 192_000 || audio.Channels < 1 || audio.Channels > 8 || audio.BitsPerSample != 16 {
		return errors.New("unsupported audio format")
	}
	if len(audio.PCM) > MaxPCMBytes {
		return errors.New("PCM exceeds WAV limit")
	}
	if len(audio.PCM)%(audio.Channels*2) != 0 {
		return errors.New("PCM contains a partial sample frame")
	}
	return nil
}

func DecodeWAV(data []byte) (Audio, error) {
	if len(data) < 12 || len(data) > maxWAVBytes || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return Audio{}, errors.New("invalid WAV container")
	}
	var format []byte
	var pcm []byte
	var seenFormat, seenData bool
	streaming := false
	for offset := 12; offset < len(data); {
		if len(data)-offset < 8 {
			return Audio{}, errors.New("truncated WAV chunk header")
		}
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
			streaming = true
		}
		if size < 0 || offset+size > len(data) {
			return Audio{}, errors.New("invalid WAV chunk size")
		}
		chunk := data[offset : offset+size]
		switch name {
		case "fmt ":
			if seenFormat {
				return Audio{}, errors.New("duplicate WAV format chunk")
			}
			seenFormat = true
			format = append([]byte(nil), chunk...)
		case "data":
			if seenData {
				return Audio{}, errors.New("duplicate WAV data chunk")
			}
			seenData = true
			pcm = append([]byte(nil), chunk...)
		}
		offset += size
		if size%2 == 1 {
			if offset == len(data) {
				return Audio{}, errors.New("missing WAV chunk padding")
			}
			offset++
		}
	}
	riffSize := binary.LittleEndian.Uint32(data[4:8])
	if riffSize != uint32(len(data)-8) && !(streaming && riffSize == 0x7ffff024) {
		return Audio{}, errors.New("invalid WAV RIFF size")
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
	audio := Audio{SampleRate: sampleRate, Channels: channels, BitsPerSample: bits, PCM: pcm}
	if err := audio.Validate(); err != nil {
		return Audio{}, fmt.Errorf("invalid WAV PCM: %w", err)
	}
	bytesPerSecond := sampleRate * channels * (bits / 8)
	if int(binary.LittleEndian.Uint32(format[8:12])) != bytesPerSecond || int(binary.LittleEndian.Uint16(format[12:14])) != channels*(bits/8) {
		return Audio{}, errors.New("inconsistent WAV PCM header")
	}
	audio.Duration = time.Duration(int64(len(pcm)) * int64(time.Second) / int64(bytesPerSecond))
	return audio, nil
}

func EncodeWAV(audio Audio) ([]byte, error) {
	if err := audio.Validate(); err != nil {
		return nil, err
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
