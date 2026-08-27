package recording

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/openhoo/hoovda/internal/synth"
)

type Config struct {
	Root      string
	Display   string
	Width     int
	Height    int
	FFmpeg    string
	FrameRate int
}

type Artifact struct {
	Name        string `json:"name"`
	Path        string `json:"-"`
	ContentType string `json:"contentType"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
}

type Segment struct {
	Offset time.Duration
	Audio  synth.Audio
}

type session struct {
	id       string
	dir      string
	baseNS   int64
	record   bool
	segments []Segment
	extras   []Artifact
	video    *exec.Cmd
	videoIn  io.WriteCloser
	videoErr *os.File
	mu       sync.Mutex
}

type Manager struct {
	cfg      Config
	mu       sync.Mutex
	sessions map[string]*session
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.Root == "" || cfg.Display == "" || cfg.Width < 1 || cfg.Height < 1 {
		return nil, errors.New("invalid recording configuration")
	}
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	if cfg.FrameRate == 0 {
		cfg.FrameRate = 25
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, err
	}
	return &Manager{cfg: cfg, sessions: map[string]*session{}}, nil
}

func (m *Manager) Start(ctx context.Context, id string, baseNS int64, record bool) error {
	if id == "" {
		return errors.New("recording session id is empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[id]; exists {
		return errors.New("recording session already exists")
	}
	dir := filepath.Join(m.cfg.Root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	s := &session{id: id, dir: dir, baseNS: baseNS, record: record}
	if record {
		stderr, err := os.OpenFile(filepath.Join(dir, "ffmpeg-capture.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, m.cfg.FFmpeg, "-hide_banner", "-loglevel", "warning", "-y", "-f", "x11grab", "-framerate", fmt.Sprint(m.cfg.FrameRate), "-video_size", fmt.Sprintf("%dx%d", m.cfg.Width, m.cfg.Height), "-i", m.cfg.Display, "-an", "-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", filepath.Join(dir, "video-silent.webm"))
		stdin, err := cmd.StdinPipe()
		if err != nil {
			_ = stderr.Close()
			return err
		}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			_ = stderr.Close()
			return fmt.Errorf("start ffmpeg capture: %w", err)
		}
		s.video, s.videoIn, s.videoErr = cmd, stdin, stderr
	}
	m.sessions[id] = s
	return nil
}

func (m *Manager) AddAudio(sessionID string, monotonicNS int64, audio synth.Audio) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return
	}
	offset := time.Duration(monotonicNS - s.baseNS)
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	s.segments = append(s.segments, Segment{Offset: offset, Audio: audio})
	s.mu.Unlock()
}

func (m *Manager) WriteJSON(sessionID, name string, value []byte) (Artifact, error) {
	filenames := map[string]string{
		"screenreader-events":   "events.json",
		"screenreader-document": "document.json",
	}
	filename, ok := filenames[name]
	if !ok {
		return Artifact{}, errors.New("unknown JSON artifact")
	}
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return Artifact{}, errors.New("recording session not found")
	}
	path := filepath.Join(s.dir, filename)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		return Artifact{}, err
	}
	artifact, err := inventory(name, path, "application/json")
	if err != nil {
		return Artifact{}, err
	}
	s.mu.Lock()
	s.extras = append(s.extras, artifact)
	s.mu.Unlock()
	return artifact, nil
}

func (m *Manager) Finish(ctx context.Context, id string) ([]Artifact, error) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s == nil {
		return nil, errors.New("recording session not found")
	}
	if s.video != nil {
		_, _ = io.WriteString(s.videoIn, "q\n")
		_ = s.videoIn.Close()
		wait := make(chan error, 1)
		go func() { wait <- s.video.Wait() }()
		select {
		case err := <-wait:
			if err != nil {
				_ = s.videoErr.Close()
				return nil, fmt.Errorf("ffmpeg capture: %w", err)
			}
		case <-ctx.Done():
			_ = s.video.Process.Kill()
			_ = s.videoErr.Close()
			return nil, ctx.Err()
		}
		_ = s.videoErr.Close()
	}
	s.mu.Lock()
	segments := append([]Segment(nil), s.segments...)
	extras := append([]Artifact(nil), s.extras...)
	s.mu.Unlock()
	audio, err := renderTimeline(segments)
	if err != nil {
		return nil, err
	}
	wav, err := synth.EncodeWAV(audio)
	if err != nil {
		return nil, err
	}
	wavPath := filepath.Join(s.dir, "screenreader.wav")
	if err := os.WriteFile(wavPath, wav, 0o600); err != nil {
		return nil, err
	}
	artifacts := extras
	item, err := inventory("screenreader-audio", wavPath, "audio/wav")
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, item)
	if s.record {
		videoPath := filepath.Join(s.dir, "screenreader.webm")
		capturePath := filepath.Join(s.dir, "video-silent.webm")
		cmd := exec.CommandContext(ctx, m.cfg.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "warning", "-y", "-i", capturePath, "-i", wavPath, "-c:v", "copy", "-c:a", "libopus", "-af", "apad", "-shortest", videoPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("mux screenreader video: %w: %s", err, output)
		}
		item, err := inventory("screenreader-video", videoPath, "video/webm")
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, item)
	}
	return artifacts, nil
}

func renderTimeline(segments []Segment) (synth.Audio, error) {
	const defaultRate = 22_050
	segments = append([]Segment(nil), segments...)
	sort.SliceStable(segments, func(i, j int) bool { return segments[i].Offset < segments[j].Offset })
	result := synth.Audio{SampleRate: defaultRate, Channels: 1, BitsPerSample: 16}
	for _, segment := range segments {
		if segment.Audio.Channels != 1 || segment.Audio.BitsPerSample != 16 {
			return synth.Audio{}, errors.New("audio timeline requires mono signed 16-bit PCM")
		}
		if len(result.PCM) == 0 {
			result.SampleRate = segment.Audio.SampleRate
		}
		if segment.Audio.SampleRate != result.SampleRate {
			return synth.Audio{}, errors.New("audio timeline sample rates differ")
		}
		offsetBytes := int(float64(segment.Offset) / float64(time.Second) * float64(result.SampleRate) * 2)
		if offsetBytes%2 != 0 {
			offsetBytes++
		}
		if offsetBytes > len(result.PCM) {
			result.PCM = append(result.PCM, make([]byte, offsetBytes-len(result.PCM))...)
		}
		if offsetBytes < len(result.PCM) {
			offsetBytes = len(result.PCM)
		}
		result.PCM = append(result.PCM, segment.Audio.PCM...)
	}
	if len(result.PCM) == 0 {
		result.PCM = make([]byte, result.SampleRate/10*2)
	}
	result.Duration = time.Duration(float64(len(result.PCM)) / float64(result.SampleRate*2) * float64(time.Second))
	return result, nil
}

func inventory(name, path, contentType string) (Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	digest := sha256.New()
	bytes, err := io.Copy(digest, file)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: name, Path: path, ContentType: contentType, Bytes: bytes, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func (m *Manager) ResolveArtifact(sessionID, name string) (string, error) {
	base := filepath.Clean(filepath.Join(m.cfg.Root, sessionID))
	files := map[string]string{
		"screenreader-audio":    "screenreader.wav",
		"screenreader-video":    "screenreader.webm",
		"screenreader-events":   "events.json",
		"screenreader-document": "document.json",
	}
	filename, ok := files[name]
	if !ok {
		return "", errors.New("unknown artifact")
	}
	path := filepath.Clean(filepath.Join(base, filename))
	if filepath.Dir(path) != base {
		return "", errors.New("artifact path escapes session")
	}
	return path, nil
}
