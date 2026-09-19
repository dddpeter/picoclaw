package agentplugins

import (
	"strings"
	"testing"
)

func TestSupportedManifestSchema(t *testing.T) {
	if !SupportedManifestSchema(ManifestSchemaURL) {
		t.Fatal("canonical URL must be supported")
	}
	if SupportedManifestSchema("https://agent-plugins.org/schemas/1.1.0/plugin.schema.json") {
		t.Fatal("1.1.0 draft must not be accepted")
	}
}

func TestReportSeverity(t *testing.T) {
	var r Report
	r.Warnf("ignored field %q", "bogus")
	r.Fatalf("manifest invalid: %s", "bad name")
	if r.OK() {
		t.Fatal("report with fatal must not be OK")
	}
	var clean Report
	if !clean.OK() {
		t.Fatal("empty report must be OK")
	}
	if !strings.Contains(r.Fatals[0], "bad name") {
		t.Fatal("fatalf must preserve args")
	}
}
