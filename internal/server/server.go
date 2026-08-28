package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openhoo/hoovda/internal/buildinfo"
	"github.com/openhoo/hoovda/internal/engine"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/input"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/recording"
)

type Config struct {
	Token          string
	Profile        string
	Locale         string
	KeyboardLayout string
	ActionTimeout  time.Duration
	FinishTimeout  time.Duration
	QuietWindow    time.Duration
}

type activeSession struct {
	Session
	mu sync.Mutex
}

type Server struct {
	ctx      context.Context
	cfg      Config
	engine   *engine.Engine
	store    *events.Store
	injector input.Injector
	recorder *recording.Manager
	logger   *slog.Logger

	mu       sync.RWMutex
	active   map[string]*activeSession
	finished map[string]map[string]recording.Artifact
	sem      chan struct{}
	handler  http.Handler
}

func New(ctx context.Context, cfg Config, screenreader *engine.Engine, store *events.Store, injector input.Injector, recorder *recording.Manager, logger *slog.Logger) (*Server, error) {
	if ctx == nil || cfg.Token == "" || cfg.Profile == "" || cfg.Locale == "" || screenreader == nil || store == nil || injector == nil || recorder == nil || logger == nil {
		return nil, errors.New("invalid server dependency")
	}
	if cfg.ActionTimeout <= 0 {
		cfg.ActionTimeout = 15 * time.Second
	}
	if cfg.QuietWindow <= 0 {
		cfg.QuietWindow = 300 * time.Millisecond
	}
	if cfg.FinishTimeout <= 0 {
		cfg.FinishTimeout = 30 * time.Second
	}
	s := &Server{
		ctx: ctx, cfg: cfg, engine: screenreader, store: store, injector: injector,
		recorder: recorder, logger: logger, active: map[string]*activeSession{},
		finished: map[string]map[string]recording.Artifact{}, sem: make(chan struct{}, 1),
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/health", s.health)
	mux.HandleFunc("GET /v2/actions", s.actions)
	mux.HandleFunc("POST /v2/sessions", s.createSession)
	mux.HandleFunc("GET /v2/sessions/{session}/state", s.state)
	mux.HandleFunc("POST /v2/sessions/{session}/actions", s.action)
	mux.HandleFunc("GET /v2/sessions/{session}/events", s.sessionEvents)
	mux.HandleFunc("GET /v2/sessions/{session}/document", s.document)
	mux.HandleFunc("POST /v2/sessions/{session}/finish", s.finish)
	mux.HandleFunc("GET /v2/sessions/{session}/artifacts/{artifact}", s.artifact)
	return s.recover(s.authenticate(http.MaxBytesHandler(mux, 1<<20)))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{
		Status: "ok", ProtocolVersion: ProtocolVersion, Version: buildinfo.Version,
		Commit: buildinfo.Commit, Profile: s.cfg.Profile, Locale: s.cfg.Locale,
		KeyboardLayout: s.cfg.KeyboardLayout, Ready: s.engine.State().Ready,
	})
}

func (s *Server) actions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, ActionsResult{Profile: s.cfg.Profile, KeyboardLayout: s.cfg.KeyboardLayout, Commands: profile.SupportedCommands()})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var request CreateSessionRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	request.TestID = strings.TrimSpace(request.TestID)
	if request.TestID == "" || len(request.TestID) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_test_id", errors.New("testId must contain 1 to 500 bytes"))
		return
	}
	select {
	case s.sem <- struct{}{}:
	default:
		writeError(w, http.StatusConflict, "session_active", errors.New("another test session is active"))
		return
	}
	id, err := randomID()
	if err != nil {
		<-s.sem
		writeError(w, http.StatusInternalServerError, "session_id", err)
		return
	}
	created := Session{ID: id, TestID: request.TestID, Recording: request.Recording, StartSequence: s.store.Cursor(), CreatedAt: time.Now().UTC()}
	if err := s.engine.BeginSession(id); err != nil {
		<-s.sem
		writeError(w, http.StatusConflict, "session_active", err)
		return
	}
	if err := s.recorder.Start(s.ctx, id, s.store.MonotonicNS(), request.Recording); err != nil {
		s.engine.EndSession(id)
		<-s.sem
		writeError(w, http.StatusInternalServerError, "recording_start", err)
		return
	}
	s.mu.Lock()
	s.active[id] = &activeSession{Session: created}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupActive(w, r.PathValue("session")); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.engine.State())
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	session, ok := s.lookupActive(w, r.PathValue("session"))
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	var request ActionRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	command, ok := profile.SupportedCommandByID(request.Command)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported_command", fmt.Errorf("unsupported command %q", request.Command))
		return
	}
	gestures := command.Desktop
	if s.cfg.KeyboardLayout == "laptop" {
		gestures = command.Laptop
	}
	if len(gestures) == 0 {
		writeError(w, http.StatusNotImplemented, "missing_gesture", fmt.Errorf("command %q has no %s gesture", command.ID, s.cfg.KeyboardLayout))
		return
	}
	argument := ""
	if request.Argument != nil {
		argument = strings.TrimSpace(*request.Argument)
	}
	if request.Argument != nil && (argument == "" || len(argument) > 500) {
		writeError(w, http.StatusBadRequest, "invalid_argument", errors.New("action argument must contain 1 to 500 bytes"))
		return
	}
	if request.Argument != nil && command.ID != "find" {
		writeError(w, http.StatusBadRequest, "invalid_argument", fmt.Errorf("command %q does not accept an argument", command.ID))
		return
	}
	if request.Argument == nil && command.ID == "find" {
		writeError(w, http.StatusBadRequest, "invalid_argument", errors.New("find requires an argument through the structured action API"))
		return
	}
	before := s.store.Cursor()
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()
	delivery := "physical"
	if request.Argument != nil {
		delivery = "structured"
		_ = s.engine.ExecuteDirectWithArgument(ctx, command.ID, argument)
	} else {
		if err := s.injector.Press(ctx, gestures[0]); err != nil {
			writeError(w, http.StatusInternalServerError, "input_injection", err)
			return
		}
	}
	observed, _, settled, err := s.store.WaitFor(ctx, before, session.ID, func(event events.Event) bool {
		return event.Kind == events.KindCommandSettled && event.CausalCommand == command.ID
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "event_cursor", err)
		return
	}
	if !settled {
		started := false
		for _, event := range observed {
			if event.Kind == events.KindCommandStarted && event.CausalCommand == command.ID {
				started = true
				break
			}
		}
		if started {
			writeError(w, http.StatusGatewayTimeout, "command_timeout", fmt.Errorf("physical gesture %q began command %q but it did not settle before %s", gestures[0], command.ID, s.cfg.ActionTimeout))
		} else {
			writeError(w, http.StatusGatewayTimeout, "command_not_observed", fmt.Errorf("physical gesture %q did not start command %q before %s", gestures[0], command.ID, s.cfg.ActionTimeout))
		}
		return
	}
	for _, event := range observed {
		if event.Kind != events.KindCommandSettled || event.CausalCommand != command.ID || event.Reason == "completed" {
			continue
		}
		status := http.StatusInternalServerError
		if strings.Contains(event.Reason, "deadline exceeded") || strings.Contains(event.Reason, "timed out") {
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, "command_failed", fmt.Errorf("command %q failed: %s", command.ID, event.Reason))
		return
	}
	cancel()
	tailCtx, cancelTail := context.WithTimeout(r.Context(), max(2*s.cfg.QuietWindow, time.Second))
	defer cancelTail()
	resultEvents, cursor, timedOut, err := s.store.Wait(tailCtx, before, session.ID, s.cfg.QuietWindow)
	if err != nil {
		writeError(w, http.StatusBadRequest, "event_cursor", err)
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{Command: command.ID, Gesture: gestures[0], Delivery: delivery, BeforeSequence: before, Cursor: cursor, TimedOut: timedOut, Events: resultEvents, State: s.engine.State()})
}

func (s *Server) sessionEvents(w http.ResponseWriter, r *http.Request) {
	session, ok := s.lookupActive(w, r.PathValue("session"))
	if !ok {
		return
	}
	after, err := parseUintQuery(r, "after", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_after", err)
		return
	}
	timeoutMS, err := parseUintQuery(r, "timeoutMs", 0)
	if err != nil || timeoutMS > 60_000 {
		writeError(w, http.StatusBadRequest, "invalid_timeout", errors.New("timeoutMs must be between 0 and 60000"))
		return
	}
	if timeoutMS == 0 {
		resultEvents, cursor, snapshotErr := s.store.Snapshot(after, session.ID)
		if snapshotErr != nil {
			writeError(w, http.StatusBadRequest, "event_cursor", snapshotErr)
			return
		}
		writeJSON(w, http.StatusOK, EventsResult{Cursor: cursor, Events: resultEvents})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	resultEvents, cursor, timedOut, waitErr := s.store.Wait(ctx, after, session.ID, s.cfg.QuietWindow)
	if waitErr != nil {
		writeError(w, http.StatusBadRequest, "event_cursor", waitErr)
		return
	}
	writeJSON(w, http.StatusOK, EventsResult{Cursor: cursor, TimedOut: timedOut, Events: resultEvents})
}

func (s *Server) document(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupActive(w, r.PathValue("session")); !ok {
		return
	}
	if err := s.engine.Sync(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "document_refresh", err)
		return
	}
	state := s.engine.State()
	writeJSON(w, http.StatusOK, DocumentResult{Profile: s.cfg.Profile, Locale: s.cfg.Locale, Revision: state.GraphRevision, Nodes: s.engine.DocumentSnapshot()})
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request) {
	session, ok := s.lookupActive(w, r.PathValue("session"))
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()

	synthesisCtx, cancelSynthesis := context.WithTimeout(r.Context(), s.cfg.FinishTimeout)
	if err := s.engine.WaitForSynthesis(synthesisCtx); err != nil {
		cancelSynthesis()
		writeError(w, http.StatusGatewayTimeout, "speech_synthesis", err)
		return
	}
	cancelSynthesis()

	exportCtx, cancelExport := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	allEvents, cursor, _, err := s.store.Wait(exportCtx, session.StartSequence, session.ID, s.cfg.QuietWindow)
	cancelExport()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event_export", err)
		return
	}
	eventJSON, err := json.MarshalIndent(EventsResult{Cursor: cursor, Events: allEvents}, "", "  ")
	if err == nil {
		_, err = s.recorder.WriteJSON(session.ID, "screenreader-events", append(eventJSON, '\n'))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event_export", err)
		return
	}
	refreshCtx, cancelRefresh := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	refreshErr := s.engine.Sync(refreshCtx)
	cancelRefresh()
	if refreshErr != nil {
		writeError(w, http.StatusServiceUnavailable, "document_refresh", refreshErr)
		return
	}
	state := s.engine.State()
	documentJSON, err := json.MarshalIndent(DocumentResult{Profile: s.cfg.Profile, Locale: s.cfg.Locale, Revision: state.GraphRevision, Nodes: s.engine.DocumentSnapshot()}, "", "  ")
	if err == nil {
		_, err = s.recorder.WriteJSON(session.ID, "screenreader-document", append(documentJSON, '\n'))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "document_export", err)
		return
	}
	finishCtx, cancelFinish := context.WithTimeout(r.Context(), s.cfg.FinishTimeout)
	artifacts, finishErr := s.recorder.Finish(finishCtx, session.ID)
	cancelFinish()
	s.engine.EndSession(session.ID)
	s.mu.Lock()
	delete(s.active, session.ID)
	if finishErr == nil {
		items := make(map[string]recording.Artifact, len(artifacts))
		for _, artifact := range artifacts {
			items[artifact.Name] = artifact
		}
		s.finished[session.ID] = items
	}
	s.mu.Unlock()
	<-s.sem
	if finishErr != nil {
		writeError(w, http.StatusInternalServerError, "recording_finish", finishErr)
		return
	}
	writeJSON(w, http.StatusOK, FinishResult{SessionID: session.ID, Cursor: cursor, Artifacts: artifacts})
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	items := s.finished[r.PathValue("session")]
	item, ok := items[r.PathValue("artifact")]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "artifact_not_found", errors.New("artifact not found"))
		return
	}
	file, err := os.Open(item.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact_not_found", err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", item.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(item.Bytes, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", item.Name))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		s.logger.Warn("stream artifact", "session", r.PathValue("session"), "artifact", item.Name, "error", err)
	}
}

func (s *Server) lookupActive(w http.ResponseWriter, id string) (*activeSession, bool) {
	s.mu.RLock()
	session := s.active[id]
	s.mu.RUnlock()
	if session == nil {
		writeError(w, http.StatusNotFound, "session_not_found", errors.New("active session not found"))
		return nil, false
	}
	return session, true
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "Bearer "
		provided := r.Header.Get("Authorization")
		if !strings.HasPrefix(provided, prefix) || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(provided, prefix)), []byte(s.cfg.Token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", errors.New("valid bearer token required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("HTTP handler panic", "value", value, "method", r.Method, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal", errors.New("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func parseUintQuery(r *http.Request, name string, fallback uint64) (uint64, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "hv-" + hex.EncodeToString(data), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}
