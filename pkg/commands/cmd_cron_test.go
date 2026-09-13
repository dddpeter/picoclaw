package commands

import (
	"context"

	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/cron"
)

// --- /learn prompt builder ---

func TestBuildLearnPrompt_ContainsHardRequirements(t *testing.T) {
	prompt := BuildLearnPrompt("how we deployed the gateway")
	for _, want := range []string{
		"CHECK FIRST",
		"SKILL.md",
		"disable-model-invocation",
		"## When to Use",
		"## Pitfalls",
		"## Verification",
		"how we deployed the gateway",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("learn prompt missing %q", want)
		}
	}
}

func TestBuildLearnPrompt_TruncatesHugeSources(t *testing.T) {
	base := len(BuildLearnPrompt("x")) // template + 1-char source
	source := strings.Repeat("x", learnPromptBudget+5000)
	prompt := BuildLearnPrompt(source)
	if len(prompt) > base+learnPromptBudget+200 {
		t.Fatalf("source not truncated: prompt len %d, base %d", len(prompt), base)
	}
	if !strings.Contains(prompt, "(source truncated)") {
		t.Fatal("truncation notice missing")
	}
}

func TestParseLearnSource(t *testing.T) {
	if got := ParseLearnSource("/learn the deploy dance"); got != "the deploy dance" {
		t.Fatalf("ParseLearnSource = %q", got)
	}
	if got := ParseLearnSource("/learn"); got != "" {
		t.Fatalf("bare /learn must yield empty source, got %q", got)
	}
	if got := ParseLearnSource("/learn   spaced  out   "); got != "spaced  out" {
		t.Fatalf("ParseLearnSource trimming wrong: %q", got)
	}
}

// --- /cron command ---

type cronRuntimeFixture struct {
	rt           *Runtime
	replies      []string
	addedChannel string
	addedChatID  string
	addedID      string
	dismissedID  string
}

func newCronRuntimeFixture() *cronRuntimeFixture {
	f := &cronRuntimeFixture{}
	jobs := []cron.CronJob{
		{Name: "every-hour", ID: "job1", Enabled: true, Schedule: cron.CronSchedule{Kind: "every", EveryMS: int64PtrCron(3600000)}},
		{Name: "disabled", ID: "job2", Enabled: false},
	}
	f.rt = &Runtime{
		CronJobs: func() []cron.CronJob { return jobs },
		CronSuggestions: func() []cron.Suggestion {
			every := int64PtrCron(86400000)
			return []cron.Suggestion{
				{ID: "sug_1", Name: "nightly", Message: "m", Source: "evolution",
					Schedule:  cron.CronSchedule{Kind: "every", EveryMS: every},
					Rationale: "repeats daily"},
			}
		},
		AcceptCronSuggestion: func(channel, chatID, id string) (string, error) {
			f.addedChannel, f.addedChatID, f.addedID = channel, chatID, id
			return "newjob", nil
		},
		DismissCronSuggestion: func(id string) error {
			f.dismissedID = id
			return nil
		},
	}
	return f
}

func execCron(t *testing.T, f *cronRuntimeFixture, text string) {
	t.Helper()
	req := Request{Channel: "feishu", ChatID: "chat1", Text: text, Reply: func(s string) error {
		f.replies = append(f.replies, s)
		return nil
	}}
	// Route through the executor so sub-command dispatch stays exercised.
	e := NewExecutor(NewRegistry(BuiltinDefinitions()), f.rt)
	res := e.Execute(context.Background(), req)
	if res.Err != nil {
		t.Fatalf("execute %q: %v", text, res.Err)
	}
}

func TestCronCommand_List(t *testing.T) {
	f := newCronRuntimeFixture()
	execCron(t, f, "/cron list")
	out := strings.Join(f.replies, "\n")
	if !strings.Contains(out, "every-hour") || strings.Contains(out, "disabled") {
		t.Fatalf("list output wrong: %q", out)
	}
}

func TestCronCommand_Suggest(t *testing.T) {
	f := newCronRuntimeFixture()
	execCron(t, f, "/cron suggest")
	out := strings.Join(f.replies, "\n")
	if !strings.Contains(out, "sug_1") || !strings.Contains(out, "repeats daily") {
		t.Fatalf("suggest output wrong: %q", out)
	}
}

func TestCronCommand_AcceptForwardsChannel(t *testing.T) {
	f := newCronRuntimeFixture()
	execCron(t, f, "/cron accept sug_1")
	if f.addedID != "sug_1" || f.addedChannel != "feishu" || f.addedChatID != "chat1" {
		t.Fatalf("accept did not forward context: %+v", f)
	}
	if !strings.Contains(f.replies[0], "newjob") {
		t.Fatalf("accept reply missing job id: %q", f.replies[0])
	}
}

func TestCronCommand_Dismiss(t *testing.T) {
	f := newCronRuntimeFixture()
	execCron(t, f, "/cron dismiss sug_1")
	if f.dismissedID != "sug_1" {
		t.Fatalf("dismiss did not reach the runtime callback")
	}
}

func TestCronCommand_Blueprint(t *testing.T) {
	f := newCronRuntimeFixture()
	execCron(t, f, "/cron blueprint")
	if !strings.Contains(strings.Join(f.replies, "\n"), "daily_report") {
		t.Fatalf("blueprint listing missing catalog: %q", f.replies)
	}
}

func TestCronCommand_MissingArgsShowUsage(t *testing.T) {
	f := newCronRuntimeFixture()
	execCron(t, f, "/cron accept")
	if !strings.Contains(strings.Join(f.replies, "\n"), "Usage:") {
		t.Fatalf("accept without id must show usage: %q", f.replies)
	}
}

func TestCronCommand_NilRuntimeRepliesUnavailable(t *testing.T) {
	f := &cronRuntimeFixture{rt: &Runtime{}}
	execCron(t, f, "/cron list")
	if !strings.Contains(strings.Join(f.replies, "\n"), unavailableMsg) {
		t.Fatalf("nil callback must degrade to unavailable message: %q", f.replies)
	}
}

func int64PtrCron(v int64) *int64 {
	return &v
}
