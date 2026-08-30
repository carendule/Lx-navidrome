package nativeapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupExpiredDoesNotDeleteServerModeFiles 是修复回归测试：
// 之前 cleanupExpiredOnlineDownloadTasksLocked 不区分 server-mode 和
// browser-mode，对所有过期任务都 os.Remove(task.FilePath)。
// server-mode 任务的 FilePath 指向用户音乐库（task.DownloadDir ==
// conf.Server.MusicFolder），已经被 Navidrome 扫描入库。删掉它会导致
// 下次 watcher 扫描报 tracksMissing=N。
//
// 修复后：expired server-mode 任务只清理内存中的引用，不删磁盘文件。
// expired browser-mode 任务仍然按原意删除（其 FilePath 在 os.TempDir() 里，
// 是临时文件，可以安全删除）。
func TestCleanupExpiredDoesNotDeleteServerModeFiles(t *testing.T) {
	dir := t.TempDir()

	// 模拟一个 server-mode 任务完成后的状态：
	//   - FilePath 指向用户音乐库里的文件（/music/劫-音频怪物.flac）
	//   - UpdatedAt 是 1 小时前（已超过 30 分钟 TTL）
	libraryFile := filepath.Join(dir, "劫-音频怪物.flac")
	if err := os.WriteFile(libraryFile, []byte("fake-flac-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	id := "expired-server-mode"
	defer cleanupTestTasks(id)

	old := time.Now().Add(-1 * time.Hour)
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:          id,
		Mode:        "server",
		Title:       "劫",
		Artist:      "音频怪物",
		Source:      "tx",
		Quality:     "flac",
		Status:      "completed",
		CreatedAt:   old,
		UpdatedAt:   old,
		FilePath:    libraryFile,
		FileName:    "劫-音频怪物.flac",
		DownloadDir: dir,
	}
	onlineDownloadTasks.Unlock()

	// 触发清理
	cleanupExpiredOnlineDownloadTasksLocked(time.Now())

	// 验证：本任务被从内存中清掉了
	if _, ok := getOnlineDownloadTaskPointer(id); ok {
		t.Errorf("expected task to be removed from in-memory map")
	}
	// 关键断言：server-mode 的 FilePath 没有被删除
	if _, err := os.Stat(libraryFile); err != nil {
		t.Errorf("server-mode FilePath should NOT be deleted by cleanup, stat err = %v", err)
	}
}

// TestCleanupExpiredStillDeletesBrowserModeFiles 验证 browser-mode 任务的
// 清理行为没有被破坏。browser-mode 任务的 FilePath 在 os.TempDir() 里，
// 是临时文件，30 分钟后清理掉是正确行为。
func TestCleanupExpiredStillDeletesBrowserModeFiles(t *testing.T) {
	dir := t.TempDir()

	tempFile := filepath.Join(dir, "nd-online-download-browser123")
	if err := os.WriteFile(tempFile, []byte("fake-browser-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	id := "expired-browser-mode"
	defer cleanupTestTasks(id)

	old := time.Now().Add(-1 * time.Hour)
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:        id,
		Mode:      "browser",
		Status:    "completed",
		CreatedAt: old,
		UpdatedAt: old,
		FilePath:  tempFile,
	}
	onlineDownloadTasks.Unlock()

	cleanupExpiredOnlineDownloadTasksLocked(time.Now())

	if _, ok := getOnlineDownloadTaskPointer(id); ok {
		t.Errorf("expected task to be removed from in-memory map")
	}
	// browser-mode 临时文件应该被删除
	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Errorf("browser-mode temp file SHOULD be deleted by cleanup, stat err = %v", err)
	}
}

// TestCleanupExpiredLeavesRecentTasksAlone 验证未过期的任务不会被清理。
func TestCleanupExpiredLeavesRecentTasksAlone(t *testing.T) {
	dir := t.TempDir()
	libraryFile := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(libraryFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	id := "recent-server-mode"
	defer cleanupTestTasks(id)

	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:          id,
		Mode:        "server",
		Status:      "completed",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		FilePath:    libraryFile,
		DownloadDir: dir,
	}
	onlineDownloadTasks.Unlock()

	cleanupExpiredOnlineDownloadTasksLocked(time.Now())

	// 未过期任务不应该被清理
	if _, ok := getOnlineDownloadTaskPointer(id); !ok {
		t.Errorf("recent task should still be in map")
	}
	if _, err := os.Stat(libraryFile); err != nil {
		t.Errorf("recent task file should still exist, stat err = %v", err)
	}
}

// TestCleanupExpiredSkipsServerModeWithoutFilePath 是边界情况：
// server-mode 任务在创建早期 FilePath 还没填好（异步 resolve 阶段）。
// 这种任务即使过期也不应该有任何问题（因为 FilePath 为空，删除分支
// 本来就不会触发）。这个测试只是把这个不变量钉住，防止以后误改。
func TestCleanupExpiredSkipsServerModeWithoutFilePath(t *testing.T) {
	id := "expired-server-mode-no-file"
	defer cleanupTestTasks(id)

	old := time.Now().Add(-1 * time.Hour)
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:        id,
		Mode:      "server",
		Status:    "queued",
		CreatedAt: old,
		UpdatedAt: old,
		FilePath:  "", // 还没填好
	}
	onlineDownloadTasks.Unlock()

	cleanupExpiredOnlineDownloadTasksLocked(time.Now())

	if _, ok := getOnlineDownloadTaskPointer(id); ok {
		t.Errorf("expected task to be removed from in-memory map")
	}
}
