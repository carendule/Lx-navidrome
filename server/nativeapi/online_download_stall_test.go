package nativeapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// helper: clean up any test tasks we inject
func cleanupTestTasks(ids ...string) {
	onlineDownloadTasks.Lock()
	for _, id := range ids {
		delete(onlineDownloadTasks.items, id)
	}
	onlineDownloadTasks.Unlock()
}

func injectTestTask(id string, status string, received int64, createdAt time.Time, tempPath string) {
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:        id,
		Mode:      "server",
		Title:     "test",
		Artist:    "tester",
		Status:    status,
		Received:  received,
		Total:     1000,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
		TempPath:  tempPath,
		FilePath:  strings.TrimSuffix(tempPath, ".part"),
	}
	onlineDownloadTasks.Unlock()
}

// TestStallDetectorTimesOutIdleDownload: 一个"刚启动但从未读到一个 byte"的
// downloading 任务，超过 stallTimeout 后被 stall detector 判为 failed。
func TestStallDetectorTimesOutIdleDownload(t *testing.T) {
	dir := t.TempDir()
	tempPath := filepath.Join(dir, "song.mp3.part")
	if err := os.WriteFile(tempPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	id := "stall-test"
	defer cleanupTestTasks(id)
	// createdAt = 2 minutes ago (well past the 1-minute stall timeout)
	injectTestTask(id, "downloading", 0, time.Now().Add(-2*time.Minute), tempPath)

	detectAndFailStalledTasks()

	task, ok := getOnlineDownloadTaskPointer(id)
	if !ok {
		t.Fatal("task disappeared unexpectedly")
	}
	if task.Status != "failed" {
		t.Errorf("expected status=failed, got %q", task.Status)
	}
	if !strings.Contains(task.Error, "stalled") {
		t.Errorf("expected Error to mention 'stalled', got %q", task.Error)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Errorf("expected .part file to be deleted, stat err = %v", err)
	}
}

// TestStallDetectorIgnoresTaskWithProgress: 一旦 Received > 0（已经在下载），
// stall detector 不应该把它判 failed。
func TestStallDetectorIgnoresTaskWithProgress(t *testing.T) {
	id := "progress-test"
	defer cleanupTestTasks(id)
	// createdAt = 2 minutes ago, but received=1024 表示已下载 1KB
	injectTestTask(id, "downloading", 1024, time.Now().Add(-2*time.Minute), "")

	detectAndFailStalledTasks()

	task, ok := getOnlineDownloadTaskPointer(id)
	if !ok {
		t.Fatal("task disappeared unexpectedly")
	}
	if task.Status != "downloading" {
		t.Errorf("expected status=downloading (untouched), got %q", task.Status)
	}
}

// TestStallDetectorIgnoresRecentTask: 新建不到 1 分钟的任务不应被 stall。
func TestStallDetectorIgnoresRecentTask(t *testing.T) {
	id := "recent-test"
	defer cleanupTestTasks(id)
	injectTestTask(id, "downloading", 0, time.Now(), "")

	detectAndFailStalledTasks()

	task, ok := getOnlineDownloadTaskPointer(id)
	if !ok {
		t.Fatal("task disappeared unexpectedly")
	}
	if task.Status != "downloading" {
		t.Errorf("expected status=downloading (untouched), got %q", task.Status)
	}
}

// TestClearFailedEndpoint: 端到端测试：HTTP POST clear-failed 删 failed/paused/
// canceled 任务，保留 downloading/completed；.part 文件也一并清掉。
func TestClearFailedEndpoint(t *testing.T) {
	dir := t.TempDir()

	ids := []string{"f-failed", "f-paused", "f-canceled", "f-completed", "f-downloading"}
	defer cleanupTestTasks(ids...)

	// failed task 有 .part 文件
	failedTemp := filepath.Join(dir, "failed.mp3.part")
	if err := os.WriteFile(failedTemp, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	injectTestTask(ids[0], "failed", 0, time.Now().Add(-2*time.Minute), failedTemp)
	injectTestTask(ids[1], "paused", 0, time.Now(), "")
	injectTestTask(ids[2], "canceled", 0, time.Now(), "")
	injectTestTask(ids[3], "completed", 1024, time.Now(), "")
	injectTestTask(ids[4], "downloading", 512, time.Now(), "")

	api := &Router{}
	req := httptest.NewRequest(http.MethodPost, "/api/online/download/tasks/clear-failed", nil)
	rr := httptest.NewRecorder()
	api.onlineServerDownloadClearFailed(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	// failed/paused/canceled 应被删
	for _, id := range ids[:3] {
		if _, ok := getOnlineDownloadTaskPointer(id); ok {
			t.Errorf("expected %s to be removed", id)
		}
	}
	// completed/downloading 应保留
	for _, id := range ids[3:] {
		if _, ok := getOnlineDownloadTaskPointer(id); !ok {
			t.Errorf("expected %s to be preserved", id)
		}
	}
	// .part 文件应被删
	if _, err := os.Stat(failedTemp); !os.IsNotExist(err) {
		t.Errorf("expected failed .part to be removed, stat err = %v", err)
	}
}

// TestStallDetectorConcurrent: 并发跑 detectAndFailStalledTasks + 正常
// updateOnlineDownloadTask 不应引发 race 或 panic。
func TestStallDetectorConcurrent(t *testing.T) {
	id := "race-stall"
	defer cleanupTestTasks(id)
	injectTestTask(id, "downloading", 0, time.Now().Add(-2*time.Minute), "")

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < 50; r++ {
				detectAndFailStalledTasks()
				updateOnlineDownloadTask(id, func(t *onlineDownloadTask) { t.Progress = r })
			}
		}()
	}
	wg.Wait()
}

// TestStallDetectorCoversBrowserModeTasks: stall detector 同样作用于
// browser 模式（Mode == "browser"）的下载任务——前端 download dialog
// 的"浏览器下载"路径创建的 task 即是这种。
func TestStallDetectorCoversBrowserModeTasks(t *testing.T) {
	id := "browser-stall-test"
	defer cleanupTestTasks(id)

	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:        id,
		Mode:      "browser",
		Status:    "downloading",
		CreatedAt: time.Now().Add(-2 * time.Minute),
		UpdatedAt: time.Now().Add(-2 * time.Minute),
	}
	onlineDownloadTasks.Unlock()

	detectAndFailStalledTasks()

	onlineDownloadTasks.RLock()
	task, ok := onlineDownloadTasks.items[id]
	onlineDownloadTasks.RUnlock()
	if !ok {
		t.Fatal("task disappeared unexpectedly")
	}
	if task.Status != "failed" {
		t.Errorf("expected status=failed for browser task, got %q", task.Status)
	}
	if !strings.Contains(task.Error, "stalled") {
		t.Errorf("expected Error to mention 'stalled', got %q", task.Error)
	}
}

// TestStallDetectorIgnoresEmptyModeForBackwardsCompat: 旧任务 Mode 为空
// 时 stall detector 仍能识别（回归保险）。
func TestStallDetectorIgnoresEmptyModeForBackwardsCompat(t *testing.T) {
	id := "empty-mode-test"
	defer cleanupTestTasks(id)

	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:        id,
		Mode:      "",
		Status:    "downloading",
		CreatedAt: time.Now().Add(-2 * time.Minute),
		UpdatedAt: time.Now().Add(-2 * time.Minute),
	}
	onlineDownloadTasks.Unlock()

	detectAndFailStalledTasks()

	onlineDownloadTasks.RLock()
	task, ok := onlineDownloadTasks.items[id]
	onlineDownloadTasks.RUnlock()
	if !ok {
		t.Fatal("task disappeared")
	}
	if task.Status != "failed" {
		t.Errorf("expected legacy empty-mode task to be failed, got %q", task.Status)
	}
}
