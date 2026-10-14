package synth

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestWAVRoundTrip(t *testing.T) {
	original := Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: []byte{0, 0, 1, 0}, Duration: time.Second}
	encoded, err := EncodeWAV(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWAV(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SampleRate != original.SampleRate || !bytes.Equal(decoded.PCM, original.PCM) {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestDecodeAcceptsESpeakStreamingSentinel(t *testing.T) {
	original := Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: []byte{0, 0, 1, 0}}
	encoded, err := EncodeWAV(original)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(encoded[4:8], 0x7ffff024)
	binary.LittleEndian.PutUint32(encoded[40:44], 0x7ffff000)
	decoded, err := DecodeWAV(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.PCM, original.PCM) {
		t.Fatalf("pcm = %#v", decoded.PCM)
	}
}

func TestDecodeRejectsNonWAV(t *testing.T) {
	if _, err := DecodeWAV([]byte("not wave")); err == nil {
		t.Fatal("expected error")
	}
}

func TestWAVRejectsPartialPCMFrames(t *testing.T) {
	for _, audio := range []Audio{
		{SampleRate: 22050, Channels: 1, BitsPerSample: 16, PCM: []byte{1}},
		{SampleRate: 22050, Channels: 2, BitsPerSample: 16, PCM: []byte{1, 0}},
	} {
		if _, err := EncodeWAV(audio); err == nil {
			t.Errorf("encoded partial PCM frame: %#v", audio)
		}
	}
	valid, err := EncodeWAV(Audio{SampleRate: 22050, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	valid = valid[:len(valid)-1]
	binary.LittleEndian.PutUint32(valid[4:8], uint32(len(valid)-8))
	binary.LittleEndian.PutUint32(valid[40:44], 1)
	if _, err := DecodeWAV(valid); err == nil {
		t.Fatal("decoded partial PCM frame")
	}
}

func TestDecodeRejectsInconsistentPCMHeader(t *testing.T) {
	valid, err := EncodeWAV(Audio{SampleRate: 22050, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []int{4, 28, 32} {
		broken := append([]byte(nil), valid...)
		broken[field]++
		if _, err := DecodeWAV(broken); err == nil {
			t.Errorf("accepted invalid PCM header field at %d", field)
		}
	}
}

func TestDecodeRejectsEmptyDuplicateChunks(t *testing.T) {
	valid, err := EncodeWAV(Audio{SampleRate: 22050, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0, 2, 0}})
	if err != nil {
		t.Fatal(err)
	}
	for _, chunkName := range []string{"fmt ", "data"} {
		t.Run(chunkName, func(t *testing.T) {
			// An empty chunk still declares this structural member. Appending
			// its zero bytes to nil must not make the declaration invisible.
			duplicate := make([]byte, 0, len(valid)+8)
			duplicate = append(duplicate, valid[:12]...)
			duplicate = append(duplicate, []byte(chunkName)...)
			duplicate = append(duplicate, 0, 0, 0, 0)
			duplicate = append(duplicate, valid[12:]...)
			binary.LittleEndian.PutUint32(duplicate[4:8], uint32(len(duplicate)-8))
			if audio, err := DecodeWAV(duplicate); err == nil {
				t.Fatalf("empty duplicate %q chunk was accepted: %#v", chunkName, audio)
			}
		})
	}
}
