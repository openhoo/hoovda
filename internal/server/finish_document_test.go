package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/engine"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/synth"
)

func TestFinishPreservesEvidenceWithoutFinalDocument(t *testing.T) {
	for _, failure := range []string{"unavailable", "deadline", "request-canceled"} {
		t.Run(failure, func(t *testing.T) {
			server, access := testServerWithAccess(t)
			created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "closed-page"}, "test-secret")
			if created.Code != http.StatusCreated {
				t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
			}
			var session Session
			if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "nextHeading"}, "test-secret")
			if action.Code != http.StatusOK {
				t.Fatalf("action status=%d body=%s", action.Code, action.Body.String())
			}
			server.cfg.ActionTimeout = 40 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			access.setGraphFailure(func(refreshCtx context.Context) error {
				switch failure {
				case "unavailable":
					return errors.New("accessible document closed")
				case "request-canceled":
					cancel()
				}
				<-refreshCtx.Done()
				return refreshCtx.Err()
			})
			// A native document event invalidates the graph after the last action.
			// The unique source lets this test wait for event-loop acknowledgement
			// before issuing finish, without relying on sleeps or a cached graph.
			closedDocument := model.ObjectID{Bus: "app", Path: "/closed-document"}
			access.events <- engine.NativeEvent{Name: "org.a11y.atspi.Event.Document.LoadStopped", Source: closedDocument}
			deadline := time.Now().Add(time.Second)
			for server.engine.State().Focus != closedDocument && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if server.engine.State().Focus != closedDocument {
				t.Fatal("native document invalidation was not processed")
			}
			r := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+session.ID+"/finish", strings.NewReader("{}")).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer test-secret")
			finished := httptest.NewRecorder()
			server.Handler().ServeHTTP(finished, r)
			if failure == "request-canceled" {
				if finished.Code != http.StatusGatewayTimeout {
					t.Fatalf("canceled finish status=%d body=%s", finished.Code, finished.Body.String())
				}
				blocked := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "must-wait-for-retry"}, "test-secret")
				if blocked.Code != http.StatusConflict {
					t.Fatalf("canceled finish released exclusive session: status=%d body=%s", blocked.Code, blocked.Body.String())
				}
				// Export preparation has completed. A fresh request must finalize
				// it without querying the inaccessible document again.
				finished = request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
			}
			if finished.Code != http.StatusOK {
				t.Fatalf("finish discarded valid evidence: status=%d body=%s", finished.Code, finished.Body.String())
			}
			var result FinishResult
			if err := json.Unmarshal(finished.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Artifacts) != 2 {
				t.Fatalf("expected events and audio without a stale document: %#v", result.Artifacts)
			}
			payloads := make(map[string][]byte)
			for _, item := range result.Artifacts {
				if item.Name != "screenreader-events" && item.Name != "screenreader-audio" {
					t.Fatalf("unexpected artifact after document closure: %#v", item)
				}
				artifact := request(t, server, http.MethodGet, "/v2/sessions/"+session.ID+"/artifacts/"+item.Name, nil, "test-secret")
				if artifact.Code != http.StatusOK {
					t.Fatalf("saved %s unavailable: status=%d body=%s", item.Name, artifact.Code, artifact.Body.String())
				}
				payload := artifact.Body.Bytes()
				digest := sha256.Sum256(payload)
				if int64(len(payload)) != item.Bytes || hex.EncodeToString(digest[:]) != item.SHA256 {
					t.Fatalf("saved %s does not match finish manifest", item.Name)
				}
				payloads[item.Name] = payload
			}
			var exported EventsResult
			if err := json.Unmarshal(payloads["screenreader-events"], &exported); err != nil {
				t.Fatal(err)
			}
			if exported.Cursor != result.Cursor || !bytes.Contains(payloads["screenreader-events"], []byte("Checkout")) || len(exported.AudioTimeline) == 0 {
				t.Fatalf("completed speech or its sample index was lost: %#v", exported)
			}
			wav, err := synth.DecodeWAV(payloads["screenreader-audio"])
			if err != nil {
				t.Fatal(err)
			}
			for _, indexed := range exported.AudioTimeline {
				if indexed.SampleCount <= 0 || indexed.StartSample < 0 || (indexed.StartSample+indexed.SampleCount)*2 > int64(len(wav.PCM)) {
					t.Fatalf("completed speech audio is absent from exported WAV: index=%#v PCM bytes=%d", indexed, len(wav.PCM))
				}
			}
			staleDocument := request(t, server, http.MethodGet, "/v2/sessions/"+session.ID+"/artifacts/screenreader-document", nil, "test-secret")
			if staleDocument.Code != http.StatusNotFound {
				t.Fatalf("stale document was exported: status=%d body=%s", staleDocument.Code, staleDocument.Body.String())
			}
			retried := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/finish", struct{}{}, "test-secret")
			if retried.Code != http.StatusOK || !bytes.Equal(retried.Body.Bytes(), finished.Body.Bytes()) {
				t.Fatalf("successful finish retry changed evidence: status=%d body=%s", retried.Code, retried.Body.String())
			}
			next := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "after-closed-page"}, "test-secret")
			if next.Code != http.StatusCreated {
				t.Fatalf("completed session retained exclusivity: status=%d body=%s", next.Code, next.Body.String())
			}
		})
	}
}
