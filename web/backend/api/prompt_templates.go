package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// Prompt templates are user-defined reusable prompts shown as suggestion cards
// in the web chat (empty state and an in-conversation picker). The gateway
// persists only the user delta: custom templates, overrides of built-in
// templates (matched by id), and hidden markers for built-ins removed from the
// library. Built-in defaults live in the frontend i18n bundles; the backend
// never needs to know them.

const (
	promptTemplatesFileName = "prompt-templates.json"

	promptTemplatesMaxCount = 500
	promptTemplatesMaxIDLen = 100
	promptTemplatesMaxIcon  = 16
	promptTemplatesMaxTitle = 120
	promptTemplatesMaxDesc  = 200
	promptTemplatesMaxBody  = 16000
)

// storedPromptTemplate is one persisted entry: either a full template
// (custom, or an override of a built-in matched by id) or a hidden marker
// {id, hidden:true} removing a built-in from the library.
type storedPromptTemplate struct {
	ID     string `json:"id"`
	Icon   string `json:"icon,omitempty"`
	Title  string `json:"title,omitempty"`
	Desc   string `json:"desc,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

type promptTemplatesFile struct {
	Version   int                    `json:"version"`
	Templates []storedPromptTemplate `json:"templates"`
}

type promptTemplatesResponse struct {
	Templates []storedPromptTemplate `json:"templates"`
}

func (h *Handler) registerPromptTemplateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/prompt-templates", h.handleGetPromptTemplates)
	mux.HandleFunc("PUT /api/prompt-templates", h.handlePutPromptTemplates)
}

func (h *Handler) promptTemplatesPath() string {
	if h.promptTemplatesDataPath != "" {
		return h.promptTemplatesDataPath
	}
	dir := filepath.Dir(h.configPath)
	if dir == "" || dir == "." {
		dir = "."
	}
	return filepath.Join(dir, promptTemplatesFileName)
}

func (h *Handler) loadPromptTemplates() ([]storedPromptTemplate, error) {
	data, err := os.ReadFile(h.promptTemplatesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var file promptTemplatesFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", promptTemplatesFileName, err)
	}
	return file.Templates, nil
}

func (h *Handler) savePromptTemplates(templates []storedPromptTemplate) error {
	out := promptTemplatesFile{Version: 1, Templates: templates}
	if out.Templates == nil {
		out.Templates = []storedPromptTemplate{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fileutil.WriteFileAtomic(h.promptTemplatesPath(), data, 0o600)
}

// normalizePromptTemplates validates and normalizes the incoming list.
// Hidden markers keep only their id; full entries must carry a title and a
// prompt. Trims whitespace everywhere and rejects duplicates / oversized
// values so the file stays small and well-formed.
func normalizePromptTemplates(templates []storedPromptTemplate) ([]storedPromptTemplate, error) {
	if len(templates) > promptTemplatesMaxCount {
		return nil, fmt.Errorf("too many templates: %d (max %d)", len(templates), promptTemplatesMaxCount)
	}
	seen := make(map[string]struct{}, len(templates))
	out := make([]storedPromptTemplate, 0, len(templates))
	for i, tpl := range templates {
		tpl.ID = strings.TrimSpace(tpl.ID)
		if tpl.ID == "" {
			return nil, fmt.Errorf("templates[%d]: id is required", i)
		}
		if len([]rune(tpl.ID)) > promptTemplatesMaxIDLen {
			return nil, fmt.Errorf("templates[%d]: id too long (max %d chars)", i, promptTemplatesMaxIDLen)
		}
		if _, dup := seen[tpl.ID]; dup {
			return nil, fmt.Errorf("templates[%d]: duplicate id %q", i, tpl.ID)
		}
		seen[tpl.ID] = struct{}{}

		tpl.Icon = strings.TrimSpace(tpl.Icon)
		tpl.Title = strings.TrimSpace(tpl.Title)
		tpl.Desc = strings.TrimSpace(tpl.Desc)
		tpl.Prompt = strings.TrimSpace(tpl.Prompt)

		if tpl.Hidden {
			out = append(out, storedPromptTemplate{ID: tpl.ID, Hidden: true})
			continue
		}
		if tpl.Title == "" {
			return nil, fmt.Errorf("templates[%d] (%s): title is required", i, tpl.ID)
		}
		if tpl.Prompt == "" {
			return nil, fmt.Errorf("templates[%d] (%s): prompt is required", i, tpl.ID)
		}
		if len([]rune(tpl.Icon)) > promptTemplatesMaxIcon {
			return nil, fmt.Errorf("templates[%d] (%s): icon too long (max %d chars)", i, tpl.ID, promptTemplatesMaxIcon)
		}
		if len([]rune(tpl.Title)) > promptTemplatesMaxTitle {
			return nil, fmt.Errorf("templates[%d] (%s): title too long (max %d chars)", i, tpl.ID, promptTemplatesMaxTitle)
		}
		if len([]rune(tpl.Desc)) > promptTemplatesMaxDesc {
			return nil, fmt.Errorf("templates[%d] (%s): desc too long (max %d chars)", i, tpl.ID, promptTemplatesMaxDesc)
		}
		if len([]rune(tpl.Prompt)) > promptTemplatesMaxBody {
			return nil, fmt.Errorf("templates[%d] (%s): prompt too long (max %d chars)", i, tpl.ID, promptTemplatesMaxBody)
		}
		out = append(out, tpl)
	}
	return out, nil
}

func (h *Handler) handleGetPromptTemplates(w http.ResponseWriter, r *http.Request) {
	h.promptTemplatesMu.Lock()
	defer h.promptTemplatesMu.Unlock()

	templates, err := h.loadPromptTemplates()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load prompt templates: %v", err), http.StatusInternalServerError)
		return
	}
	if templates == nil {
		templates = []storedPromptTemplate{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(promptTemplatesResponse{Templates: templates})
}

// handlePutPromptTemplates replaces the entire list (last write wins): there is
// no per-entry merge, so concurrent writers can lose updates. Clients must
// serialize their writes — the web UI disables its write actions while a save
// is in flight (see PromptTemplatesProvider.persist).
func (h *Handler) handlePutPromptTemplates(w http.ResponseWriter, r *http.Request) {
	var payload promptTemplatesResponse
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	normalized, err := normalizePromptTemplates(payload.Templates)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.promptTemplatesMu.Lock()
	defer h.promptTemplatesMu.Unlock()

	if err := h.savePromptTemplates(normalized); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save prompt templates: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(promptTemplatesResponse{Templates: normalized})
}
