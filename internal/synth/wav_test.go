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
