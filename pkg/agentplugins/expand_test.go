package agentplugins

import "testing"

func TestExpand(t *testing.T) {
	v := Vars{Root: `C:\p`, Data: `C:\d`}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plugin root", "${PLUGIN_ROOT}/config.json", `C:\p/config.json`},
		{"plugin data", "${PLUGIN_DATA}/x", `C:\d/x`},
		{"twice", "${PLUGIN_ROOT}${PLUGIN_ROOT}", `C:\pC:\p`},
		{"unknown var literal", "${UNKNOWN_VAR}", "${UNKNOWN_VAR}"},
		{"truncated placeholder", "${PLUGIN_ROOT", "${PLUGIN_ROOT"},
		{"empty", "", ""},
		{"no placeholder", "plain", "plain"},
		{"lowercase not expanded", "${plugin_root}", "${plugin_root}"},
	}
	for _, c := range cases {
		if got := Expand(c.in, v); got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExpandNoRescan(t *testing.T) {
	// Replacement output must not be scanned again (spec §9.2): Root's value
	// contains a literal ${PLUGIN_DATA}; a second pass would expand it.
	v := Vars{Root: "${PLUGIN_DATA}", Data: "D"}
	got := Expand("X ${PLUGIN_ROOT} Y", v)
	want := "X ${PLUGIN_DATA} Y"
	if got != want {
		t.Fatalf("Expand = %q, want %q (replacement output must not be rescanned)", got, want)
	}
}

func TestExpandArgs(t *testing.T) {
	v := Vars{Root: "R", Data: "D"}
	got := ExpandArgs([]string{"--config", "${PLUGIN_ROOT}/c.json", "${PLUGIN_DATA}"}, v)
	want := []string{"--config", "R/c.json", "D"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExpandArgs = %v, want %v", got, want)
		}
	}
}

func TestExpandEnvValues(t *testing.T) {
	v := Vars{Root: "R", Data: "D"}
	out, err := ExpandEnvValues(map[string]string{"DATA_DIR": "${PLUGIN_DATA}/database"}, v)
	if err != nil {
		t.Fatal(err)
	}
	if out["DATA_DIR"] != "D/database" {
		t.Fatalf("expanded env = %v", out)
	}

	// Reserved env keys make the server config invalid (spec §9.1).
	for _, key := range []string{"PLUGIN_ROOT", "plugin_root", "Plugin_Root", "PLUGIN_DATA", "plugin_data"} {
		if _, err := ExpandEnvValues(map[string]string{key: "x"}, v); err == nil {
			t.Errorf("reserved env key %q must be rejected", key)
		}
	}
}
