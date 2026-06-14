package nativeapi

import "testing"

// TestIsActiveServerDownloadStatus pins the active-state predicate
// that the badge counter in the top nav and the "in flight" chip
// in Download_list both consult. The set is intentionally narrow —
// only queued / resolving / downloading are in flight; once a
// task reaches a terminal state we stop counting it so the badge
// can reset cleanly after a session. Adding a new status? Update
// isActiveServerDownloadStatus in online_download.go and the
// list here at the same time.
func TestIsActiveServerDownloadStatus(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		// Active states — the user is waiting for work to finish.
		{"queued", true},
		{"resolving", true},
		{"downloading", true},
		// Terminal states — never count toward the badge.
		{"completed", false},
		{"failed", false},
		{"paused", false},
		{"canceled", false},
		// Unknown values default to inactive so a typo on the
		// producer side silently degrades to "not active" rather
		// than inflating the badge.
		{"", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			if got := isActiveServerDownloadStatus(tc.status); got != tc.want {
				t.Fatalf("isActiveServerDownloadStatus(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// TestListOnlineServerDownloadTasksCountsResolvingAsActive is the
// regression test for the user-reported "badge doesn't show in
// flight task" bug. Before this fix, only `task.Status == "downloading"`
// contributed to ActiveCount, so a task that was in the "resolving"
// phase (the common case when a user clicks download on a slow
// source script) showed `activeCount=0` and the badge was hidden.
//
// We seed the in-memory task list directly so the test is fully
// synchronous — no goroutine, no race against a real download.
// ListOnlineServerDownloadTasks takes the read lock and returns
// a deterministic snapshot.
func TestListOnlineServerDownloadTasksCountsResolvingAsActive(t *testing.T) {
	// Wipe the task map so prior tests' leftover tasks don't
	// pollute the count.
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items = map[string]*onlineDownloadTask{}
	onlineDownloadTasks.Unlock()

	seed := []struct {
		id     string
		status string
	}{
		{"a-queued", "queued"},
		{"b-resolving", "resolving"},
		{"c-downloading", "downloading"},
		{"d-completed", "completed"},
		{"e-failed", "failed"},
		{"f-paused", "paused"},
		{"g-canceled", "canceled"},
	}

	onlineDownloadTasks.Lock()
	for _, s := range seed {
		onlineDownloadTasks.items[s.id] = &onlineDownloadTask{
			ID:     s.id,
			Mode:   "server",
			Status: s.status,
		}
	}
	onlineDownloadTasks.Unlock()

	got := listOnlineServerDownloadTasks()

	if got.TaskCount != len(seed) {
		t.Errorf("TaskCount = %d, want %d", got.TaskCount, len(seed))
	}
	// Expected active count: 3 (queued, resolving, downloading).
	const wantActive = 3
	if got.ActiveCount != wantActive {
		t.Errorf("ActiveCount = %d, want %d (queued+resolving+downloading)",
			got.ActiveCount, wantActive)
	}
}
