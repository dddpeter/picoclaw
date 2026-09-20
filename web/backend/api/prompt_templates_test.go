package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPromptTemplatesTestHandler(t *testing.T) (*Handler, *http.ServeMux, string) {
	t.Helper()
	h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	h.promptTemplatesDataPath = filepath.Join(t.TempDir(), promptTemplatesFileName)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, h.promptTemplatesDataPath
}

func doPromptTemplatesJSON(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodePromptTemplates(t *testing.T, rec *httptest.ResponseRecorder) promptTemplatesResponse {
	t.Helper()
	var resp promptTemplatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, rec.Body.String())
	}
	return resp
}

func TestPromptTemplatesGetEmpty(t *testing.T) {
	_, mux, _ := newPromptTemplatesTestHandler(t)

	rec := doPromptTemplatesJSON(t, mux, http.MethodGet, "/api/prompt-templates", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", rec.Code, rec.Body.String())
	}
	resp := decodePromptTemplates(t, rec)
	if len(resp.Templates) != 0 {
		t.Fatalf("expected empty templates, got %+v", resp.Templates)
	}
	// Empty list must serialize as [] rather than null.
	if !strings.Contains(rec.Body.String(), `"templates":[]`) {
		t.Fatalf("expected \"templates\":[] in body, got %s", rec.Body.String())
	}
}

func TestPromptTemplatesPutRoundtrip(t *testing.T) {
	_, mux, path := newPromptTemplatesTestHandler(t)

	put := promptTemplatesResponse{Templates: []storedPromptTemplate{
		{ID: "custom-1", Icon: "📌", Title: "My template", Desc: "  desc  ", Prompt: "  do the thing  "},
		{ID: "tdd", Hidden: true},
	}}
	rec := doPromptTemplatesJSON(t, mux, http.MethodPut, "/api/prompt-templates", put)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	saved := decodePromptTemplates(t, rec)
	if len(saved.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %+v", saved.Templates)
	}
	if saved.Templates[0].Desc != "desc" || saved.Templates[0].Prompt != "do the thing" {
		t.Fatalf("expected trimmed fields, got %+v", saved.Templates[0])
	}
	if saved.Templates[1].Hidden != true || saved.Templates[1].Title != "" {
		t.Fatalf("hidden marker should keep only id, got %+v", saved.Templates[1])
	}

	// Persisted next to the app config as versioned JSON.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var file promptTemplatesFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse file: %v", err)
	}
	if file.Version != 1 || len(file.Templates) != 2 {
		t.Fatalf("unexpected file content: %+v", file)
	}

	rec2 := doPromptTemplatesJSON(t, mux, http.MethodGet, "/api/prompt-templates", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec2.Code)
	}
	got := decodePromptTemplates(t, rec2)
	if len(got.Templates) != 2 || got.Templates[0].ID != "custom-1" {
		t.Fatalf("roundtrip mismatch: %+v", got.Templates)
	}
}

func TestPromptTemplatesPutValidation(t *testing.T) {
	_, mux, path := newPromptTemplatesTestHandler(t)

	cases := []struct {
		name string
		body promptTemplatesResponse
		want string
	}{
		{"missing id", promptTemplatesResponse{Templates: []storedPromptTemplate{{Title: "x", Prompt: "y"}}}, "id is required"},
		{"blank id", promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: "  ", Title: "x", Prompt: "y"}}}, "id is required"},
		{"missing title", promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: "a", Prompt: "y"}}}, "title is required"},
		{"missing prompt", promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: "a", Title: "x"}}}, "prompt is required"},
		{
			"duplicate id",
			promptTemplatesResponse{Templates: []storedPromptTemplate{
				{ID: "a", Title: "x", Prompt: "y"},
				{ID: "a", Title: "x2", Prompt: "y2"},
			}},
			"duplicate id",
		},
		{
			"id too long",
			promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: strings.Repeat("a", promptTemplatesMaxIDLen+1), Title: "x", Prompt: "y"}}},
			"id too long",
		},
		{
			"prompt too long",
			promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: "a", Title: "x", Prompt: strings.Repeat("p", promptTemplatesMaxBody+1)}}},
			"prompt too long",
		},
		{
			"title too long",
			promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: "a", Title: strings.Repeat("t", promptTemplatesMaxTitle+1), Prompt: "y"}}},
			"title too long",
		},
		{
			"desc too long",
			promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: "a", Title: "x", Desc: strings.Repeat("d", promptTemplatesMaxDesc+1), Prompt: "y"}}},
			"desc too long",
		},
		{
			"icon too long",
			promptTemplatesResponse{Templates: []storedPromptTemplate{{ID: "a", Icon: strings.Repeat("i", promptTemplatesMaxIcon+1), Title: "x", Prompt: "y"}}},
			"icon too long",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doPromptTemplatesJSON(t, mux, http.MethodPut, "/api/prompt-templates", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("error %q does not contain %q", rec.Body.String(), tc.want)
			}
		})
	}

	// Oversized list.
	tooMany := make([]storedPromptTemplate, 0, promptTemplatesMaxCount+1)
	for i := 0; i <= promptTemplatesMaxCount; i++ {
		tooMany = append(tooMany, storedPromptTemplate{ID: fmt.Sprintf("t%d", i), Title: "x", Prompt: "y"})
	}
	rec := doPromptTemplatesJSON(t, mux, http.MethodPut, "/api/prompt-templates", promptTemplatesResponse{Templates: tooMany})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("too-many status = %d", rec.Code)
	}

	// No file may be written by rejected requests.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected PUT must not write the store: %v", err)
	}
}

func TestPromptTemplatesPutBadJSON(t *testing.T) {
	_, mux, _ := newPromptTemplatesTestHandler(t)

	req := httptest.NewRequest(http.MethodPut, "/api/prompt-templates", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestPromptTemplatesGetCorruptFile(t *testing.T) {
	_, mux, path := newPromptTemplatesTestHandler(t)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	rec := doPromptTemplatesJSON(t, mux, http.MethodGet, "/api/prompt-templates", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
