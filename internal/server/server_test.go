package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
func (f *fakeAccess) Events() <-chan engine.NativeEvent                     { return f.events }

type engineInjector struct{ engine *engine.Engine }

func (i engineInjector) Press(ctx context.Context, gesture string) error {
	_, err := i.engine.HandleGesture(ctx, gesture)
	return err
}

type failedCommandInjector struct {
	store     *events.Store
	sessionID string
}

func (i failedCommandInjector) Press(_ context.Context, _ string) error {
	i.store.Append(events.Event{Kind: events.KindCommandStarted, SessionID: i.sessionID, CausalCommand: "nextHeading", Text: "Next heading"})
	i.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: i.sessionID, CausalCommand: "nextHeading", Reason: "context deadline exceeded"})
	return nil
}

func testServer(t *testing.T) *Server {
	t.Helper()
	root := model.ObjectID{Bus: "app", Path: "/root"}
	heading := model.ObjectID{Bus: "app", Path: "/heading"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:    {ID: root, Role: "document web", Name: "Fixture", Children: []model.ObjectID{heading}},
		heading: {ID: heading, Parent: root, Role: "heading", Name: "Checkout", HeadingLevel: 1},
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := events.NewStore(1000)
	presenter, _ := profile.NewPresenter("en-US")
	screenreader := engine.New(&fakeAccess{graph: graph, events: make(chan engine.NativeEvent)}, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22_050}, nil, logger, engine.Config{Locale: "en-US", KeyboardLayout: "desktop"})
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
	return result
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

func TestCapabilitiesDoNotAdvertiseReservedDialogGestures(t *testing.T) {
	server := testServer(t)
	capabilities := request(t, server, http.MethodGet, "/v2/actions", nil, "test-secret")
	if capabilities.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d body=%s", capabilities.Code, capabilities.Body.String())
	}
	if bytes.Contains(capabilities.Body.Bytes(), []byte(`"id":"elementsList"`)) || bytes.Contains(capabilities.Body.Bytes(), []byte(`"id":"find"`)) {
		t.Fatalf("reserved command was advertised: %s", capabilities.Body.String())
	}
}

func TestRejectsSecondSession(t *testing.T) {
	server := testServer(t)
	first := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "one"}, "test-secret")
	second := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "two"}, "test-secret")
	if first.Code != http.StatusCreated || second.Code != http.StatusConflict {
		t.Fatalf("statuses = %d, %d", first.Code, second.Code)
	}
}
