package recording

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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

	"github.com/openhoo/hoovda/internal/processoutput"
	"github.com/openhoo/hoovda/internal/synth"
)

// ErrCaptureFailed marks capture failures that cannot be repaired by retrying
// artifact export. The stopped video process cannot resume its recording.
var ErrCaptureFailed = errors.New("recording capture cannot be finalized")

// A session contains many independently bounded synthesis results. Keep its
// finite export budget separate from the untrusted single-WAV decoder budget.
// Export streams these stored segments instead of allocating another full PCM
// timeline and WAV buffer. Silence has its own, smaller cumulative budget.
const (
	maxSessionPCMBytes      = 256 << 20
	maxTimelineSilenceBytes = synth.MaxPCMBytes
	recordingIOChunkBytes   = 32 << 10
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
	Offset      time.Duration
	MonotonicNS int64
	Audio       synth.Audio
}

type session struct {
	id                string
	dir               string
	baseNS            int64
	record            bool
	segments          []Segment
	extras            []Artifact
	video             *exec.Cmd
	videoIn           io.WriteCloser
	videoErr          *os.File
	mu                sync.Mutex
	finishMu          sync.Mutex
	sealed            bool
	audioSealed       bool
	audioErr          error
	audioBytes        int
	videoStopped      bool
	videoStopErr      error
	finishedArtifacts []Artifact
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
	if cfg.FrameRate < 1 || cfg.FrameRate > 120 {
		return nil, errors.New("recording frame rate outside 1..120")
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, err
	}
	return &Manager{cfg: cfg, sessions: map[string]*session{}}, nil
}

func (m *Manager) Start(ctx context.Context, id string, baseNS int64, record bool) (err error) {
	if !validSessionID(id) {
		return errors.New("invalid recording session id")
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
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
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
	defer s.mu.Unlock()
	if s.sealed || s.audioSealed || s.audioErr != nil {
		return
	}
	if err := audio.Validate(); err != nil {
		s.audioErr = err
		return
	}
	if len(audio.PCM) > maxSessionPCMBytes-s.audioBytes {
		s.audioErr = fmt.Errorf("recorded PCM exceeds session limit (%d + %d > %d bytes)", s.audioBytes, len(audio.PCM), maxSessionPCMBytes)
		return
	}
	audio.PCM = append([]byte(nil), audio.PCM...)
	s.audioBytes += len(audio.PCM)
	s.segments = append(s.segments, Segment{Offset: offset, MonotonicNS: monotonicNS, Audio: audio})
}

// AudioTimeline describes the exact sample ranges in the exported WAV, including
// rounding and serialized overlapping utterances. MonotonicNS identifies the
// originating speech event, not the later asynchronous audio completion event.
type AudioTimelineEntry struct {
	MonotonicNS int64 `json:"monotonicNs"`
	StartSample int64 `json:"startSample"`
	SampleCount int64 `json:"sampleCount"`
	SampleRate  int   `json:"sampleRate"`
}

func (m *Manager) AudioTimeline(id string) ([]AudioTimelineEntry, error) {
	return m.audioTimeline(id, false)
}

// SealAudioTimeline fixes the final sample index and WAV at the same audio
// boundary. JSON exports can still be written before Finish begins.
func (m *Manager) SealAudioTimeline(id string) ([]AudioTimelineEntry, error) {
	return m.audioTimeline(id, true)
}

func (m *Manager) audioTimeline(id string, seal bool) ([]AudioTimelineEntry, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, errors.New("recording session not found")
	}
	s.mu.Lock()
	if seal {
		s.audioSealed = true
	}
	if s.audioErr != nil {
		err := s.audioErr
		s.mu.Unlock()
		return nil, err
	}
	segments := append([]Segment(nil), s.segments...)
	s.mu.Unlock()
	plan, err := planAudioTimeline(segments)
	return plan.timeline, err
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return Artifact{}, errors.New("recording session is finishing")
	}
	path := filepath.Join(s.dir, filename)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		return Artifact{}, err
	}
	artifact, err := inventory(name, path, "application/json")
	if err != nil {
		return Artifact{}, err
	}
	replaced := false
	for index := range s.extras {
		if s.extras[index].Name == artifact.Name {
			s.extras[index] = artifact
			replaced = true
			break
		}
	}
	if !replaced {
		s.extras = append(s.extras, artifact)
	}
	return artifact, nil
}

func (m *Manager) Finish(ctx context.Context, id string) ([]Artifact, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, errors.New("recording session not found")
	}
	s.finishMu.Lock()
	defer s.finishMu.Unlock()
	if s.finishedArtifacts != nil {
		return append([]Artifact(nil), s.finishedArtifacts...), nil
	}
	// Freeze the same segments used by AudioTimeline; retries cannot silently
	// append synthesis that was absent from the exported event index.
	s.mu.Lock()
	s.sealed = true
	segments := append([]Segment(nil), s.segments...)
	extras := append([]Artifact(nil), s.extras...)
	audioErr := s.audioErr
	s.mu.Unlock()
	if err := stopCapture(ctx, s); err != nil {
		return nil, err
	}
	if audioErr != nil {
		return nil, audioErr
	}
	wavPath := filepath.Join(s.dir, "screenreader.wav")
	if err := writeTimelineWAV(ctx, wavPath, segments); err != nil {
		return nil, err
	}
	artifacts := extras
	item, err := inventoryContext(ctx, "screenreader-audio", wavPath, "audio/wav")
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, item)
	if s.record {
		videoPath := filepath.Join(s.dir, "screenreader.webm")
		capturePath := filepath.Join(s.dir, "video-silent.webm")
		// Keep both finite inputs complete. Serialized speech can extend beyond
		// capture; apad plus shortest would truncate it at the video's end.
		cmd := exec.CommandContext(ctx, m.cfg.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "warning", "-y", "-i", capturePath, "-i", wavPath, "-c:v", "copy", "-c:a", "libopus", videoPath)
		output := processoutput.Buffer{Limit: 64 << 10}
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := errors.Join(cmd.Run(), ctx.Err(), output.Err); err != nil {
			return nil, fmt.Errorf("mux screenreader video: %w: %s", err, output.String())
		}
		item, err := inventoryContext(ctx, "screenreader-video", videoPath, "video/webm")
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, item)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.sessions[id] == s {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	s.finishedArtifacts = append([]Artifact(nil), artifacts...)
	return artifacts, nil
}

// Abort stops capture and releases a failed or abandoned session without trying
// to render or mux evidence. It also makes RemoveArtifacts safe after failure.
func (m *Manager) Abort(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	s.finishMu.Lock()
	defer s.finishMu.Unlock()
	s.mu.Lock()
	s.sealed = true
	s.mu.Unlock()
	err := stopCapture(ctx, s)
	m.mu.Lock()
	if m.sessions[id] == s {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	return err
}

// The caller holds finishMu. A mux retry must never call exec.Cmd.Wait twice.
func stopCapture(ctx context.Context, s *session) error {
	if s.videoStopped {
		return s.videoStopErr
	}
	s.videoStopped = true
	if s.video != nil {
		_, _ = io.WriteString(s.videoIn, "q\n")
		_ = s.videoIn.Close()
		wait := make(chan error, 1)
		go func() { wait <- s.video.Wait() }()
		select {
		case err := <-wait:
			if err != nil {
				_ = s.videoErr.Close()
				s.videoStopErr = fmt.Errorf("%w: ffmpeg capture: %w", ErrCaptureFailed, err)
				return s.videoStopErr
			}
		case <-ctx.Done():
			_ = s.video.Process.Kill()
			<-wait
			_ = s.videoErr.Close()
			s.videoStopErr = errors.Join(ErrCaptureFailed, ctx.Err())
			return s.videoStopErr
		}
		_ = s.videoErr.Close()
	}
	return nil
}

func renderTimeline(segments []Segment) (synth.Audio, error) {
	audio, _, err := renderTimelineWithIndex(segments)
	return audio, err
}

func renderTimelineWithIndex(segments []Segment) (synth.Audio, []AudioTimelineEntry, error) {
	plan, err := planAudioTimeline(segments)
	if err != nil {
		return synth.Audio{}, nil, err
	}
	result := synth.Audio{SampleRate: plan.sampleRate, Channels: 1, BitsPerSample: 16, PCM: make([]byte, plan.pcmBytes)}
	for _, segment := range plan.segments {
		copy(result.PCM[segment.offsetBytes:], segment.audio.PCM)
	}
	result.Duration = time.Duration(int64(len(result.PCM)) * int64(time.Second) / int64(result.SampleRate*2))
	return result, plan.timeline, nil
}

type plannedAudioSegment struct {
	offsetBytes int
	audio       synth.Audio
}

type audioTimelinePlan struct {
	sampleRate int
	pcmBytes   int
	segments   []plannedAudioSegment
	timeline   []AudioTimelineEntry
}

// Planning the exact index must not allocate a PCM copy. Finish uses the same
// layout to stream every original frame and the bounded intervening silence.
func planAudioTimeline(segments []Segment) (audioTimelinePlan, error) {
	const defaultRate = 22_050
	segments = append([]Segment(nil), segments...)
	sort.SliceStable(segments, func(i, j int) bool { return segments[i].Offset < segments[j].Offset })
	plan := audioTimelinePlan{sampleRate: defaultRate}
	silenceBytes := 0
	for _, segment := range segments {
		if err := segment.Audio.Validate(); err != nil {
			return audioTimelinePlan{}, err
		}
		if segment.Audio.Channels != 1 || segment.Audio.BitsPerSample != 16 {
			return audioTimelinePlan{}, errors.New("audio timeline requires mono signed 16-bit PCM")
		}
		if plan.pcmBytes == 0 {
			plan.sampleRate = segment.Audio.SampleRate
		}
		if segment.Audio.SampleRate != plan.sampleRate {
			return audioTimelinePlan{}, errors.New("audio timeline sample rates differ")
		}
		if segment.Offset < 0 {
			return audioTimelinePlan{}, errors.New("audio timeline offset is negative")
		}
		// Reject oversized timestamps before arithmetic or allocation. Split
		// seconds and fractional seconds to preserve nearest-sample rounding
		// without float precision or overflowing offset*sampleRate for hostile
		// timestamps.
		seconds := int64(segment.Offset / time.Second)
		rate := int64(plan.sampleRate)
		if seconds > int64(maxSessionPCMBytes/2)/rate {
			return audioTimelinePlan{}, errors.New("audio timeline exceeds session PCM limit")
		}
		fraction := int64(segment.Offset % time.Second)
		offsetSamples := seconds*rate + (fraction*rate+int64(time.Second)/2)/int64(time.Second)
		if offsetSamples > int64(maxSessionPCMBytes/2) {
			return audioTimelinePlan{}, errors.New("audio timeline exceeds session PCM limit")
		}
		offsetBytes := int(offsetSamples * 2)
		if offsetBytes < plan.pcmBytes {
			offsetBytes = plan.pcmBytes
		}
		gapBytes := offsetBytes - plan.pcmBytes
		if gapBytes > maxTimelineSilenceBytes-silenceBytes {
			return audioTimelinePlan{}, errors.New("audio timeline silence exceeds limit")
		}
		if offsetBytes > maxSessionPCMBytes-len(segment.Audio.PCM) {
			return audioTimelinePlan{}, errors.New("audio timeline exceeds session PCM limit")
		}
		silenceBytes += gapBytes
		plan.timeline = append(plan.timeline, AudioTimelineEntry{MonotonicNS: segment.MonotonicNS, StartSample: int64(offsetBytes / 2), SampleCount: int64(len(segment.Audio.PCM) / 2), SampleRate: plan.sampleRate})
		plan.segments = append(plan.segments, plannedAudioSegment{offsetBytes: offsetBytes, audio: segment.Audio})
		plan.pcmBytes = offsetBytes + len(segment.Audio.PCM)
	}
	if plan.pcmBytes == 0 {
		plan.pcmBytes = plan.sampleRate / 10 * 2
	}
	return plan, nil
}

func writeTimelineWAV(ctx context.Context, path string, segments []Segment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := planAudioTimeline(segments)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".screenreader-wav-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := streamTimelineWAV(ctx, file, plan); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func streamTimelineWAV(ctx context.Context, output io.Writer, plan audioTimelinePlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Both limits are explicit: finite session resources and representable RIFF
	// sizes. The header addition cannot wrap even if the resource limit changes.
	if plan.pcmBytes < 0 || plan.pcmBytes > maxSessionPCMBytes || uint64(plan.pcmBytes) > uint64(^uint32(0))-36 {
		return errors.New("recording WAV exceeds container limit")
	}
	var header [44]byte
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(plan.pcmBytes)+36)
	copy(header[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], uint32(plan.sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(plan.sampleRate*2))
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(plan.pcmBytes))
	if err := writeRecordingBytes(ctx, output, header[:]); err != nil {
		return err
	}
	var silence [recordingIOChunkBytes]byte
	position := 0
	writeSilence := func(count int) error {
		for count > 0 {
			size := min(count, len(silence))
			if err := writeRecordingBytes(ctx, output, silence[:size]); err != nil {
				return err
			}
			count -= size
		}
		return nil
	}
	for _, segment := range plan.segments {
		if err := writeSilence(segment.offsetBytes - position); err != nil {
			return err
		}
		if err := writeRecordingBytes(ctx, output, segment.audio.PCM); err != nil {
			return err
		}
		position = segment.offsetBytes + len(segment.audio.PCM)
	}
	return writeSilence(plan.pcmBytes - position)
}

func writeRecordingBytes(ctx context.Context, output io.Writer, value []byte) error {
	for len(value) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := min(len(value), recordingIOChunkBytes)
		written, err := output.Write(value[:size])
		if err != nil {
			return err
		}
		if written != size {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return ctx.Err()
}

func inventory(name, path, contentType string) (Artifact, error) {
	return inventoryContext(context.Background(), name, path, contentType)
}

func inventoryContext(ctx context.Context, name, path, contentType string) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	digest := sha256.New()
	bytes, err := io.Copy(digest, recordingContextReader{ctx: ctx, input: file})
	if err != nil {
		return Artifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: name, Path: path, ContentType: contentType, Bytes: bytes, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

type recordingContextReader struct {
	ctx   context.Context
	input io.Reader
}

func (r recordingContextReader) Read(value []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(value[:min(len(value), recordingIOChunkBytes)])
}

func (m *Manager) ResolveArtifact(sessionID, name string) (string, error) {
	if !validSessionID(sessionID) {
		return "", errors.New("invalid recording session id")
	}
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

func (m *Manager) RemoveArtifacts(sessionID string) error {
	if !validSessionID(sessionID) {
		return errors.New("invalid recording session id")
	}
	m.mu.Lock()
	_, active := m.sessions[sessionID]
	m.mu.Unlock()
	if active {
		return errors.New("recording session is active")
	}
	root := filepath.Clean(m.cfg.Root)
	dir := filepath.Clean(filepath.Join(root, sessionID))
	if filepath.Dir(dir) != root {
		return errors.New("recording session path escapes root")
	}
	return os.RemoveAll(dir)
}

func validSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !filepath.IsAbs(id) && filepath.Base(id) == id
}
