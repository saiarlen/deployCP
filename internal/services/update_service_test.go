package services

import (
	"testing"
	"time"
)

func TestUpdateProgressTracksDurableUpdaterStages(t *testing.T) {
	tests := []struct {
		state string
		log   string
		phase string
		want  int
	}{
		{state: "queued", phase: "Queued", want: 5},
		{state: "running", log: "Downloading deploycp-v1.2.0-linux-amd64.tar.gz", phase: "Downloading release package", want: 16},
		{state: "running", log: "Verifying deploycp-v1.2.0-linux-amd64.tar.gz", phase: "Verifying package integrity", want: 32},
		{state: "running", log: "restricted SSH bridge refreshed", phase: "Refreshing restricted SSH access", want: 60},
		{state: "running", log: "[OK] platform_mode: linux production mode", phase: "Verifying host health", want: 90},
		{state: "success", phase: "Complete", want: 100},
		{state: "failed", log: "Downloading deploycp-v1.2.0-linux-amd64.tar.gz", phase: "Stopped during downloading release package", want: 16},
	}
	for _, test := range tests {
		t.Run(test.state+"/"+test.phase, func(t *testing.T) {
			phase, got := updateProgress(test.state, test.log)
			if phase != test.phase || got != test.want {
				t.Fatalf("updateProgress(%q, %q) = (%q, %d), want (%q, %d)", test.state, test.log, phase, got, test.phase, test.want)
			}
		})
	}
}

func TestUpdateIsActive(t *testing.T) {
	for _, state := range []string{"queued", "running", " QUEUED "} {
		if !updateIsActive(state) {
			t.Fatalf("updateIsActive(%q) = false, want true", state)
		}
	}
	for _, state := range []string{"", "idle", "success", "failed"} {
		if updateIsActive(state) {
			t.Fatalf("updateIsActive(%q) = true, want false", state)
		}
	}
}

func TestIsStaleQueuedUpdate(t *testing.T) {
	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	if !isStaleQueuedUpdate(updateJobState{State: "queued", StartedAt: now.Add(-3 * time.Minute).Format(time.RFC3339)}, now) {
		t.Fatal("expired queued update should be stale")
	}
	if isStaleQueuedUpdate(updateJobState{State: "queued", StartedAt: now.Add(-time.Minute).Format(time.RFC3339)}, now) {
		t.Fatal("recent queued update must remain active")
	}
	if isStaleQueuedUpdate(updateJobState{State: "running", StartedAt: now.Add(-time.Hour).Format(time.RFC3339)}, now) {
		t.Fatal("running update must not be treated as a stale queue")
	}
}
