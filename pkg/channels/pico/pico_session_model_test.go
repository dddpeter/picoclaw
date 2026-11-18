package pico

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubSessionModelController is a fake agent-side controller.
type stubSessionModelController struct {
	models map[string]string
	err    error
}

func (s *stubSessionModelController) SetModel(sessionID, modelName string) error {
	if s.err != nil {
		return s.err
	}
	if s.models == nil {
		s.models = map[string]string{}
	}
	if modelName == "" {
		delete(s.models, sessionID)
		return nil
	}
	s.models[sessionID] = modelName
	return nil
}

func (s *stubSessionModelController) GetModel(sessionID string) string {
	return s.models[sessionID]
}

func (s *stubSessionModelController) ModelWindows(sessionID string) (int, int) {
	if s.models[sessionID] == "" {
		return 0, 0
	}
	return 32_000, 24_000
}

func newSessionModelTestChannel(t *testing.T) (*PicoChannel, *stubSessionModelController) {
	t.Helper()
	ch := newTestPicoChannel(t)
	ch.SetRunning(true)
	controller := &stubSessionModelController{}
	ch.SetSessionModelController(controller)
	return ch, controller
}

func sessionModelRequest(t *testing.T, ch *PicoChannel, method, session, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/pico/session-model?session="+session, reader)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleSessionModel(rec, req)
	return rec
}

// The endpoint binds a model to ONE session and reports it back.
func TestHandleSessionModel_SetAndGet(t *testing.T) {
	ch, controller := newSessionModelTestChannel(t)

	rec := sessionModelRequest(t, ch, http.MethodPost, "sess-1", `{"model":"small"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST code = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["model"] != "small" {
		t.Fatalf("model = %v, want small", body["model"])
	}
	if body["context_window"] != float64(32_000) {
		t.Fatalf("context_window = %v, want 32000", body["context_window"])
	}
	if controller.GetModel("sess-1") != "small" {
		t.Fatalf("controller did not store the model")
	}

	rec = sessionModelRequest(t, ch, http.MethodGet, "sess-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["model"] != "small" {
		t.Fatalf("GET model = %v, want small", body["model"])
	}
}

// The binding is per session: another session keeps the configured model.
func TestHandleSessionModel_IsolatedPerSession(t *testing.T) {
	ch, _ := newSessionModelTestChannel(t)
	sessionModelRequest(t, ch, http.MethodPost, "sess-1", `{"model":"small"}`)

	rec := sessionModelRequest(t, ch, http.MethodGet, "sess-2", "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["model"] != "" {
		t.Fatalf("session 2 model = %v, want empty", body["model"])
	}
}

// An empty model clears the binding (the session returns to the config
// default).
func TestHandleSessionModel_Clear(t *testing.T) {
	ch, _ := newSessionModelTestChannel(t)
	sessionModelRequest(t, ch, http.MethodPost, "sess-1", `{"model":"small"}`)
	sessionModelRequest(t, ch, http.MethodPost, "sess-1", `{"model":""}`)

	rec := sessionModelRequest(t, ch, http.MethodGet, "sess-1", "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["model"] != "" {
		t.Fatalf("model = %v, want empty after clear", body["model"])
	}
}

// A rejected change surfaces the error instead of pretending it worked.
func TestHandleSessionModel_ReportsError(t *testing.T) {
	ch, controller := newSessionModelTestChannel(t)
	controller.err = errTestModelRejected

	rec := sessionModelRequest(t, ch, http.MethodPost, "sess-1", `{"model":"nope"}`)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] == nil || body["error"] == "" {
		t.Fatalf("expected an error in the response, got %v", body)
	}
}

// Requests without a session id are rejected.
func TestHandleSessionModel_RequiresSession(t *testing.T) {
	ch, _ := newSessionModelTestChannel(t)
	rec := sessionModelRequest(t, ch, http.MethodGet, "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

// Unauthenticated requests are rejected.
func TestHandleSessionModel_RequiresAuth(t *testing.T) {
	ch, _ := newSessionModelTestChannel(t)
	req := httptest.NewRequest(http.MethodGet, "/pico/session-model?session=sess-1", nil)
	rec := httptest.NewRecorder()
	ch.handleSessionModel(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

var errTestModelRejected = &testModelError{}

type testModelError struct{}

func (e *testModelError) Error() string { return "model not configured" }
