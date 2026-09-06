package evolution

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- 9.1: strengthened secret scanning ---

func TestScanDraftContentCatchesAlgorithmVariantPEM(t *testing.T) {
	for _, header := range []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN ENCRYPTED PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
	} {
		findings := scanDraftContent(SkillDraft{BodyOrPatch: header + "\nMIIE..."})
		joined := strings.Join(findings, "\n")
		if !strings.Contains(joined, "private key") && !strings.Contains(joined, "credential-pattern") {
			t.Errorf("PEM header %q not detected, findings: %v", header, findings)
		}
	}
}

func TestScanDraftContentCatchesCommonTokenPrefixes(t *testing.T) {
	// Fixtures are shaped to trip THIS scanner while staying below the
	// entropy/shape thresholds of real secret scanners: GitHub push
	// protection blocks commits that contain realistic credentials, even
	// as test data.
	cases := map[string]string{
		"openai proj":  "use sk-proj-unit-test-fixture in the config",
		"aws":          "aws key AKIAunittestfixture0 here",
		"github":       "token ghp_unittestfixturetoken0123456789abc",
		"github pat":   "github_pat_unit_test_fixture_prefix_and_tail",
		"slack":        "xoxb-unit-test-fixture-token",
		"key literal":  `api_key = "placeholder-not-real-0123456789"`,
		"stripe style": "pk and sk_live_unittestfixture",
	}
	for name, body := range cases {
		findings := scanDraftContent(SkillDraft{BodyOrPatch: body})
		if len(findings) == 0 {
			t.Errorf("%s: not detected, body: %q", name, body)
		}
	}
}

func TestScanDraftContentNoFalsePositivesOnProse(t *testing.T) {
	benign := []string{
		"Use the api_key setting to configure your provider.",
		"The token count exceeded the budget this turn.",
		"No secrets here, just documentation about secret handling.",
		"password policies should require rotation.",
	}
	for _, body := range benign {
		findings := scanDraftContent(SkillDraft{BodyOrPatch: body})
		if len(findings) != 0 {
			t.Errorf("benign body flagged: %q -> %v", body, findings)
		}
	}
}

// --- 3.1: self-cleaning lock registry ---

func TestLockStoreFileReclaimsEntries(t *testing.T) {
	path := t.TempDir() + "/state/test-reclaim.json"
	unlock := lockStoreFile(path)
	if len(storeFileLocks.locks) != 1 {
		t.Fatalf("expected 1 registered lock, got %d", len(storeFileLocks.locks))
	}
	unlock()
	if len(storeFileLocks.locks) != 0 {
		t.Fatalf("entry not reclaimed after unlock, %d remain", len(storeFileLocks.locks))
	}
}

func TestLockStoreFileConcurrentSamePathSingleEntry(t *testing.T) {
	path := t.TempDir() + "/state/test-concurrent.json"
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		unlock := lockStoreFile(path)
		if len(storeFileLocks.locks) != 1 {
			t.Fatalf("expected exactly 1 lock entry under concurrency, got %d", len(storeFileLocks.locks))
		}
		go func() {
			time.Sleep(5 * time.Millisecond)
			unlock()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if len(storeFileLocks.locks) != 0 {
		t.Fatalf("entries not reclaimed, %d remain", len(storeFileLocks.locks))
	}
}

// --- 7.1: configurable lifecycle thresholds ---

func TestNextLifecycleStateWithThresholds(t *testing.T) {
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	profile := SkillProfile{
		Status:         SkillStatusActive,
		RetentionScore: 0.1,
		LastUsedAt:     now.Add(-30 * 24 * time.Hour),
	}

	// Custom threshold: 25 days → transitions at 30 idle days.
	if got := NextLifecycleStateWithThresholds(profile, now, 25, 180, 365); got != SkillStatusCold {
		t.Fatalf("custom cold threshold: got %q, want cold", got)
	}
	// Default threshold: 30 idle days < 90 → stays active.
	if got := NextLifecycleState(profile, now); got != SkillStatusActive {
		t.Fatalf("default threshold: got %q, want active", got)
	}
	// Zero disables the transition.
	if got := NextLifecycleStateWithThresholds(profile, now, 0, 180, 365); got != SkillStatusActive {
		t.Fatalf("zero threshold should disable transition, got %q", got)
	}
}

// --- 6.1: bounded Close ---

type slowColdPathRuntime struct {
	release chan struct{}
}

func (s *slowColdPathRuntime) RunColdPathOnce(ctx context.Context, _ string) error {
	<-s.release
	return nil
}

func TestColdPathRunnerCloseBounded(t *testing.T) {
	oldTimeout := coldPathCloseTimeout
	coldPathCloseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { coldPathCloseTimeout = oldTimeout })

	runner := NewColdPathRunner(&slowColdPathRuntime{release: make(chan struct{})})
	runner.async = func(fn func()) { go fn() }
	if !runner.Trigger("ws-1") {
		t.Fatal("expected trigger to schedule a run")
	}

	start := time.Now()
	closeDone := make(chan error, 1)
	go func() { closeDone <- runner.Close() }()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within the bounded timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close blocked for %v despite the bound", elapsed)
	}
}
