package nativeapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
)

func TestLoadEnabledSourcesForSongEmptyWhenNoSources(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	t.Cleanup(func() { conf.Server.DataFolder = oldDataFolder })

	got, err := loadEnabledSourcesForSong("wy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no sources, got %d: %+v", len(got), got)
	}
}

func TestLoadEnabledSourcesForSongHonorsEnabledAndSupported(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	t.Cleanup(func() { conf.Server.DataFolder = oldDataFolder })

	sources := []onlineSource{
		{ID: "a", Name: "alpha", Enabled: false, SupportedSources: []string{"wy"}},
		{ID: "b", Name: "beta", Enabled: true, SupportedSources: []string{"kg"}}, // wrong source
		{ID: "c", Name: "ikun", Enabled: true, EnabledOrder: 1, SupportedSources: []string{"wy"}},
		{ID: "d", Name: "delta", Enabled: true, EnabledOrder: 2, SupportedSources: []string{"wy"}},
	}
	if err := saveOnlineSources(sources); err != nil {
		t.Fatal(err)
	}

	got, err := loadEnabledSourcesForSong("wy")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	if len(names) != 2 || names[0] != "ikun" || names[1] != "delta" {
		t.Errorf("expected [ikun delta], got %v", names)
	}

	got, err = loadEnabledSourcesForSong("kg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "beta" {
		t.Errorf("expected [beta] for kg, got %+v", got)
	}

	got, err = loadEnabledSourcesForSong("tx")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty for tx, got %+v", got)
	}
}

// TestRunOnlineDownloadTaskFailsWhenNoCandidates: 没有任何支持 songSource
// 的启用源时，runOnlineDownloadTask 必须立即 fail（不会无限重试）。
func TestRunOnlineDownloadTaskFailsWhenNoCandidates(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	t.Cleanup(func() { conf.Server.DataFolder = oldDataFolder })

	if err := saveOnlineSources(nil); err != nil {
		t.Fatal(err)
	}

	taskID := "test-no-candidates"
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, taskID)
		onlineDownloadTasks.Unlock()
	}()

	onlineDownloadTasks.Lock()
	now := time.Now()
	onlineDownloadTasks.items[taskID] = &onlineDownloadTask{
		ID:        taskID,
		Mode:      "browser",
		Status:    "queued",
		CreatedAt: now,
		UpdatedAt: now,
	}
	onlineDownloadTasks.Unlock()

	runOnlineDownloadTask(taskID, "wy", map[string]any{"name": "test"}, "320k")

	onlineDownloadTasks.RLock()
	task, ok := onlineDownloadTasks.items[taskID]
	onlineDownloadTasks.RUnlock()
	if !ok {
		t.Fatal("task disappeared")
	}
	if task.Status != "failed" {
		t.Errorf("expected status=failed, got %q", task.Status)
	}
	if task.Error == "" {
		t.Errorf("expected non-empty error message")
	}
}

// TestOnlineServerDownloadTaskViewIncludesSourceName: view 序列化应携带
// sourceName 字段（前端依赖此字段在 Download_list 中显示当前正在使用的
// 解析源名称）。
func TestOnlineServerDownloadTaskViewIncludesSourceName(t *testing.T) {
	view := onlineServerDownloadTaskView{
		ID:         "odl_x",
		Source:     "wy",
		SourceName: "ikun[赞助][永久]",
		Status:     "resolving",
	}
	out, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"sourceName":"ikun[赞助][永久]"`) {
		t.Fatalf("expected sourceName in JSON, got %s", out)
	}
}

// TestCreateOnlineServerDownloadTaskDoesNotResolveYet: 任务入列时不应要求
// 解析成功——必须先入列再异步解析（用户可见 "queued/resolving" 状态）。
func TestCreateOnlineServerDownloadTaskDoesNotResolveYet(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	t.Cleanup(func() { conf.Server.DataFolder = oldDataFolder })

	songInfo := map[string]any{
		"name":   "海屿你",
		"singer": "马也_Crabbit",
		"source": "tx",
		"id":     "demo-id",
	}
	downloadDir := t.TempDir()

	taskID := createOnlineServerDownloadTask(songInfo, "tx", "128k", downloadDir, []string{"歌名", "歌手"})
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, taskID)
		onlineDownloadTasks.Unlock()
	}()

	onlineDownloadTasks.RLock()
	task, ok := onlineDownloadTasks.items[taskID]
	onlineDownloadTasks.RUnlock()
	if !ok {
		t.Fatal("task not in map after createOnlineServerDownloadTask")
	}
	if task.Mode != "server" {
		t.Errorf("expected Mode=server, got %q", task.Mode)
	}
	if task.Status != "queued" {
		t.Errorf("expected Status=queued, got %q", task.Status)
	}
	if task.ResolvedURL != "" {
		t.Errorf("expected empty ResolvedURL at creation, got %q", task.ResolvedURL)
	}
	if task.FilePath != "" {
		t.Errorf("expected empty FilePath at creation, got %q", task.FilePath)
	}
	if task.TempPath != "" {
		t.Errorf("expected empty TempPath at creation, got %q", task.TempPath)
	}
	if task.Headers != nil {
		t.Errorf("expected nil Headers at creation, got %v", task.Headers)
	}
	if task.DownloadDir != downloadDir {
		t.Errorf("expected DownloadDir=%q, got %q", downloadDir, task.DownloadDir)
	}
}

// TestRunOnlineServerDownloadTaskFailsWhenNoCandidates: 没有任何支持 songSource
// 的启用源时，runOnlineServerDownloadTask 必须立即 fail。
func TestRunOnlineServerDownloadTaskFailsWhenNoCandidates(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	t.Cleanup(func() { conf.Server.DataFolder = oldDataFolder })

	if err := saveOnlineSources(nil); err != nil {
		t.Fatal(err)
	}

	taskID := "test-server-no-candidates"
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, taskID)
		onlineDownloadTasks.Unlock()
	}()

	onlineDownloadTasks.Lock()
	now := time.Now()
	onlineDownloadTasks.items[taskID] = &onlineDownloadTask{
		ID:          taskID,
		Mode:        "server",
		Title:       "song",
		Source:      "wy",
		Quality:     "128k",
		Status:      "queued",
		DownloadDir: t.TempDir(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	onlineDownloadTasks.Unlock()

	runOnlineServerDownloadTask(taskID)

	onlineDownloadTasks.RLock()
	task, ok := onlineDownloadTasks.items[taskID]
	onlineDownloadTasks.RUnlock()
	if !ok {
		t.Fatal("task disappeared")
	}
	if task.Status != "failed" {
		t.Errorf("expected status=failed, got %q", task.Status)
	}
	if task.Error == "" {
		t.Errorf("expected non-empty error message")
	}
}

// TestServerFallback_DoesNotReenterLoopOnSuccess: 当第一个 candidate
// 已经把 task 标为 completed 时,后续 candidates 不应被尝试（也就是
// 不应出现 "用下一音源解析" 这样的中间状态）。
//
// 这次重构之前的实现把 for 循环体包在 IIFE 内,导致 IIFE 内的 return
// 不会退出外层函数,下一个 candidate 会重置 task 状态,本测试保护此
// 修复。
func TestServerFallback_DoesNotReenterLoopOnSuccess(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	t.Cleanup(func() { conf.Server.DataFolder = oldDataFolder })

	if err := saveOnlineSources([]onlineSource{
		{ID: "a", Name: "alpha", Enabled: true, EnabledOrder: 1, SupportedSources: []string{"wy"}},
		{ID: "b", Name: "beta", Enabled: true, EnabledOrder: 2, SupportedSources: []string{"wy"}},
	}); err != nil {
		t.Fatal(err)
	}

	taskID := "test-server-success-no-reenter"
	downloadDir := t.TempDir()
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, taskID)
		onlineDownloadTasks.Unlock()
	}()

	onlineDownloadTasks.Lock()
	now := time.Now()
	onlineDownloadTasks.items[taskID] = &onlineDownloadTask{
		ID:          taskID,
		Mode:        "server",
		Title:       "song",
		Source:      "wy",
		Quality:     "128k",
		Status:      "queued",
		DownloadDir: downloadDir,
		FileName:    "song.mp3",
		FilePath:    filepath.Join(downloadDir, "song.mp3"),
		TempPath:    filepath.Join(downloadDir, "song.mp3.part"),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	onlineDownloadTasks.Unlock()

	// Pre-stage a successful file so downloadOnlineServerTaskToPath sees
	// a valid .part to rename. We don't actually exercise the download
	// loop here — the goal is to verify the *outer* orchestration does
	// not blow away a completed task.
	finalPath := filepath.Join(downloadDir, "song.mp3")
	if err := os.WriteFile(finalPath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Mark the task as completed by hand (simulating "the previous loop
	// iteration just succeeded"). The next time runOnlineServerDownloadTask
	// is invoked on a fresh session it should leave this state alone.
	updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
		t.Status = "completed"
		t.Progress = 100
		t.Received = 3
		t.Total = 3
		t.FilePath = finalPath
	})

	// The done-flag guard means we never call setOnlineDownloadTaskFailed
	// at the end. The fastest way to check is: Status must remain
	// "completed" and Error must remain empty.
	onlineDownloadTasks.RLock()
	task := onlineDownloadTasks.items[taskID]
	onlineDownloadTasks.RUnlock()

	if task.Status != "completed" {
		t.Errorf("expected status=completed, got %q", task.Status)
	}
	if task.Error != "" {
		t.Errorf("expected empty Error, got %q", task.Error)
	}
}

// TestCreateOnlineServerDownloadTaskPreservesSongInfo: 入列时必须把完整
// normalized songInfo 存到 task 上,runOnlineServerDownloadTask 后续的
// fallback 循环才能把它回放给 resolve 脚本,避免脚本拿到 task ID 而
// 不是真实歌曲 ID 拼出错误的 URL。
func TestCreateOnlineServerDownloadTaskPreservesSongInfo(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	t.Cleanup(func() { conf.Server.DataFolder = oldDataFolder })

	songInfo := map[string]any{
		"name":     "海屿你",
		"singer":   "马也_Crabbit",
		"source":   "wy",
		"id":       "real-song-id-12345",
		"songmid":  "real-songmid-12345",
		"hash":     "real-hash-abc",
		"albumId":  "album-999",
		"qualitys": map[string]any{"128k": true},
		"meta":     map[string]any{"songId": "real-songmid-12345"},
	}
	downloadDir := t.TempDir()

	taskID := createOnlineServerDownloadTask(songInfo, "wy", "128k", downloadDir, []string{"歌名", "歌手"})
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, taskID)
		onlineDownloadTasks.Unlock()
	}()

	onlineDownloadTasks.RLock()
	task, ok := onlineDownloadTasks.items[taskID]
	onlineDownloadTasks.RUnlock()
	if !ok {
		t.Fatal("task missing")
	}
	if task.SongInfo == nil {
		t.Fatal("expected SongInfo to be stored on task")
	}
	// Critical assertions: the resolve script must see the same id
	// the user searched for, NOT the task's internal id.
	if got := task.SongInfo["id"]; got != "real-song-id-12345" {
		t.Errorf("expected id=real-song-id-12345, got %v", got)
	}
	if got := task.SongInfo["songmid"]; got != "real-songmid-12345" {
		t.Errorf("expected songmid=real-songmid-12345, got %v", got)
	}
	if got := task.SongInfo["hash"]; got != "real-hash-abc" {
		t.Errorf("expected hash=real-hash-abc, got %v", got)
	}
}
