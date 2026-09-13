package cron

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Hot-reload tests pin the fork behavior that `picoclaw cron add/remove`
// (CLI writes jobs.json directly) takes effect on a running service without
// a gateway restart — see AGENTS.md 运维禁令 and fork-overview §同步注意事项.

func TestCronService_ReloadsStoreAfterExternalAdd(t *testing.T) {
	oldPoll := storePollInterval
	storePollInterval = 20 * time.Millisecond
	defer func() { storePollInterval = oldPoll }()

	storePath := filepath.Join(t.TempDir(), "jobs.json")
	cs := NewCronService(storePath, nil)
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	if len(cs.ListJobs(true)) != 0 {
		t.Fatalf("expected empty store at start")
	}

	// Simulate the CLI: a separate CronService instance writing the same file.
	at := time.Now().Add(2 * time.Hour).UnixMilli()
	cli := NewCronService(storePath, nil)
	if _, err := cli.AddJob("cli-job", CronSchedule{Kind: "at", AtMS: &at}, "hello", "feishu", "chat1"); err != nil {
		t.Fatalf("CLI AddJob failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if jobs := cs.ListJobs(true); len(jobs) == 1 && jobs[0].Name == "cli-job" {
			return // hot reload observed the external write
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("running service did not pick up external job addition within 3s")
}

func TestCronService_ReloadsStoreAfterExternalRemove(t *testing.T) {
	oldPoll := storePollInterval
	storePollInterval = 20 * time.Millisecond
	defer func() { storePollInterval = oldPoll }()

	storePath := filepath.Join(t.TempDir(), "jobs.json")
	cs := NewCronService(storePath, nil)
	at := time.Now().Add(2 * time.Hour).UnixMilli()
	job, err := cs.AddJob("doomed", CronSchedule{Kind: "at", AtMS: &at}, "hello", "cli", "direct")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	// Simulate the CLI removing the job behind our back.
	cli := NewCronService(storePath, nil)
	if !cli.RemoveJob(job.ID) {
		t.Fatalf("CLI RemoveJob failed")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(cs.ListJobs(true)) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("running service did not pick up external job removal within 3s")
}

func TestCronService_OwnWritesDoNotTriggerReload(t *testing.T) {
	oldPoll := storePollInterval
	storePollInterval = 20 * time.Millisecond
	defer func() { storePollInterval = oldPoll }()

	storePath := filepath.Join(t.TempDir(), "jobs.json")

	var mu sync.Mutex
	runs := 0
	cs := NewCronService(storePath, func(job *CronJob) (string, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		return "ok", nil
	})

	// Job due ~immediately, then hourly (no further runs during the test).
	at := time.Now().Add(30 * time.Millisecond).UnixMilli()
	if _, err := cs.AddJob("one-shot", CronSchedule{Kind: "at", AtMS: &at}, "hello", "cli", "direct"); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		runsNow := runs
		mu.Unlock()
		if runsNow > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Give the loop several poll cycles; our own save must not re-fire the job.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Fatalf("one-shot job ran %d times, want 1 (own saves must not re-trigger)", runs)
	}
}

func TestValidateSchedule(t *testing.T) {
	cs := NewCronService(filepath.Join(t.TempDir(), "jobs.json"), nil)

	tests := []struct {
		name     string
		schedule CronSchedule
		wantErr  bool
	}{
		{"valid cron", CronSchedule{Kind: "cron", Expr: "0 9 * * *"}, false},
		{"valid every", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, false},
		{"valid at", CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().Add(time.Hour).UnixMilli())}, false},
		// "45 16" was silently accepted before the fork validation and never
		// matched — this case is the regression anchor for AGENTS.md 运维禁令.
		{"truncated cron", CronSchedule{Kind: "cron", Expr: "45 16"}, true},
		{"garbage cron", CronSchedule{Kind: "cron", Expr: "not a cron"}, true},
		{"empty cron expr", CronSchedule{Kind: "cron", Expr: ""}, true},
		{"past at", CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().Add(-time.Hour).UnixMilli())}, true},
		{"zero at", CronSchedule{Kind: "at"}, true},
		{"zero every", CronSchedule{Kind: "every"}, true},
		{"empty kind", CronSchedule{}, true},
		{"unknown kind", CronSchedule{Kind: "whenever"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cs.validateSchedule(tt.schedule)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSchedule(%+v) error = %v, wantErr %v", tt.schedule, err, tt.wantErr)
			}
		})
	}
}

func TestAddJob_RejectsTruncatedCronExpression(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "jobs.json")
	cs := NewCronService(storePath, nil)

	if _, err := cs.AddJob("broken", CronSchedule{Kind: "cron", Expr: "45 16"}, "hello", "cli", "direct"); err == nil {
		t.Fatalf("AddJob accepted a truncated cron expression")
	}

	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("rejected job must not be persisted, stat err = %v", err)
	}
}
