package agentplugins

import (
	"strings"
	"testing"
)

func TestValidatePluginName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"my-plugin", false}, {"acme.tools", false}, {"lint3r", false}, {"a", false},
		{"a.b", false}, {"a.b.c", false},
		{"My-Plugin", true}, {"-start", true}, {"end-", true}, {".dot", true},
		{"has--double", true}, {"too.many..dots", true}, {"", true},
		{"has_underscore", true}, {"has space", true},
		{strings.Repeat("a", 65), true}, {strings.Repeat("a", 64), false},
	}
	for _, c := range cases {
		err := ValidatePluginName(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidatePluginName(%q) err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
}
