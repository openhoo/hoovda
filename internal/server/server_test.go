package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/engine"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/recording"
	"github.com/openhoo/hoovda/internal/synth"
)

func TestRecoverDoesNotLogAttackerControlledValues(t *testing.T) {
	var logs bytes.Buffer
	server := &Server{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	handler := server.recover(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		panic(request.URL.Path)
	}))
	request := &http.Request{
		Method: "GET\nlevel=ERROR msg=forged",
		URL:    &url.URL{Path: "/safe\nlevel=ERROR msg=forged"},
	}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if strings.Contains(logs.String(), "forged") {
		t.Fatalf("recovery log contains attacker-controlled value: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "value_type=string") {
		t.Fatalf("recovery log lacks safe diagnostic type: %q", logs.String())
	}
}

type fakeAccess struct {
	graph  *model.Graph
	events chan engine.NativeEvent
}

func (f *fakeAccess) BrowserGraph(context.Context, string, model.ObjectID) (*model.Graph, error) {
	return f.graph, nil
}
func (f *fakeAccess) ReadNode(_ context.Context, id model.ObjectID) (*model.Node, error) {
	return f.graph.Nodes[id], nil
}
func (f *fakeAccess) DoDefaultAction(context.Context, model.ObjectID) error { return nil }
func (f *fakeAccess) GrabFocus(context.Context, model.ObjectID) error       { return nil }
func (f *fakeAccess) SetTextSelection(context.Context, model.ObjectID, int, int) error {
	return nil
}
func (f *fakeAccess) GenerateMouseEvent(context.Context, int, int, string) error { return nil }
func (f *fakeAccess) Events() <-chan engine.NativeEvent                          { return f.events }

type engineInjector struct{ engine *engine.Engine }

func (i engineInjector) Press(ctx context.Context, gesture string) error {
	_, err := i.engine.HandleGesture(ctx, gesture)
	return err
}

type dropFirstInjector struct {
	engine *engine.Engine
	calls  int
}

func (i *dropFirstInjector) Press(ctx context.Context, gesture string) error {
	i.calls++
	if i.calls == 1 {
		return nil
	}
	_, err := i.engine.HandleGesture(ctx, gesture)
	return err
}

type failedCommandInjector struct {
	store     *events.Store
	sessionID string
}

type delayedFocusInjector struct {
	store     *events.Store
	sessionID string
}

func (i delayedFocusInjector) Press(_ context.Context, _ string) error {
	i.store.Append(events.Event{Kind: events.KindFocus, SessionID: i.sessionID, Text: "Address and search bar", Reason: "nativeFocus"})
	i.store.Append(events.Event{Kind: events.KindCommandStarted, SessionID: i.sessionID, CausalCommand: "nextFocusable", Text: "Next focusable element"})
	i.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: i.sessionID, CausalCommand: "nextFocusable", Reason: "completed"})
	go func() {
		time.Sleep(20 * time.Millisecond)
		i.store.Append(events.Event{Kind: events.KindFocus, SessionID: i.sessionID, CausalCommand: "nextFocusable", Text: "Email", Reason: "nativeFocus"})
	}()
	return nil
}

func (i failedCommandInjector) Press(_ context.Context, _ string) error {
	i.store.Append(events.Event{Kind: events.KindCommandStarted, SessionID: i.sessionID, CausalCommand: "nextHeading", Text: "Next heading"})
	i.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: i.sessionID, CausalCommand: "nextHeading", Reason: "context deadline exceeded"})
	return nil
}

func testServer(t *testing.T) *Server {
	t.Helper()
	server, _ := testServerWithAccess(t)
	return server
}

func testServerWithAccess(t *testing.T) (*Server, *fakeAccess) {
	t.Helper()
	root := model.ObjectID{Bus: "app", Path: "/root"}
	heading := model.ObjectID{Bus: "app", Path: "/heading"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:    {ID: root, Role: "document web", Name: "Fixture", Children: []model.ObjectID{heading}, States: map[string]bool{"enabled": true}},
		heading: {ID: heading, Parent: root, Role: "heading", Name: "Checkout", HeadingLevel: 1, States: map[string]bool{"enabled": true}},
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := events.NewStore(1000)
	presenter, _ := profile.NewPresenter("en-US")
	access := &fakeAccess{graph: graph, events: make(chan engine.NativeEvent, 10)}
	screenreader := engine.New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22_050}, nil, logger, engine.Config{Locale: "en-US", KeyboardLayout: "desktop"})
	if err := screenreader.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorder, err := recording.NewManager(recording.Config{Root: filepath.Join(t.TempDir(), "artifacts"), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(context.Background(), Config{Token: "test-secret", Profile: "nvda-web-2026.1.1", Locale: "en-US", KeyboardLayout: "desktop", ActionTimeout: time.Second, QuietWindow: time.Millisecond}, screenreader, store, engineInjector{screenreader}, recorder, logger)
	if err != nil {
		t.Fatal(err)
	}
	return result, access
}

func request(t *testing.T, server *Server, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	return w
}

func TestRequiresBearerToken(t *testing.T) {
	w := request(t, testServer(t), http.MethodGet, "/v2/health", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestSessionActionAndArtifacts(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "checkout", Recording: false}, "test-secret")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "nextHeading"}, "test-secret")
	if action.Code != http.StatusOK {
		t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
	}
	var result ActionResult
	if err := json.Unmarshal(action.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Command != "nextHeading" || len(result.Events) == 0 {
		t.Fatalf("result = %#v", result)
	}
	finished := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
	if finished.Code != http.StatusOK {
		t.Fatalf("finish status = %d body=%s", finished.Code, finished.Body.String())
	}
	var finish FinishResult
	if err := json.Unmarshal(finished.Body.Bytes(), &finish); err != nil {
		t.Fatal(err)
	}
	if len(finish.Artifacts) != 3 {
		t.Fatalf("artifacts = %#v", finish.Artifacts)
	}
	artifact := request(t, server, http.MethodGet, "/v2/sessions/"+session.ID+"/artifacts/screenreader-events", nil, "test-secret")
	if artifact.Code != http.StatusOK || !bytes.Contains(artifact.Body.Bytes(), []byte("Checkout")) {
		t.Fatalf("artifact status=%d body=%s", artifact.Code, artifact.Body.String())
	}
}

func TestFinishIsIdempotentAfterSuccessfulFinalization(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "finish-retry"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	first := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
	second := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("finish statuses = %d, %d; second=%s", first.Code, second.Code, second.Body.String())
	}
	var firstResult, secondResult FinishResult
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.SessionID != secondResult.SessionID || firstResult.Cursor != secondResult.Cursor || len(firstResult.Artifacts) != len(secondResult.Artifacts) {
		t.Fatalf("finish retry changed result: first=%#v second=%#v", firstResult, secondResult)
	}
}

func TestFinishedSessionRetentionRemovesExpiredArtifacts(t *testing.T) {
	server := testServer(t)
	server.cfg.FinishedSessionLimit = 1
	finish := func(testID string) Session {
		t.Helper()
		created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: testID}, "test-secret")
		var session Session
		if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		response := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
		if response.Code != http.StatusOK {
			t.Fatalf("finish status = %d body=%s", response.Code, response.Body.String())
		}
		return session
	}
	first := finish("retained-first")
	server.mu.RLock()
	firstPath := server.finished[first.ID].artifacts["screenreader-events"].Path
	server.mu.RUnlock()
	second := finish("retained-second")
	if first.ID == second.ID {
		t.Fatal("random session IDs collided")
	}
	if response := request(t, server, http.MethodGet, "/v2/sessions/"+first.ID+"/artifacts/screenreader-events", nil, "test-secret"); response.Code != http.StatusNotFound {
		t.Fatalf("expired artifact status = %d", response.Code)
	}
	if _, err := os.Stat(filepath.Dir(firstPath)); !os.IsNotExist(err) {
		t.Fatalf("expired artifact directory still exists: %v", err)
	}
}

func TestFinishExportFailureDoesNotPoisonNextSession(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "truncated"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1001; index++ {
		server.store.Append(events.Event{Kind: events.KindSpeech, SessionID: session.ID, Text: "overflow"})
	}
	failed := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
	if failed.Code != http.StatusInternalServerError || !bytes.Contains(failed.Body.Bytes(), []byte(`"code":"event_export"`)) {
		t.Fatalf("finish status = %d body=%s", failed.Code, failed.Body.String())
	}
	next := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "after-failure"}, "test-secret")
	if next.Code != http.StatusCreated {
		t.Fatalf("next session status = %d body=%s", next.Code, next.Body.String())
	}
}

func TestStateReturnsEnrichedRuntimeObjects(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "state"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	response := request(t, server, http.MethodGet, "/v2/sessions/"+session.ID+"/state", nil, "test-secret")
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d body=%s", response.Code, response.Body.String())
	}
	var state StateResult
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Navigator == nil || state.Navigator.Name == nil || *state.Navigator.Name != "Fixture" || state.Navigator.Role == nil || *state.Navigator.Role != "document web" {
		t.Fatalf("navigator = %#v body=%s", state.Navigator, response.Body.String())
	}
	if state.Browse == nil || state.Browse.ID == "" || state.Browse.Role == nil || *state.Browse.Role != "document web" {
		t.Fatalf("browse = %#v body=%s", state.Browse, response.Body.String())
	}
}

func TestStateRefreshesDirtyDocumentMetadata(t *testing.T) {
	server, access := testServerWithAccess(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "state-refresh"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	root := access.graph.Root
	updated, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {
			ID: root, Role: "document web", Name: "Updated fixture",
			States: map[string]bool{"enabled": true, "focused": true, "showing": true},
		},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	access.graph = updated
	access.events <- engine.NativeEvent{Name: "org.a11y.atspi.Event.Document.LoadComplete", Source: root}
	deadline := time.Now().Add(time.Second)
	for server.engine.State().Focus != root && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	response := request(t, server, http.MethodGet, "/v2/sessions/"+session.ID+"/state", nil, "test-secret")
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d body=%s", response.Code, response.Body.String())
	}
	var state StateResult
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.GraphRevision != 8 || state.Focus == nil || state.Focus.Name == nil || *state.Focus.Name != "Updated fixture" {
		t.Fatalf("state = %#v body=%s", state, response.Body.String())
	}
}

func TestRuntimeObjectDoesNotExposeProtectedValue(t *testing.T) {
	node := &model.Node{
		ID:        model.ObjectID{Bus: "app", Path: "/password"},
		Role:      "password text",
		Name:      "Password",
		ValueText: "DO_NOT_LEAK",
		States:    map[string]bool{"protected": true},
	}
	result := runtimeObject(node)
	if result == nil || !result.Redacted || result.Name != nil {
		t.Fatalf("protected runtime object = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("DO_NOT_LEAK")) || bytes.Contains(encoded, []byte("Password")) {
		t.Fatalf("protected runtime object leaked content: %s", encoded)
	}
}

func TestRuntimeObjectHashesDocumentURL(t *testing.T) {
	node := &model.Node{
		ID:         model.ObjectID{Bus: "app", Path: "/document"},
		Role:       "document web",
		Name:       "Checkout",
		Attributes: map[string]string{"url": "https://example.test/checkout?token=secret"},
	}
	result := runtimeObject(node)
	if result == nil || result.DocumentURLSHA256 != "5db530ec6088c907ba40b8a1a226d06c0263d788d373cb3970653fc038d0bddf" {
		t.Fatalf("document runtime object = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("example.test")) || bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("document URL leaked from runtime object: %s", encoded)
	}
}

func TestSessionPresentationSettingsAreStrictScopedAndRecorded(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "settings"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	settingsResponse := request(t, server, http.MethodGet, "/v2/sessions/"+session.ID+"/settings", nil, "test-secret")
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("settings status = %d body=%s", settingsResponse.Code, settingsResponse.Body.String())
	}
	var baseline profile.PresentationSettings
	if err := json.Unmarshal(settingsResponse.Body.Bytes(), &baseline); err != nil {
		t.Fatal(err)
	}
	changed := baseline.Clone()
	changed.ReportHeadings = false
	changed.BrailleTether = profile.BrailleTetherReview
	set := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/settings", changed, "test-secret")
	if set.Code != http.StatusOK || !bytes.Contains(set.Body.Bytes(), []byte(`"reportHeadings":false`)) {
		t.Fatalf("set status = %d body=%s", set.Code, set.Body.String())
	}
	if state := server.engine.State(); state.BrailleTether != "review" {
		t.Fatalf("braille tether = %q", state.BrailleTether)
	}
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "nextHeading"}, "test-secret")
	if action.Code != http.StatusOK || !bytes.Contains(action.Body.Bytes(), []byte(`"text":"Checkout"`)) || bytes.Contains(action.Body.Bytes(), []byte(`Checkout  heading`)) {
		t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
	}

	invalid := map[string]any{"speechSymbolLevel": "some"}
	rejected := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/settings", invalid, "test-secret")
	if rejected.Code != http.StatusBadRequest || !bytes.Contains(rejected.Body.Bytes(), []byte(`"code":"invalid_settings"`)) {
		t.Fatalf("invalid status = %d body=%s", rejected.Code, rejected.Body.String())
	}
	reset := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/settings/reset", struct{}{}, "test-secret")
	if reset.Code != http.StatusOK || !bytes.Contains(reset.Body.Bytes(), []byte(`"reportHeadings":true`)) {
		t.Fatalf("reset status = %d body=%s", reset.Code, reset.Body.String())
	}
	if state := server.engine.State(); state.BrailleTether != "auto" {
		t.Fatalf("reset braille tether = %q", state.BrailleTether)
	}

	set = request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/settings", changed, "test-secret")
	if set.Code != http.StatusOK {
		t.Fatalf("second set status = %d body=%s", set.Code, set.Body.String())
	}
	finished := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
	if finished.Code != http.StatusOK {
		t.Fatalf("finish status = %d body=%s", finished.Code, finished.Body.String())
	}
	if got := server.engine.PresentationSettings(); !got.ReportHeadings || got.BrailleTether != profile.BrailleTetherAuto {
		t.Fatalf("settings leaked after session: %#v", got)
	}
	artifact := request(t, server, http.MethodGet, "/v2/sessions/"+session.ID+"/artifacts/screenreader-events", nil, "test-secret")
	if artifact.Code != http.StatusOK || !bytes.Contains(artifact.Body.Bytes(), []byte(`"presentationSettings"`)) || !bytes.Contains(artifact.Body.Bytes(), []byte(`"reportHeadings": false`)) {
		t.Fatalf("artifact status=%d body=%s", artifact.Code, artifact.Body.String())
	}
}

func TestActionReturnsGatewayTimeoutWhenCommandSettlesWithFailure(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "checkout"}, "test-secret")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	server.injector = failedCommandInjector{store: server.store, sessionID: session.ID}
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "nextHeading"}, "test-secret")
	if action.Code != http.StatusGatewayTimeout || !bytes.Contains(action.Body.Bytes(), []byte(`"code":"command_failed"`)) {
		t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
	}
}

func TestPhysicalActionRetriesOnceWhenKeyWasNotObserved(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "key-retry"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	injector := &dropFirstInjector{engine: server.engine}
	server.injector = injector
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "escape"}, "test-secret")
	if action.Code != http.StatusOK || injector.calls != 2 {
		t.Fatalf("action status=%d calls=%d body=%s", action.Code, injector.calls, action.Body.String())
	}
}

func TestFocusableActionWaitsForDelayedNativeFocus(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "focus-delay"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	server.injector = delayedFocusInjector{store: server.store, sessionID: session.ID}
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "nextFocusable"}, "test-secret")
	if action.Code != http.StatusOK {
		t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
	}
	var result ActionResult
	if err := json.Unmarshal(action.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	focused := false
	for _, event := range result.Events {
		focused = focused || event.Kind == events.KindFocus
		if event.CausalCommand != "nextFocusable" {
			t.Fatalf("unrelated event leaked into action result: %#v", event)
		}
	}
	if !focused {
		t.Fatalf("delayed native focus missing from result: %#v", result.Events)
	}
}

func TestCapabilitiesAdvertiseImplementedDialogCommands(t *testing.T) {
	server := testServer(t)
	capabilities := request(t, server, http.MethodGet, "/v2/actions", nil, "test-secret")
	if capabilities.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d body=%s", capabilities.Code, capabilities.Body.String())
	}
	if !bytes.Contains(capabilities.Body.Bytes(), []byte(`"id":"elementsList"`)) || !bytes.Contains(capabilities.Body.Bytes(), []byte(`"id":"find"`)) {
		t.Fatalf("implemented command was not advertised: %s", capabilities.Body.String())
	}
}

func TestStructuredFindActionReturnsEvidence(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "find"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	query := "Checkout"
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "find", Argument: &query}, "test-secret")
	if action.Code != http.StatusOK {
		t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
	}
	var result ActionResult
	if err := json.Unmarshal(action.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Delivery != "structured" || !bytes.Contains(action.Body.Bytes(), []byte("Checkout")) {
		t.Fatalf("result = %#v body=%s", result, action.Body.String())
	}
}

func TestUnassignedQuickNavigationUsesStructuredDelivery(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "direct-quick-nav"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "nextArticle"}, "test-secret")
	if action.Code != http.StatusOK {
		t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
	}
	var result ActionResult
	if err := json.Unmarshal(action.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Delivery != "structured" || result.Gesture != "script:nextArticle" || len(result.Events) == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFindRequiresBoundedStructuredArgument(t *testing.T) {
	for _, test := range []struct {
		name     string
		argument *string
	}{
		{name: "missing"},
		{name: "empty", argument: stringPointer("   ")},
		{name: "too long", argument: stringPointer(strings.Repeat("x", 501))},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t)
			created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "find-invalid"}, "test-secret")
			var session Session
			if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "find", Argument: test.argument}, "test-secret")
			if action.Code != http.StatusBadRequest || !bytes.Contains(action.Body.Bytes(), []byte(`"code":"invalid_argument"`)) {
				t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
			}
		})
	}
}

func TestNonFindActionRejectsStructuredArgument(t *testing.T) {
	server := testServer(t)
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "invalid-argument"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	argument := "unexpected"
	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "nextHeading", Argument: &argument}, "test-secret")
	if action.Code != http.StatusBadRequest || !bytes.Contains(action.Body.Bytes(), []byte(`"code":"invalid_argument"`)) {
		t.Fatalf("action status = %d body=%s", action.Code, action.Body.String())
	}
}

func TestExitEmbeddedObjectRequiresFocusOnlyForActualBoundaryExit(t *testing.T) {
	if commandNeedsNativeFocus("exitEmbeddedObject", nil) {
		t.Fatal("ordinary iframe no-op must not wait for an impossible focus event")
	}
	observed := []events.Event{{
		Kind: events.KindSpeech, CausalCommand: "exitEmbeddedObject", Reason: "embeddedObjectExit",
	}}
	if !commandNeedsNativeFocus("exitEmbeddedObject", observed) {
		t.Fatal("actual embedded-object exit must retain native focus proof")
	}
	if !commandNeedsNativeFocus("nextFocusable", nil) {
		t.Fatal("focus traversal must always retain native focus proof")
	}
	if commandNeedsNativeFocus("returnToPage", nil) {
		t.Fatal("F6 focus-ring cycling must let the client verify and retry an unchanged stop")
	}
}

func TestActionResultExcludesEventsMisattributedBeforeCommandStart(t *testing.T) {
	result := filterActionResultEvents([]events.Event{
		{Sequence: 10, Kind: events.KindFocus, CausalCommand: "exitEmbeddedObject", Text: "late prior focus"},
		{Sequence: 11, Kind: events.KindCommandStarted, CausalCommand: "exitEmbeddedObject"},
		{Sequence: 12, Kind: events.KindCommandSettled, CausalCommand: "exitEmbeddedObject", Reason: "completed"},
		{Sequence: 13, Kind: events.KindSpeech, CausalCommand: "nextHeading", Text: "unrelated"},
	}, "exitEmbeddedObject", 11)
	if len(result) != 2 || result[0].Sequence != 11 || result[1].Sequence != 12 {
		t.Fatalf("filtered result = %#v", result)
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestRejectsSecondSession(t *testing.T) {
	server := testServer(t)
	first := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "one"}, "test-secret")
	second := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "two"}, "test-secret")
	if first.Code != http.StatusCreated || second.Code != http.StatusConflict {
		t.Fatalf("statuses = %d, %d", first.Code, second.Code)
	}
}
