package agentplugins

import (
	"fmt"
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
