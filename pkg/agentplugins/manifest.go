package agentplugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// pluginNameRe encodes spec §5.5: lowercase alphanumeric start/end, interior
// may contain hyphens and periods.
var pluginNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$|^[a-z0-9]$`)

// ValidatePluginName enforces the plugin name constraints of spec §5.5:
// 1-64 characters, charset [a-z0-9.-], alphanumeric first/last characters,
// no consecutive "--" or "..".
func ValidatePluginName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return fmt.Errorf("plugin name must be 1-64 chars, got %d", len(name))
	}
	if strings.Contains(name, "--") || strings.Contains(name, "..") {
		return fmt.Errorf("plugin name %q must not contain consecutive -- or ..", name)
	}
	if !pluginNameRe.MatchString(name) {
		return fmt.Errorf("plugin name %q must match [a-z0-9.-], start/end alphanumeric", name)
	}
	return nil
}

// Author is the manifest author object. Closed schema: only
// name/email/url string fields are permitted (spec §5.4).
type Author struct {
	Name  string
	Email string
	URL   string
}

// Manifest is the parsed and validated root plugin.json (spec §5).
type Manifest struct {
	SchemaURL   string
	Name        string
	Version     string
	Description string
	License     string
	Homepage    string
	Repository  string
	Keywords    []string
	Author      *Author

	// ignoredTopFields records top-level fields that were reported and
	// ignored (unknown fields, non-object extensions).
	ignoredTopFields []string
}

// manifestTopFields is the closed top-level whitelist of spec §5.2.
var manifestTopFields = map[string]bool{
	"$schema": true, "name": true, "version": true, "description": true,
	"author": true, "homepage": true, "repository": true, "license": true,
	"keywords": true, "extensions": true,
}

// LoadManifest loads and validates plugin.json at the plugin root. A non-nil
// error means the whole plugin is rejected (fatal per spec §5.2/§5.3);
// non-fatal issues are recorded in the Report.
func LoadManifest(root string, r *Report) (*Manifest, error) {
	path := filepath.Join(root, "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plugin.json: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("plugin.json is not a JSON object: %w", err)
	}

	// Closed schema: unknown top-level fields are reported and ignored
	// (spec §5.2), the rest of the manifest still loads.
	m := &Manifest{}
	for k := range raw {
		if !manifestTopFields[k] {
			r.Warnf("manifest: unknown top-level field %q ignored", k)
			m.ignoredTopFields = append(m.ignoredTopFields, k)
		}
	}

	// $schema: required, must be the canonical 1.0.0 identifier (§5.2).
	schemaURL, err := requireString(raw, "$schema")
	if err != nil {
		return nil, err
	}
	if !SupportedManifestSchema(schemaURL) {
		return nil, fmt.Errorf("plugin.json $schema %q is not a supported Agent Plugins version (supported: %s)", schemaURL, ManifestSchemaURL)
	}

	// name: required and must satisfy §5.5.
	name, err := requireString(raw, "name")
	if err != nil {
		return nil, err
	}
	if err := ValidatePluginName(name); err != nil {
		return nil, err
	}

	m.SchemaURL = schemaURL
	m.Name = name

	// Metadata fields: string-typed only (§5.4).
	for field, dst := range map[string]*string{
		"version":     &m.Version,
		"description": &m.Description,
		"license":     &m.License,
		"homepage":    &m.Homepage,
		"repository":  &m.Repository,
	} {
		if _, ok := raw[field]; ok {
			s, err := requireString(raw, field)
			if err != nil {
				return nil, err
			}
			*dst = s
		}
	}

	// keywords: array of strings (§5.4).
	if v, ok := raw["keywords"]; ok {
		var kw []string
		if err := json.Unmarshal(v, &kw); err != nil {
			return nil, fmt.Errorf("plugin.json %q must be an array of strings", "keywords")
		}
		m.Keywords = kw
	}

	// author: closed object with only name/email/url string fields (§5.4).
	if v, ok := raw["author"]; ok {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(v, &obj); err != nil {
			return nil, fmt.Errorf("plugin.json author must be an object")
		}
		a := &Author{}
		for k, val := range obj {
			switch k {
			case "name":
				if a.Name, err = decodeStringField("author."+k, val); err != nil {
					return nil, err
				}
			case "email":
				if a.Email, err = decodeStringField("author."+k, val); err != nil {
					return nil, err
				}
			case "url":
				if a.URL, err = decodeStringField("author."+k, val); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("plugin.json author has unknown field %q (allowed: name, email, url)", k)
			}
		}
		m.Author = a
	}

	// extensions: non-object is reported and ignored (§8.1); object values
	// for namespaces we do not implement are ignored without validation.
	if v, ok := raw["extensions"]; ok {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(v, &obj); err != nil {
			r.Warnf("manifest: extensions is not an object, ignored")
			m.ignoredTopFields = append(m.ignoredTopFields, "extensions")
		}
	}

	return m, nil
}

func requireString(raw map[string]json.RawMessage, field string) (string, error) {
	v, ok := raw[field]
	if !ok {
		return "", fmt.Errorf("plugin.json: required field %q missing", field)
	}
	return decodeStringField(field, v)
}

func decodeStringField(field string, v json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("plugin.json field %q must be a string", field)
	}
	return s, nil
}
