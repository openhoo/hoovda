package synth

import (
	"context"
	"errors"
	"time"

	"github.com/openhoo/hoovda/internal/events"
)

const MaxSpeechBytes = 1 << 20

type Request struct {
	Text     string                 `json:"text"`
	Locale   string                 `json:"locale"`
	Voice    string                 `json:"voice"`
	Rate     int                    `json:"rate"`
	Pitch    int                    `json:"pitch"`
	Volume   int                    `json:"volume"`
	Commands []events.SpeechCommand `json:"commands,omitempty"`
}

func (r Request) Validate() error {
	if r.Text == "" {
		return errors.New("speech text is empty")
	}
	if len(r.Text) > MaxSpeechBytes {
		return errors.New("speech text exceeds limit")
	}
	if r.Locale != "en-US" && r.Locale != "de-DE" {
		return errors.New("unsupported speech locale")
	}
	if r.Rate < 80 || r.Rate > 450 {
		return errors.New("speech rate outside 80..450")
	}
	if r.Pitch < 0 || r.Pitch > 99 {
		return errors.New("speech pitch outside 0..99")
	}
	if r.Volume < 0 || r.Volume > 200 {
		return errors.New("speech volume outside 0..200")
	}
	return nil
}

type Audio struct {
	SampleRate    int           `json:"sampleRate"`
	Channels      int           `json:"channels"`
	BitsPerSample int           `json:"bitsPerSample"`
	PCM           []byte        `json:"-"`
	Duration      time.Duration `json:"duration"`
}

type Driver interface {
	Synthesize(context.Context, Request) (Audio, error)
	Name() string
	Version(context.Context) string
}

type Silence struct{ SampleRate int }

func (Silence) Name() string                   { return "silence-test-only" }
func (Silence) Version(context.Context) string { return "1" }
func (s Silence) Synthesize(_ context.Context, request Request) (Audio, error) {
	if err := request.Validate(); err != nil {
		return Audio{}, err
	}
	rate := s.SampleRate
	if rate == 0 {
		rate = 22_050
	}
	duration := time.Duration(len([]rune(request.Text))) * 30 * time.Millisecond
	pcm := make([]byte, int(duration.Seconds()*float64(rate))*2)
	return Audio{SampleRate: rate, Channels: 1, BitsPerSample: 16, PCM: pcm, Duration: duration}, nil
}
