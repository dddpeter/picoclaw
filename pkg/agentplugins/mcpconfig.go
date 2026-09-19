package agentplugins

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MCPServerEntry is one normalized server entry from mcp.json (spec §7.2.1).
// Normalization guarantees for returned entries:
//   - placeholders already expanded in args elements, env values and cwd
//   - a "./"-prefixed command is resolved to an absolute path (containment
//     re-checked; escapes drop the entry)
//   - bare-name command, url and headers are untouched (no expansion)
//   - cwd is always non-empty for stdio entries: explicit value or the
//     resolved plugin root (spec §7.2.1 MUST).
type MCPServerEntry struct {
	Name    string
	Type    string
	Command string
	URL     string
	Args    []string
	Env     map[string]string
	Headers map[string]string
	CWD     string
}

// mcpTopFields is the closed top-level whitelist of spec §7.2.1.
var mcpTopFields = map[string]bool{"$schema": true, "mcpServers": true}

// Closed per-variant field sets (§7.2.1): a field belonging to another
// variant, or an unknown field, makes the entry invalid.
var stdioEntryFields = map[string]bool{"type": true, "command": true, "args": true, "env": true, "cwd": true}
var remoteEntryFields = map[string]bool{"type": true, "url": true, "headers": true}

// headerNameRe accepts RFC 7230 field-name token characters.
var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+$`)

// LoadMCPConfig loads and validates mcp.json at the plugin root.
//
// Report/error split: FILE-level failures (missing → silent nil; bad JSON,
// $schema problems, top-level violations → error without Report entries —
// the caller converts the error to a warning) vs ENTRY-level problems
// (Warnf + drop the entry, keep loading).
//
// A whole-config error means MCP is disabled for that plugin; other component
// types keep loading (spec §7.2.2.2).
func LoadMCPConfig(root, manifestSchemaURL string, v Vars, r *Report) (map[string]MCPServerEntry, error) {
	path := filepath.Join(root, "mcp.json")
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // missing location is not an error (§6.2)
		}
		return nil, fmt.Errorf("stat mcp.json: %w", err)
	}
	if !st.Mode().IsRegular() {
		// Present but not the expected filesystem kind: component type
		// invalid, continue other types (§6.2).
		r.Warnf("mcp.json does not resolve to a regular file; MCP disabled for this plugin")
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mcp.json: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("mcp.json is not valid JSON: %w", err)
	}

	// Closed top-level schema (§7.2.1).
	for k := range raw {
		if !mcpTopFields[k] {
			return nil, fmt.Errorf("mcp.json: unknown top-level field %q (allowed: $schema, mcpServers)", k)
		}
	}

	// $schema: required, canonical 1.0.0 identifier, and version-matched
	// with plugin.json (§10.1). Any mismatch disables plugin MCP (§7.2.2.2).
	schemaRaw, ok := raw["$schema"]
	if !ok {
		return nil, fmt.Errorf("mcp.json: required field %q missing", "$schema")
	}
	var schemaURL string
	if err := json.Unmarshal(schemaRaw, &schemaURL); err != nil {
		return nil, fmt.Errorf("mcp.json: field %q must be a string", "$schema")
	}
	if schemaURL != MCPConfigSchemaURL {
		return nil, fmt.Errorf("mcp.json $schema %q is not a supported Agent Plugins version (supported: %s)", schemaURL, MCPConfigSchemaURL)
	}
	if specVersionOf(schemaURL) != specVersionOf(manifestSchemaURL) {
		return nil, fmt.Errorf("mcp.json $schema version %q does not match plugin.json version %q", specVersionOf(schemaURL), specVersionOf(manifestSchemaURL))
	}

	serversRaw, ok := raw["mcpServers"]
	if !ok {
		return nil, fmt.Errorf("mcp.json: required field %q missing", "mcpServers")
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		return nil, fmt.Errorf("mcp.json: %q must be an object", "mcpServers")
	}

	out := make(map[string]MCPServerEntry, len(servers))
	for name, entryRaw := range servers {
		entry, err := parseServerEntry(root, name, entryRaw, v)
		if err != nil {
			r.Warnf("mcp.json: server %q skipped: %v", name, err)
			continue
		}
		out[name] = entry
	}
	return out, nil
}

// specVersionOf extracts the version segment of a canonical schema URL
// (.../schemas/<version>/<name>.schema.json).
func specVersionOf(schemaURL string) string {
	i := strings.Index(schemaURL, "/schemas/")
	if i < 0 {
		return ""
	}
	rest := schemaURL[i+len("/schemas/"):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// parseServerEntry validates one server entry and normalizes it. A non-nil
// error means the entry is invalid and must be skipped (§7.2.2.3).
func parseServerEntry(root, name string, entryRaw json.RawMessage, v Vars) (MCPServerEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entryRaw, &fields); err != nil {
		return MCPServerEntry{}, fmt.Errorf("entry must be an object")
	}

	typeRaw, ok := fields["type"]
	if !ok {
		return MCPServerEntry{}, fmt.Errorf("required field %q missing", "type")
	}
	var typ string
	if err := json.Unmarshal(typeRaw, &typ); err != nil {
		return MCPServerEntry{}, fmt.Errorf("field %q must be a string", "type")
	}

	switch typ {
	case "stdio":
		return parseStdioEntry(root, name, fields, v)
	case "streamable-http", "sse":
		return parseRemoteEntry(name, typ, fields)
	default:
		return MCPServerEntry{}, fmt.Errorf("unsupported transport type %q", typ)
	}
}

func parseStdioEntry(root, name string, fields map[string]json.RawMessage, v Vars) (MCPServerEntry, error) {
	for k := range fields {
		if !stdioEntryFields[k] {
			return MCPServerEntry{}, fmt.Errorf("unknown field %q (stdio allows: type, command, args, env, cwd)", k)
		}
	}

	cmdRaw, ok := fields["command"]
	if !ok {
		return MCPServerEntry{}, fmt.Errorf("required field %q missing", "command")
	}
	var command string
	if err := json.Unmarshal(cmdRaw, &command); err != nil {
		return MCPServerEntry{}, fmt.Errorf("field %q must be a string", "command")
	}
	// Single executable token, not a shell command string (§7.2.1).
	if command == "" || strings.ContainsAny(command, " \t\r\n") {
		return MCPServerEntry{}, fmt.Errorf("command %q must be a single executable token", command)
	}

	entry := MCPServerEntry{Name: name, Type: "stdio"}
	if strings.HasPrefix(command, "./") {
		if !IsPluginRelative(command) {
			return MCPServerEntry{}, fmt.Errorf("command %q is not a valid plugin-relative path", command)
		}
		resolved := filepath.Join(root, command)
		if !Contains(root, resolved) {
			return MCPServerEntry{}, fmt.Errorf("command %q resolves outside the plugin root", command)
		}
		entry.Command = resolved
	} else {
		// Bare name: platform executable search, kept verbatim. Anything
		// path-like but not "./"-prefixed (../x, a/b, C:\x) is invalid.
		if strings.ContainsAny(command, `/\:`) {
			return MCPServerEntry{}, fmt.Errorf("command %q must be a bare name or a plugin-relative ./ path", command)
		}
		entry.Command = command
	}

	// args: expanded (§9.2).
	if raw, ok := fields["args"]; ok {
		var args []string
		if err := json.Unmarshal(raw, &args); err != nil {
			return MCPServerEntry{}, fmt.Errorf("field %q must be an array of strings", "args")
		}
		entry.Args = ExpandArgs(args, v)
	}

	// env: values expanded, reserved keys invalid (§9.1/§9.2).
	if raw, ok := fields["env"]; ok {
		var env map[string]string
		if err := json.Unmarshal(raw, &env); err != nil {
			return MCPServerEntry{}, fmt.Errorf("field %q must be an object of strings", "env")
		}
		expanded, err := ExpandEnvValues(env, v)
		if err != nil {
			return MCPServerEntry{}, err
		}
		entry.Env = expanded
	}

	// cwd: three allowed raw forms, expanded then contained (§7.2.1);
	// default = plugin root.
	entry.CWD = v.Root
	if raw, ok := fields["cwd"]; ok {
		var cwd string
		if err := json.Unmarshal(raw, &cwd); err != nil {
			return MCPServerEntry{}, fmt.Errorf("field %q must be a string", "cwd")
		}
		resolved, err := resolveCWD(root, cwd, v)
		if err != nil {
			return MCPServerEntry{}, err
		}
		entry.CWD = resolved
	}

	return entry, nil
}

// resolveCWD validates the three allowed cwd forms, expands placeholders and
// enforces post-resolution containment.
func resolveCWD(root, cwd string, v Vars) (string, error) {
	expanded := filepath.Clean(Expand(cwd, v))
	switch {
	case strings.HasPrefix(cwd, "./"):
		if !IsPluginRelative(cwd) {
			return "", fmt.Errorf("cwd %q is not a valid plugin-relative path", cwd)
		}
		resolved := filepath.Join(root, expanded)
		if !Contains(root, resolved) {
			return "", fmt.Errorf("cwd %q resolves outside the plugin root", cwd)
		}
		return resolved, nil
	case cwd == placeholderRoot || strings.HasPrefix(cwd, placeholderRoot+"/"):
		if !Contains(root, expanded) {
			return "", fmt.Errorf("cwd %q resolves outside the plugin root", cwd)
		}
		return expanded, nil
	case cwd == placeholderData || strings.HasPrefix(cwd, placeholderData+"/"):
		if !Contains(v.Data, expanded) {
			return "", fmt.Errorf("cwd %q resolves outside the plugin data directory", cwd)
		}
		return expanded, nil
	default:
		return "", fmt.Errorf("cwd %q must be ./-relative, ${PLUGIN_ROOT}[-rooted] or ${PLUGIN_DATA}[-rooted]", cwd)
	}
}

func parseRemoteEntry(name, typ string, fields map[string]json.RawMessage) (MCPServerEntry, error) {
	for k := range fields {
		if !remoteEntryFields[k] {
			return MCPServerEntry{}, fmt.Errorf("unknown field %q (%s allows: type, url, headers)", k, typ)
		}
	}

	urlRaw, ok := fields["url"]
	if !ok {
		return MCPServerEntry{}, fmt.Errorf("required field %q missing", "url")
	}
	var rawURL string
	if err := json.Unmarshal(urlRaw, &rawURL); err != nil {
		return MCPServerEntry{}, fmt.Errorf("field %q must be a string", "url")
	}
	if err := validateRemoteURL(rawURL); err != nil {
		return MCPServerEntry{}, err
	}

	entry := MCPServerEntry{Name: name, Type: typ, URL: rawURL}

	if raw, ok := fields["headers"]; ok {
		var headers map[string]string
		if err := json.Unmarshal(raw, &headers); err != nil {
			return MCPServerEntry{}, fmt.Errorf("field %q must be an object of strings", "headers")
		}
		normalized, err := normalizeHeaders(headers)
		if err != nil {
			return MCPServerEntry{}, err
		}
		entry.Headers = normalized
	}

	return entry, nil
}

// validateRemoteURL enforces §7.2.1: absolute http(s) URL, no userinfo, no
// fragment; non-loopback endpoints must use HTTPS while localhost and
// loopback IP literals may use plain HTTP.
func validateRemoteURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("url %q is not parseable", rawURL)
	}
	if !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("url %q must be an absolute http(s) URL", rawURL)
	}
	if u.User != nil {
		return fmt.Errorf("url %q must not contain user information", rawURL)
	}
	if u.Fragment != "" {
		return fmt.Errorf("url %q must not contain a fragment", rawURL)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", rawURL)
	}
	host := u.Hostname()
	loopback := strings.EqualFold(host, "localhost")
	if !loopback {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			loopback = true
		}
	}
	if !loopback && u.Scheme != "https" {
		return fmt.Errorf("url %q must use https (plain http only for loopback endpoints)", rawURL)
	}
	return nil
}

// normalizeHeaders validates header fields and detects case-insensitive
// duplicates (invalid per §7.2.1). Values are NOT expanded.
func normalizeHeaders(headers map[string]string) (map[string]string, error) {
	seen := make(map[string]string, len(headers))
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		if name == "" || !headerNameRe.MatchString(name) {
			return nil, fmt.Errorf("header name %q is not a valid HTTP field name", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("header %q value contains CR/LF", name)
		}
		lower := strings.ToLower(name)
		if prev, dup := seen[lower]; dup {
			return nil, fmt.Errorf("header %q duplicates %q (case-insensitive)", name, prev)
		}
		seen[lower] = name
		out[name] = value
	}
	return out, nil
}
