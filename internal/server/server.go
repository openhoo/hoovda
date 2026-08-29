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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openhoo/hoovda/internal/buildinfo"
	"github.com/openhoo/hoovda/internal/engine"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/input"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/recording"
)

type Config struct {
	Token                string
	Profile              string
	Locale               string
	KeyboardLayout       string
	ActionTimeout        time.Duration
	FinishTimeout        time.Duration
	QuietWindow          time.Duration
	FinishedSessionLimit int
}

type activeSession struct {
	Session
	mu sync.Mutex
}

type finishedSession struct {
	result    FinishResult
	artifacts map[string]recording.Artifact
}

type Server struct {
	ctx      context.Context
	cfg      Config
	engine   *engine.Engine
	store    *events.Store
	injector input.Injector
	recorder *recording.Manager
	logger   *slog.Logger

	mu            sync.RWMutex
	active        map[string]*activeSession
	finished      map[string]*finishedSession
	finishedOrder []string
	sem           chan struct{}
	handler       http.Handler
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
	if cfg.FinishedSessionLimit <= 0 {
		cfg.FinishedSessionLimit = 32
	}
	s := &Server{
		ctx: ctx, cfg: cfg, engine: screenreader, store: store, injector: injector,
		recorder: recorder, logger: logger, active: map[string]*activeSession{},
		finished: map[string]*finishedSession{}, sem: make(chan struct{}, 1),
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
	mux.HandleFunc("GET /v2/sessions/{session}/settings", s.presentationSettings)
	mux.HandleFunc("POST /v2/sessions/{session}/settings", s.setPresentationSettings)
	mux.HandleFunc("POST /v2/sessions/{session}/settings/reset", s.resetPresentationSettings)
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
	writeJSON(w, http.StatusOK, s.stateResult())
}

func (s *Server) stateResult() StateResult {
	state := s.engine.State()
	nodes := s.engine.DocumentSnapshot()
	byID := make(map[model.ObjectID]*model.Node, len(nodes))
	for index := range nodes {
		node := &nodes[index]
		byID[node.ID] = node
	}
	result := StateResult{
		Ready: state.Ready, GraphRevision: state.GraphRevision, Cursor: state.Cursor,
		Browse:    runtimeObject(byID[state.Cursor.Object]),
		Navigator: runtimeObject(byID[state.Navigator]), Review: runtimeObject(byID[state.Review.Object]),
		ReviewCopyStart: state.ReviewCopyStart, ReviewSelection: state.ReviewSelection,
		CursorInDocument: state.CursorInDocument, Focus: runtimeObject(byID[state.Focus]),
		BrowserWindowActive: state.BrowserWindowActive, WebContentFocused: state.WebContentFocused,
		SingleLetterNavigation: state.SingleLetterNavigation, NativeSelectionMode: state.NativeSelectionMode,
		LeftMouseLocked: state.LeftMouseLocked, RightMouseLocked: state.RightMouseLocked,
		SpeechMode: state.SpeechMode, SpeechPaused: state.SpeechPaused,
		BrailleOffset: state.BrailleOffset, BrailleTether: state.BrailleTether, LastSequence: state.LastSequence,
	}
	if state.MousePositionKnown {
		result.Mouse = &RuntimeMouse{X: state.MouseX, Y: state.MouseY, Object: runtimeObject(nodeAtPoint(nodes, state.MouseX, state.MouseY))}
	}
	return result
}

func runtimeObject(node *model.Node) *RuntimeObject {
	if node == nil {
		return nil
	}
	result := &RuntimeObject{ID: node.ID.String()}
	if role := strings.TrimSpace(node.Role); role != "" {
		result.Role = &role
	}
	if node.IsProtected() {
		result.Redacted = true
	} else if name := strings.TrimSpace(node.Name); name != "" {
		result.Name = &name
	}
	if strings.Contains(strings.ToLower(node.Role), "link") {
		visited := node.HasState("visited")
		result.Visited = &visited
	}
	seenTargets := make(map[string]bool)
	for _, command := range profile.Commands() {
		if command.Category != "quickNavigation" || command.Target == "" || seenTargets[command.Target] {
			continue
		}
		seenTargets[command.Target] = true
		if profile.MatchTarget(command.Target)(node) {
			result.QuickNavigationTargets = append(result.QuickNavigationTargets, command.Target)
		}
	}
	if node.Bounds.Width > 0 && node.Bounds.Height > 0 {
		result.Location = &RuntimeLocation{Left: node.Bounds.X, Top: node.Bounds.Y, Width: node.Bounds.Width, Height: node.Bounds.Height}
	}
	return result
}

func nodeAtPoint(nodes []model.Node, x, y int) *model.Node {
	for index := len(nodes) - 1; index >= 0; index-- {
		node := &nodes[index]
		if node.Bounds.Width > 0 && node.Bounds.Height > 0 &&
			x >= node.Bounds.X && x < node.Bounds.X+node.Bounds.Width &&
			y >= node.Bounds.Y && y < node.Bounds.Y+node.Bounds.Height {
			return node
		}
	}
	return nil
}

func (s *Server) presentationSettings(w http.ResponseWriter, r *http.Request) {
	session, ok := s.lookupActive(w, r.PathValue("session"))
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	writeJSON(w, http.StatusOK, s.engine.PresentationSettings())
}

func (s *Server) setPresentationSettings(w http.ResponseWriter, r *http.Request) {
	session, ok := s.lookupActive(w, r.PathValue("session"))
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	var settings profile.PresentationSettings
	if err := decodeJSON(r, &settings); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err)
		return
	}
	if err := s.engine.SetPresentationSettings(session.ID, settings); err != nil {
		writeError(w, http.StatusConflict, "settings_not_applied", err)
		return
	}
	writeJSON(w, http.StatusOK, s.engine.PresentationSettings())
}

func (s *Server) resetPresentationSettings(w http.ResponseWriter, r *http.Request) {
	session, ok := s.lookupActive(w, r.PathValue("session"))
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	var request struct{}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	settings, err := s.engine.ResetPresentationSettings(session.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "settings_not_reset", err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
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
	argument := ""
	if request.Argument != nil {
		argument = strings.TrimSpace(*request.Argument)
	}
	if request.Argument != nil && (argument == "" || len(argument) > 500) {
		writeError(w, http.StatusBadRequest, "invalid_argument", errors.New("action argument must contain 1 to 500 bytes"))
		return
	}
	acceptsArgument := command.ID == "find" || command.ID == "brailleRoute" || command.ID == "brailleReportFormatting"
	if request.Argument != nil && !acceptsArgument {
		writeError(w, http.StatusBadRequest, "invalid_argument", fmt.Errorf("command %q does not accept an argument", command.ID))
		return
	}
	if request.Argument == nil && command.ID == "find" {
		writeError(w, http.StatusBadRequest, "invalid_argument", errors.New("find requires an argument through the structured action API"))
		return
	}
	if request.Argument != nil && (command.ID == "brailleRoute" || command.ID == "brailleReportFormatting") {
		cell, parseErr := strconv.Atoi(argument)
		if parseErr != nil || cell < 0 || cell > 199 {
			writeError(w, http.StatusBadRequest, "invalid_argument", errors.New("braille cell must be an integer from 0 to 199"))
			return
		}
	}
	if err := s.engine.BeginAction(session.ID, command.ID); err != nil {
		writeError(w, http.StatusConflict, "action_not_started", err)
		return
	}
	defer s.engine.EndAction(session.ID, command.ID)
	before := s.store.Cursor()
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()
	delivery := "physical"
	gesture := ""
	if request.Argument != nil {
		delivery = "structured"
		gesture = "structured:" + command.ID
		_ = s.engine.ExecuteDirectWithArgument(ctx, command.ID, argument)
	} else if len(gestures) == 0 {
		delivery = "structured"
		gesture = "script:" + command.ID
		_ = s.engine.ExecuteDirect(ctx, command.ID)
	} else {
		gesture = gestures[0]
		if err := s.engine.PreparePhysicalCommand(ctx, command.ID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "command_preparation", err)
			return
		}
		if err := s.injector.Press(ctx, gesture); err != nil {
			writeError(w, http.StatusInternalServerError, "input_injection", err)
			return
		}
		acknowledgementTimeout := min(s.cfg.ActionTimeout/5, 500*time.Millisecond)
		ackCtx, cancelAcknowledgement := context.WithTimeout(ctx, acknowledgementTimeout)
		_, _, started, ackErr := s.store.WaitFor(ackCtx, before, session.ID, func(event events.Event) bool {
			return event.Kind == events.KindCommandStarted && event.CausalCommand == command.ID
		})
		cancelAcknowledgement()
		if ackErr != nil {
			writeError(w, http.StatusBadRequest, "event_cursor", ackErr)
			return
		}
		if !started {
			if err := s.injector.Press(ctx, gesture); err != nil {
				writeError(w, http.StatusInternalServerError, "input_injection_retry", err)
				return
			}
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
			writeError(w, http.StatusGatewayTimeout, "command_timeout", fmt.Errorf("%s delivery %q began command %q but it did not settle before %s", delivery, gesture, command.ID, s.cfg.ActionTimeout))
		} else {
			writeError(w, http.StatusGatewayTimeout, "command_not_observed", fmt.Errorf("%s delivery %q did not start command %q before %s", delivery, gesture, command.ID, s.cfg.ActionTimeout))
		}
		return
	}
	startedSequence := before
	for _, event := range observed {
		if event.Kind == events.KindCommandStarted && event.CausalCommand == command.ID {
			startedSequence = event.Sequence
		}
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
	if commandNeedsNativeFocus(command.ID, observed) {
		_, _, focused, err := s.store.WaitFor(ctx, startedSequence, session.ID, func(event events.Event) bool {
			return event.Kind == events.KindFocus && event.CausalCommand == command.ID
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "event_cursor", err)
			return
		}
		if !focused {
			writeError(w, http.StatusGatewayTimeout, "focus_not_observed", fmt.Errorf("%s delivery %q completed command %q but no native focus event arrived before %s", delivery, gesture, command.ID, s.cfg.ActionTimeout))
			return
		}
	}
	cancel()
	tailCtx, cancelTail := context.WithTimeout(r.Context(), max(2*s.cfg.QuietWindow, time.Second))
	defer cancelTail()
	resultEvents, cursor, timedOut, err := s.store.Wait(tailCtx, before, session.ID, s.cfg.QuietWindow)
	if err != nil {
		writeError(w, http.StatusBadRequest, "event_cursor", err)
		return
	}
	resultEvents = slices.DeleteFunc(resultEvents, func(event events.Event) bool {
		return event.CausalCommand != command.ID
	})
	writeJSON(w, http.StatusOK, ActionResult{Command: command.ID, Gesture: gesture, Delivery: delivery, BeforeSequence: before, Cursor: cursor, TimedOut: timedOut, Events: resultEvents, State: s.stateResult()})
}

func commandNeedsNativeFocus(commandID string, observed []events.Event) bool {
	switch commandID {
	case "nextFocusable", "previousFocusable", "returnToPage":
		return true
	case "exitEmbeddedObject":
		for _, event := range observed {
			if event.CausalCommand == commandID && event.Reason == "embeddedObjectExit" {
				return true
			}
		}
		return false
	default:
		return false
	}
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
	var request struct{}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	id := r.PathValue("session")
	if result, ok := s.lookupFinished(id); ok {
		writeJSON(w, http.StatusOK, result)
		return
	}
	session, ok := s.lookupActive(w, id)
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	// A concurrent retry can resolve the active pointer before the first
	// request finishes. Re-check after serializing on the per-session lock.
	if result, completed := s.lookupFinished(id); completed {
		writeJSON(w, http.StatusOK, result)
		return
	}
	s.mu.RLock()
	stillActive := s.active[id] == session
	s.mu.RUnlock()
	if !stillActive {
		writeError(w, http.StatusNotFound, "session_not_found", errors.New("active session not found"))
		return
	}

	synthesisCtx, cancelSynthesis := context.WithTimeout(r.Context(), s.cfg.FinishTimeout)
	if err := s.engine.WaitForSynthesis(synthesisCtx); err != nil {
		cancelSynthesis()
		s.abandonSession(session)
		writeError(w, http.StatusGatewayTimeout, "speech_synthesis", err)
		return
	}
	cancelSynthesis()

	exportCtx, cancelExport := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	allEvents, cursor, _, err := s.store.Wait(exportCtx, session.StartSequence, session.ID, s.cfg.QuietWindow)
	cancelExport()
	if err != nil {
		s.abandonSession(session)
		writeError(w, http.StatusInternalServerError, "event_export", err)
		return
	}
	settings := s.engine.PresentationSettings()
	eventJSON, err := json.MarshalIndent(EventsResult{Cursor: cursor, Events: allEvents, PresentationSettings: &settings}, "", "  ")
	if err == nil {
		_, err = s.recorder.WriteJSON(session.ID, "screenreader-events", append(eventJSON, '\n'))
	}
	if err != nil {
		s.abandonSession(session)
		writeError(w, http.StatusInternalServerError, "event_export", err)
		return
	}
	refreshCtx, cancelRefresh := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	refreshErr := s.engine.Sync(refreshCtx)
	cancelRefresh()
	if refreshErr != nil {
		s.abandonSession(session)
		writeError(w, http.StatusServiceUnavailable, "document_refresh", refreshErr)
		return
	}
	state := s.engine.State()
	documentJSON, err := json.MarshalIndent(DocumentResult{Profile: s.cfg.Profile, Locale: s.cfg.Locale, Revision: state.GraphRevision, Nodes: s.engine.DocumentSnapshot()}, "", "  ")
	if err == nil {
		_, err = s.recorder.WriteJSON(session.ID, "screenreader-document", append(documentJSON, '\n'))
	}
	if err != nil {
		s.abandonSession(session)
		writeError(w, http.StatusInternalServerError, "document_export", err)
		return
	}
	finishCtx, cancelFinish := context.WithTimeout(r.Context(), s.cfg.FinishTimeout)
	artifacts, finishErr := s.recorder.Finish(finishCtx, session.ID)
	cancelFinish()
	s.engine.EndSession(session.ID)
	result := FinishResult{SessionID: session.ID, Cursor: cursor, Artifacts: artifacts}
	var expired []string
	s.mu.Lock()
	delete(s.active, session.ID)
	if finishErr == nil {
		items := make(map[string]recording.Artifact, len(artifacts))
		for _, artifact := range artifacts {
			items[artifact.Name] = artifact
		}
		s.finished[session.ID] = &finishedSession{result: result, artifacts: items}
		s.finishedOrder = append(s.finishedOrder, session.ID)
		for len(s.finishedOrder) > s.cfg.FinishedSessionLimit {
			oldest := s.finishedOrder[0]
			s.finishedOrder = s.finishedOrder[1:]
			delete(s.finished, oldest)
			expired = append(expired, oldest)
		}
	}
	s.mu.Unlock()
	<-s.sem
	for _, expiredID := range expired {
		if removeErr := s.recorder.RemoveArtifacts(expiredID); removeErr != nil {
			s.logger.Warn("remove expired artifacts", "session", expiredID, "error", removeErr)
		}
	}
	if finishErr != nil {
		if removeErr := s.recorder.RemoveArtifacts(session.ID); removeErr != nil {
			s.logger.Warn("remove failed session artifacts", "session", session.ID, "error", removeErr)
		}
		writeError(w, http.StatusInternalServerError, "recording_finish", finishErr)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) abandonSession(session *activeSession) {
	s.engine.EndSession(session.ID)
	finishCtx, cancel := context.WithTimeout(context.Background(), s.cfg.FinishTimeout)
	_, finishErr := s.recorder.Finish(finishCtx, session.ID)
	cancel()
	if finishErr != nil {
		s.logger.Warn("finalize abandoned recording", "session", session.ID, "error", finishErr)
	}
	s.mu.Lock()
	if s.active[session.ID] == session {
		delete(s.active, session.ID)
	}
	s.mu.Unlock()
	select {
	case <-s.sem:
	default:
		s.logger.Error("session semaphore was already released", "session", session.ID)
	}
	if removeErr := s.recorder.RemoveArtifacts(session.ID); removeErr != nil {
		s.logger.Warn("remove abandoned session artifacts", "session", session.ID, "error", removeErr)
	}
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	finished := s.finished[r.PathValue("session")]
	var item recording.Artifact
	var ok bool
	if finished != nil {
		item, ok = finished.artifacts[r.PathValue("artifact")]
	}
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

func (s *Server) lookupFinished(id string) (FinishResult, bool) {
	s.mu.RLock()
	finished := s.finished[id]
	s.mu.RUnlock()
	if finished == nil {
		return FinishResult{}, false
	}
	return finished.result, true
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
