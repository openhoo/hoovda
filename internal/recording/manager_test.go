package recording

import (
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/synth"
)

func TestRenderTimelineInsertsSilence(t *testing.T) {
	audio, err := renderTimeline([]Segment{{Offset: 100 * time.Millisecond, Audio: synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: make([]byte, 200)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(audio.PCM) != 2200 {
		t.Fatalf("PCM bytes = %d", len(audio.PCM))
	}
}

func TestRenderTimelineOrdersAsynchronousSegmentsByOffset(t *testing.T) {
	first := synth.Audio{SampleRate: 1000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 1}}
	second := synth.Audio{SampleRate: 1000, Channels: 1, BitsPerSample: 16, PCM: []byte{2, 2}}
	audio, err := renderTimeline([]Segment{
		{Offset: 2 * time.Millisecond, Audio: second},
		{Offset: 0, Audio: first},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := audio.PCM; len(got) != 6 || got[0] != 1 || got[1] != 1 || got[4] != 2 || got[5] != 2 {
		t.Fatalf("PCM = %v", got)
	}
}
