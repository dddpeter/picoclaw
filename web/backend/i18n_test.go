package main

import "testing"

// TestSetLanguageNormalizesForms pins the accepted language forms: unix
// locale strings (zh_CN.UTF-8), BCP-style (zh-TW), and the legacy "chinese"
// all map to the Chinese table; anything else falls back to English.
func TestSetLanguageNormalizesForms(t *testing.T) {
	cases := map[string]Language{
		"zh":               LanguageChinese,
		"zh_CN":            LanguageChinese,
		"zh_CN.UTF-8":      LanguageChinese,
		"zh-TW":            LanguageChinese,
		"zh-hk":            LanguageChinese,
		"chinese":          LanguageChinese,
		" ZH ":             LanguageChinese,
		"en":               LanguageEnglish,
		"en_US.UTF-8":      LanguageEnglish,
		"fr":               LanguageEnglish,
		"":                 LanguageEnglish,
	}
	for in, want := range cases {
		SetLanguage(in)
		if got := GetLanguage(); got != want {
			t.Errorf("SetLanguage(%q) -> %q, want %q", in, got, want)
		}
	}
	// Restore the package default for other tests in this binary.
	SetLanguage("en")
}

// TestDetectLanguagePrecedence pins the detection order: LANGUAGE beats
// LANG beats the OS UI language probe.
func TestDetectLanguagePrecedence(t *testing.T) {
	env := func(kvs map[string]string) func(string) string {
		return func(key string) string { return kvs[key] }
	}
	if got := detectLanguage(env(map[string]string{"LANGUAGE": "zh_CN", "LANG": "en_US"}), func() string { return "en" }); got != "zh_CN" {
		t.Errorf("LANGUAGE must win, got %q", got)
	}
	if got := detectLanguage(env(map[string]string{"LANG": "en_US.UTF-8"}), func() string { return "zh" }); got != "en_US.UTF-8" {
		t.Errorf("LANG must beat the OS probe, got %q", got)
	}
	if got := detectLanguage(env(nil), func() string { return "zh" }); got != "zh" {
		t.Errorf("OS probe must be the fallback, got %q", got)
	}
	if got := detectLanguage(env(map[string]string{"LANGUAGE": "  "}), func() string { return "" }); got != "" {
		t.Errorf("whitespace env must be ignored, got %q", got)
	}
}

// TestOsUILanguageProbeContract: the Windows probe never panics and only
// ever returns "" or "zh" (its contract). On a Chinese Windows box the
// log line should show zh.
func TestOsUILanguageProbeContract(t *testing.T) {
	lang := osUILanguage()
	t.Logf("osUILanguage() = %q", lang)
	if lang != "" && lang != "zh" {
		t.Errorf("osUILanguage must return \"\" or \"zh\", got %q", lang)
	}
}

// TestTrayKeysHaveBothLanguages guards the tray catalog: every key must
// exist in both language tables so T() never renders a raw key.
func TestTrayKeysHaveBothLanguages(t *testing.T) {
	en, zh := translations[LanguageEnglish], translations[LanguageChinese]
	if len(en) == 0 || len(zh) == 0 {
		t.Fatalf("translation tables must not be empty (en=%d zh=%d)", len(en), len(zh))
	}
	for key := range en {
		if _, ok := zh[key]; !ok {
			t.Errorf("key %q missing from Chinese table", key)
		}
	}
	for key := range zh {
		if _, ok := en[key]; !ok {
			t.Errorf("key %q missing from English table", key)
		}
	}
}
