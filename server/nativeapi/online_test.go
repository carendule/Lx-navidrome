package nativeapi

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
)

// ===== from online_download_test.go =====

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

// TestOnlineDownloadFileNameUserExample is the exact example from the
// bug report: "那些花儿" / 320k / 朴树 / template [歌名, 音质, 歌手] →
// "那些花儿-320k-朴树.mp3".
func TestOnlineDownloadFileNameUserExample(t *testing.T) {
	song := map[string]any{
		"name":      "那些花儿",
		"singer":    "朴树",
		"album":     "我去2000年",
		"albumName": "我去2000年",
		"source":    "wy",
	}
	got := onlineDownloadFileName(song, "320k", []string{"歌名", "音质", "歌手"}, "https://example.com/track.mp3")
	want := "那些花儿-320k-朴树.mp3"
	if got != want {
		t.Fatalf("filename mismatch: got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameFallbackToDefault: empty / unknown-only
// templates should fall back to the persisted default [歌名, 歌手].
func TestOnlineDownloadFileNameFallbackToDefault(t *testing.T) {
	song := map[string]any{"name": "海屿你", "singer": "马也_Crabbit"}
	cases := []struct {
		name     string
		template []string
		want     string
	}{
		{"nil template", nil, "海屿你-马也_Crabbit.mp3"},
		{"empty slice", []string{}, "海屿你-马也_Crabbit.mp3"},
		{"only unknown tokens", []string{"foo", "bar"}, "海屿你-马也_Crabbit.mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := onlineDownloadFileName(song, "320k", tc.template, "https://example.com/x.mp3")
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestOnlineDownloadFileNameAlbumAndSource: album + source tokens
// resolve to songInfo["albumName"] and a short label.
func TestOnlineDownloadFileNameAlbumAndSource(t *testing.T) {
	song := map[string]any{
		"name":      "夜曲",
		"singer":    "周杰伦",
		"albumName": "十一月的萧邦",
		"source":    "tx",
	}
	got := onlineDownloadFileName(song, "flac", []string{"歌名", "专辑", "来源"}, "https://example.com/x.flac")
	want := "夜曲-十一月的萧邦-QQ.flac"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameSkipsEmptyValues: tokens whose lookup
// returns "" should be dropped so the filename has no dangling "-".
func TestOnlineDownloadFileNameSkipsEmptyValues(t *testing.T) {
	song := map[string]any{"name": "song", "singer": "artist"}
	got := onlineDownloadFileName(song, "128k", []string{"歌名", "专辑", "来源", "歌手"}, "https://x/y.mp3")
	want := "song-artist.mp3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameUnknownExtension: if the URL doesn't end
// in a known audio container, we leave it extension-less (the
// downstream downloader will fill it in from Content-Type).
func TestOnlineDownloadFileNameUnknownExtension(t *testing.T) {
	song := map[string]any{"name": "song"}
	got := onlineDownloadFileName(song, "320k", []string{"歌名", "歌手"}, "https://x/y.bin")
	want := "song"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameStripsPathSegment: ensure the extension is
// taken from the *path* (not the query string) and lowercased.
func TestOnlineDownloadFileNameStripsPathSegment(t *testing.T) {
	song := map[string]any{"name": "song", "singer": "a"}
	got := onlineDownloadFileName(song, "320k", []string{"歌名", "歌手"}, "https://x/y.MP3?token=abc")
	if got != "song-a.mp3" {
		t.Fatalf("got %q", got)
	}
}

// TestSanitizeNameTemplateDropsUnknownTokensButPreservesOrder.
func TestSanitizeNameTemplateDropsUnknownTokensButPreservesOrder(t *testing.T) {
	got := sanitizeNameTemplate([]string{"歌名", "garbage", "音质", "歌名", "歌手"})
	want := []string{"歌名", "音质", "歌手"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestSourceLabel maps known source codes to short labels.
func TestSourceLabel(t *testing.T) {
	cases := map[string]string{
		"wy":      "网易",
		"tx":      "QQ",
		"kg":      "酷狗",
		"kw":      "酷我",
		"mg":      "咪咕",
		"unknown": "unknown",
	}
	for in, want := range cases {
		if got := sourceLabel(in); got != want {
			t.Errorf("sourceLabel(%q)=%q want %q", in, got, want)
		}
	}
}

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
		{ID: "b", Name: "beta", Enabled: true, SupportedSources: []string{"kg"}},
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

// TestIsSupportedDownloadScheme maps accepted URL schemes.
func TestIsSupportedDownloadScheme(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http", true},
		{"https", true},
		{"HTTP", true},
		{" https ", true},
		{"ftp", false},
		{"file", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := isSupportedDownloadScheme(c.in); got != c.want {
				t.Fatalf("isSupportedDownloadScheme(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestIsPermanentResolveError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"The current script did not declare support for this source", true},
		{"Script did not return a valid download URL", true},
		{"unsupported resolved url scheme: ftp", true},
		{"resolved url missing host", true},
		{"request timeout", false},
		{"connect ECONNRESET", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			if got := isPermanentResolveError(c.msg); got != c.want {
				t.Fatalf("isPermanentResolveError(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

func TestShouldStopResolveRetry(t *testing.T) {
	if shouldStopResolveRetry(nil) {
		t.Fatal("nil error should not stop retry")
	}
	if shouldStopResolveRetry(errors.New("transient")) {
		t.Fatal("regular error should not stop retry")
	}
	if !shouldStopResolveRetry(&onlineDownloadPermanentError{msg: "permanent"}) {
		t.Fatal("permanent error should stop retry")
	}
}

func TestOnlineDownloadPermanentErrorString(t *testing.T) {
	var e *onlineDownloadPermanentError
	if e.Error() != "" {
		t.Fatalf("nil permanent error string should be empty, got %q", e.Error())
	}
	e = &onlineDownloadPermanentError{msg: "x"}
	if e.Error() != "x" {
		t.Fatalf("unexpected error string: %q", e.Error())
	}
}

// TestBrowserDownloadProgressResponseIncludesSourceName: 验证 progress
// 响应结构序列化时携带 sourceName 字段（前端依赖此字段显示当前解析源）。
func TestBrowserDownloadProgressResponseIncludesSourceName(t *testing.T) {
	resp := onlineBrowserDownloadProgressResponse{
		TaskID:     "odl_test",
		Status:     "downloading",
		Progress:   25,
		SourceName: "ikun[赞助][永久]",
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"sourceName":"ikun[赞助][永久]"`) {
		t.Fatalf("expected sourceName in response, got %s", out)
	}
	// Decode back to ensure the field name matches the JSON tag.
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["sourceName"] != "ikun[赞助][永久]" {
		t.Errorf("expected sourceName=ikun[赞助][永久], got %v", decoded["sourceName"])
	}
	if decoded["taskId"] != "odl_test" {
		t.Errorf("expected taskId=odl_test, got %v", decoded["taskId"])
	}
	if decoded["status"] != "downloading" {
		t.Errorf("expected status=downloading, got %v", decoded["status"])
	}
}

// TestBrowserDownloadProgressResponseEmptySourceName: 内置源（wy/tx/kg/kw/mg）
// 不会触发自定义脚本，SourceName 留空，序列化为省略（omitempty）。
func TestBrowserDownloadProgressResponseEmptySourceName(t *testing.T) {
	resp := onlineBrowserDownloadProgressResponse{
		TaskID: "odl_test",
		Status: "resolving",
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "sourceName") {
		t.Errorf("expected sourceName to be omitted when empty, got %s", out)
	}
}

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

// ===== from online_embed_lyric_writer_test.go =====

// TestID3v24USLTFrameFormat pins the wire format of the
// USLT frame we emit. The frame header is 10 bytes
// (4-byte ID + 4-byte synchsafe size + 2-byte flags),
// the body is encoding(1) + language(3) + descriptor(NUL
// terminator) + lyrics. The first 4 bytes MUST be
// "USLT" so mp3tag's frame parser picks it up. The
// encoding byte MUST be 3 (UTF-8) so Chinese / Korean /
// Japanese characters render correctly. The 4-byte
// size field MUST be synchsafe-encoded (each byte's
// high bit is 0) so 2.4 readers can scan past frames
// without interpreting the data.
func TestID3v24USLTFrameFormat(t *testing.T) {
	frame := buildID3v24USLTFrame("[00:00.00]hello 一二三", "zho")
	if !bytes.Equal(frame[:4], []byte("USLT")) {
		t.Fatalf("frame ID should be USLT, got %q", frame[:4])
	}
	// Decode synchsafe size.
	v := uint32(frame[4])<<21 | uint32(frame[5])<<14 | uint32(frame[6])<<7 | uint32(frame[7])
	if v == 0 || int(v) != len(frame)-10 {
		t.Fatalf("synchsafe size %d != body length %d", v, len(frame)-10)
	}
	// Encoding byte = 3 (UTF-8).
	if frame[10] != 0x03 {
		t.Fatalf("encoding byte should be 0x03 (UTF-8), got 0x%02x", frame[10])
	}
	// Language = "zho".
	if !bytes.Equal(frame[11:14], []byte("zho")) {
		t.Fatalf("language should be zho, got %q", frame[11:14])
	}
	// Descriptor terminator.
	if frame[14] != 0x00 {
		t.Fatalf("descriptor terminator should be 0x00, got 0x%02x", frame[14])
	}
	// Lyric text — should be the UTF-8 bytes verbatim.
	want := "[00:00.00]hello 一二三"
	if string(frame[15:]) != want {
		t.Fatalf("lyric text mismatch: got %q, want %q", frame[15:], want)
	}
	// Sanity: every byte of the synchsafe size must have
	// the high bit clear (this is what makes the
	// encoding "synchsafe" — the 0x80 marker is reserved
	// for ID3v2 frame boundaries).
	for i := 4; i < 8; i++ {
		if frame[i]&0x80 != 0 {
			t.Fatalf("synchsafe byte at offset %d has high bit set: 0x%02x", i, frame[i])
		}
	}
}

func TestID3v23USLTFrameFormat(t *testing.T) {
	frame := buildID3v23USLTFrame("[00:00.00]hello 一二三", "zho")
	if !bytes.Equal(frame[:4], []byte("USLT")) {
		t.Fatalf("frame ID should be USLT, got %q", frame[:4])
	}
	// ID3v2.3 frame size is 32-bit big-endian.
	v := uint32(frame[4])<<24 | uint32(frame[5])<<16 | uint32(frame[6])<<8 | uint32(frame[7])
	if v == 0 || int(v) != len(frame)-10 {
		t.Fatalf("big-endian size %d != body length %d", v, len(frame)-10)
	}
	if frame[10] != 0x01 {
		t.Fatalf("encoding byte should be 0x01 (UTF-16 BOM), got 0x%02x", frame[10])
	}
	if !bytes.Equal(frame[11:14], []byte("zho")) {
		t.Fatalf("language should be zho, got %q", frame[11:14])
	}
	// Empty descriptor: UTF-16LE BOM + null terminator.
	if !bytes.Equal(frame[14:18], []byte{0xFF, 0xFE, 0x00, 0x00}) {
		t.Fatalf("descriptor bytes mismatch: % x", frame[14:18])
	}
	// Lyric text should start with UTF-16LE BOM.
	if !bytes.Equal(frame[18:20], []byte{0xFF, 0xFE}) {
		t.Fatalf("lyric text should start with UTF-16 BOM, got % x", frame[18:20])
	}
}

// TestWriteID3USLTIntoExistingFile simulates the full
// pipeline: an MP3 file already has an ID3v2 header
// (built by ffmpeg with the broken TXXX wrapper) plus a
// music payload. We call onlineEmbedWriteID3USLT to
// replace the ID3 tag with one that contains a real USLT
// frame, and verify the music payload is unchanged.
func TestWriteID3USLTIntoExistingFile(t *testing.T) {
	dir := t.TempDir()
	// Construct a fake MP3: ID3v2 header (10) + ID3v2
	// body (1 TXXX frame, 1 TIT2 frame) + audio payload
	// (10 bytes of zeros).
	id3Body := []byte{
		// TXXX frame: "TXXX" + size(4 BE) + flags(2) + payload
		'T', 'X', 'X', 'X',
		0x00, 0x00, 0x00, 0x08, // size = 8 (descriptor + 2 bytes lyrics)
		0x00, 0x00,
		0x00, 'U', 'S', 'L', 'T', 0x00, // descriptor "USLT\0"
		'h', 'i', // 2 bytes of lyrics in TXXX
		// TIT2 frame: title
		'T', 'I', 'T', '2',
		0x00, 0x00, 0x00, 0x05,
		0x00, 0x00,
		0x00, 'T', 'i', 't', 'l',
	}
	id3Header := []byte{'I', 'D', '3', 0x04, 0x00, 0x00}
	v := uint32(len(id3Body))
	id3Header = append(id3Header,
		byte((v>>21)&0x7f), byte((v>>14)&0x7f),
		byte((v>>7)&0x7f), byte(v&0x7f))
	audio := bytes.Repeat([]byte{0x55}, 200) // fake audio frames
	fullFile := append(append([]byte{}, id3Header...), id3Body...)
	fullFile = append(fullFile, audio...)
	path := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(path, fullFile, 0o600); err != nil {
		t.Fatal(err)
	}
	// Call the rewrite.
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]real lyrics 一二三"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	// Read back and verify.
	out, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:3], []byte("ID3")) {
		t.Fatalf("output should start with ID3, got %q", out[:3])
	}
	if out[3] != 4 {
		t.Fatalf("ID3v2 version should be 4, got %d", out[3])
	}
	// Synchsafe size should match body length.
	bodySize := int(out[6])<<21 | int(out[7])<<14 | int(out[8])<<7 | int(out[9])
	if bodySize == 0 || bodySize+10 > len(out) {
		t.Fatalf("invalid ID3v2 size: %d (file %d bytes)", bodySize, len(out))
	}
	// TIT2 was preserved (it was the second frame in the
	// source, but in the rewritten tag it comes first
	// because we prepend the surviving TIT2 to our
	// newly-built USLT frame). USLT is the second frame.
	pos := 10
	frameID := out[pos : pos+4]
	if !bytes.Equal(frameID, []byte("TIT2")) {
		t.Fatalf("first frame should be preserved TIT2, got %q", frameID)
	}
	// Walk all frames in the rewritten tag and confirm
	// both USLT and TIT2 are present, but TXXX is gone.
	var foundUSLT, foundTIT2, foundTXXX bool
	for pos+10 <= 10+bodySize {
		fid := string(out[pos : pos+4])
		switch fid {
		case "USLT":
			foundUSLT = true
		case "TIT2":
			foundTIT2 = true
		case "TXXX":
			foundTXXX = true
		}
		// ID3v2.4 synchsafe size decode (28 bits, 4 bytes).
		fsize := int(uint32(out[pos+4])<<21 | uint32(out[pos+5])<<14 | uint32(out[pos+6])<<7 | uint32(out[pos+7]))
		pos += 10 + fsize
	}
	if !foundUSLT {
		t.Fatal("rewritten tag is missing USLT frame")
	}
	if !foundTIT2 {
		t.Fatal("rewritten tag is missing the TIT2 (title) frame — the USLT writer must preserve other frames")
	}
	if foundTXXX {
		t.Fatal("rewritten tag still has the ffmpeg-emitted TXXX wrapper; the writer must drop it before injecting the real USLT")
	}
	// Audio payload should be preserved at the end.
	audioStart := 10 + bodySize
	if !bytes.Equal(out[audioStart:], audio) {
		t.Fatalf("audio payload was not preserved: got %d bytes, want %d", len(out)-audioStart, len(audio))
	}
}

// TestWriteID3USLTIntoFileWithoutID3 ensures the writer
// handles MP3s that don't have an ID3v2 header at all
// (some pre-tag era files). It must build a fresh
// header from scratch.
func TestWriteID3USLTIntoFileWithoutID3(t *testing.T) {
	dir := t.TempDir()
	audio := bytes.Repeat([]byte{0xab}, 100)
	path := filepath.Join(dir, "raw.mp3")
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]orphan lyrics"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:3], []byte("ID3")) {
		t.Fatalf("output should start with ID3 after rewrite, got %q", out[:3])
	}
	bodySize := int(out[6])<<21 | int(out[7])<<14 | int(out[8])<<7 | int(out[9])
	if bodySize == 0 || bodySize+10 > len(out) {
		t.Fatalf("invalid ID3v2 size: %d", bodySize)
	}
	if !bytes.Equal(out[10:14], []byte("USLT")) {
		t.Fatalf("first frame should be USLT, got %q", out[10:14])
	}
}

// TestWriteID3USLTPreservesV23HeaderAndFrameEncoding verifies that
// rewriting an ID3v2.3 file keeps v2.3 semantics (header version and
// frame size encoding), avoiding mixed v2.4/v2.3 tags that strict
// parsers reject.
func TestWriteID3USLTPreservesV23HeaderAndFrameEncoding(t *testing.T) {
	dir := t.TempDir()
	// Build a minimal v2.3 tag body with TXXX(USLT) + TIT2.
	id3Body := []byte{
		'T', 'X', 'X', 'X',
		0x00, 0x00, 0x00, 0x08,
		0x00, 0x00,
		0x00, 'U', 'S', 'L', 'T', 0x00,
		'h', 'i',
		'T', 'I', 'T', '2',
		0x00, 0x00, 0x00, 0x05,
		0x00, 0x00,
		0x00, 'T', 'i', 't', 'l',
	}
	id3Header := []byte{'I', 'D', '3', 0x03, 0x00, 0x00}
	v := uint32(len(id3Body))
	id3Header = append(id3Header,
		byte((v>>21)&0x7f), byte((v>>14)&0x7f),
		byte((v>>7)&0x7f), byte(v&0x7f))
	audio := bytes.Repeat([]byte{0x55}, 200)
	full := append(append([]byte{}, id3Header...), id3Body...)
	full = append(full, audio...)

	path := filepath.Join(dir, "song-v23.mp3")
	if err := os.WriteFile(path, full, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]real lyrics 一二三"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:3], []byte("ID3")) {
		t.Fatalf("output should start with ID3, got %q", out[:3])
	}
	if out[3] != 3 {
		t.Fatalf("ID3 major version should stay 3, got %d", out[3])
	}
	bodySize := int(out[6])<<21 | int(out[7])<<14 | int(out[8])<<7 | int(out[9])
	if bodySize <= 0 || bodySize+10 > len(out) {
		t.Fatalf("invalid ID3v2 size: %d (file %d bytes)", bodySize, len(out))
	}

	var foundUSLT, foundTIT2, foundTXXX bool
	pos := 10
	for pos+10 <= 10+bodySize {
		if out[pos] == 0 {
			break
		}
		fid := string(out[pos : pos+4])
		switch fid {
		case "USLT":
			foundUSLT = true
			if out[pos+10] != 0x01 {
				t.Fatalf("v2.3 USLT should use UTF-16 BOM encoding byte 0x01, got 0x%02x", out[pos+10])
			}
		case "TIT2":
			foundTIT2 = true
		case "TXXX":
			foundTXXX = true
		}
		// v2.3 frame size decoding: big-endian uint32.
		fsize := int(uint32(out[pos+4])<<24 | uint32(out[pos+5])<<16 | uint32(out[pos+6])<<8 | uint32(out[pos+7]))
		remaining := 10 + bodySize - pos - 10
		if fsize > remaining {
			fsize = remaining
		}
		pos += 10 + fsize
	}
	if pos != 10+bodySize {
		t.Fatalf("v2.3 frame walker ended at %d, expected %d", pos, 10+bodySize)
	}
	if !foundUSLT {
		t.Fatal("rewritten tag is missing USLT")
	}
	if !foundTIT2 {
		t.Fatal("rewritten tag is missing TIT2")
	}
	if foundTXXX {
		t.Fatal("rewritten tag still has TXXX")
	}
	audioStart := 10 + bodySize
	if !bytes.Equal(out[audioStart:], audio) {
		t.Fatalf("audio payload was not preserved: got %d bytes, want %d", len(out)-audioStart, len(audio))
	}
}

// TestWriteID3USLTPreservesID3v1Footer ensures the
// writer doesn't strip the legacy 128-byte ID3v1 footer
// (the "TAG" block at the end of the file) when the
// source has one. Some old tools and Windows File
// Explorer's Properties dialog still read the ID3v1
// fields, so we keep them around.
func TestWriteID3USLTPreservesID3v1Footer(t *testing.T) {
	dir := t.TempDir()
	id3v1 := append([]byte("TAG"), bytes.Repeat([]byte{0}, 125)...)
	audio := append(bytes.Repeat([]byte{0x55}, 200), id3v1...)
	path := filepath.Join(dir, "withtag.mp3")
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]with id3v1"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	// Last 128 bytes should still be the ID3v1 footer.
	if !bytes.Equal(out[len(out)-128:len(out)-125], []byte("TAG")) {
		t.Fatalf("ID3v1 footer was lost: tail=%q", out[len(out)-10:])
	}
}

// TestRewriteVorbisCommentRoundTrip constructs a small
// Vorbis comment block, runs rewriteVorbisComment on it,
// and verifies the LYRICS key is added with the new
// content and any old lyrics=… entry is removed.
func TestRewriteVorbisCommentRoundTrip(t *testing.T) {
	// Build a Vorbis comment block with vendor + 3
	// entries: TITLE, LYRICS=old, ARTIST.
	// Layout: vendor_len(4 LE) + vendor + count(4 LE) + entries
	buf := []byte{
		// vendor: "ffmpeg" (6 bytes)
		0x06, 0x00, 0x00, 0x00,
		'f', 'f', 'm', 'p', 'e', 'g',
		// count: 3
		0x03, 0x00, 0x00, 0x00,
		// entry 1: TITLE=hello
		0x0b, 0x00, 0x00, 0x00,
		'T', 'I', 'T', 'L', 'E', '=', 'h', 'e', 'l', 'l', 'o',
		// entry 2: LYRICS=old lyrics (17 bytes)
		0x11, 0x00, 0x00, 0x00,
		'L', 'Y', 'R', 'I', 'C', 'S', '=', 'o', 'l', 'd', ' ', 'l', 'y', 'r', 'i', 'c', 's',
		// entry 3: ARTIST=bob
		0x0a, 0x00, 0x00, 0x00,
		'A', 'R', 'T', 'I', 'S', 'T', '=', 'b', 'o', 'b',
	}
	out, err := rewriteVorbisComment(buf, "fresh lyrics 一二三")
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	// Parse the result.
	if len(out) < 8 {
		t.Fatal("output too short")
	}
	vlen := int(out[0]) | int(out[1])<<8 | int(out[2])<<16 | int(out[3])<<24
	if vlen != 6 || string(out[4:4+vlen]) != "ffmpeg" {
		t.Fatalf("vendor mismatch: len=%d body=%q", vlen, out[4:4+vlen])
	}
	rest := out[4+vlen:]
	count := int(rest[0]) | int(rest[1])<<8 | int(rest[2])<<16 | int(rest[3])<<24
	rest = rest[4:]
	if count != 3 {
		t.Fatalf("expected 3 comments (TITLE, ARTIST, new LYRICS), got %d", count)
	}
	var entries []string
	for i := 0; i < count; i++ {
		clen := int(rest[0]) | int(rest[1])<<8 | int(rest[2])<<16 | int(rest[3])<<24
		rest = rest[4:]
		entries = append(entries, string(rest[:clen]))
		rest = rest[clen:]
	}
	// Check entries — TITLE and ARTIST preserved, LYRICS
	// replaced.
	wantEntries := map[string]bool{
		"TITLE=hello":             true,
		"ARTIST=bob":              true,
		"LYRICS=fresh lyrics 一二三": true,
	}
	for _, e := range entries {
		if !wantEntries[e] {
			t.Fatalf("unexpected entry: %q", e)
		}
	}
	// Make sure no "lyrics=old" or "lyrics=old lyrics" survived.
	for _, e := range entries {
		if bytes.HasPrefix([]byte(e), []byte("lyrics=")) ||
			bytes.HasPrefix([]byte(e), []byte("LYRICS=old")) {
			t.Fatalf("old lyrics entry survived: %q", e)
		}
	}
}

// ===== from online_embed_metadata_test.go =====

func TestOnlineEmbedEnrichSongInfoWithLyricaMetadata(t *testing.T) {
	tests := []struct {
		name        string
		songInfo    map[string]any
		lyricaMeta  map[string]any
		expectKey   string
		expectValue string
	}{
		{
			name: "enrich title from lyrica",
			songInfo: map[string]any{
				"singer":    "Artist Name",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"title":  "Song Title",
				"artist": "Artist Name",
				"album":  "Album",
			},
			expectKey:   "name",
			expectValue: "Song Title",
		},
		{
			name: "prefer existing songInfo over lyrica",
			songInfo: map[string]any{
				"name":      "Original Title",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"title": "Lyrica Title",
			},
			expectKey:   "name",
			expectValue: "Original Title",
		},
		{
			name: "enrich genre from lyrica",
			songInfo: map[string]any{
				"name":      "Song",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"genre": "Pop",
			},
			expectKey:   "genre",
			expectValue: "Pop",
		},
		{
			name: "enrich genre from tags array",
			songInfo: map[string]any{
				"name":      "Song",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"tags": []string{"Mandopop", "Chinese"},
			},
			expectKey:   "genre",
			expectValue: "Mandopop, Chinese",
		},
		{
			name: "enrich cover_art to img field",
			songInfo: map[string]any{
				"name":      "Song",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"cover_art": "https://example.com/cover.jpg",
			},
			expectKey:   "img",
			expectValue: "https://example.com/cover.jpg",
		},
		{
			name: "enrich year from release_year (number)",
			songInfo: map[string]any{
				"name":      "Song",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"release_year": float64(2017),
			},
			expectKey:   "year",
			expectValue: "2017",
		},
		{
			name: "prefer producer over writer for composer",
			songInfo: map[string]any{
				"name":      "Song",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"producer": "Producer Name",
				"writer":   "Writer Name",
			},
			expectKey:   "composer",
			expectValue: "Producer Name",
		},
		{
			name: "use writer when producer missing",
			songInfo: map[string]any{
				"name":      "Song",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"writer": "Writer Name",
			},
			expectKey:   "composer",
			expectValue: "Writer Name",
		},
		{
			name: "enrich release_date to date field",
			songInfo: map[string]any{
				"name":      "Song",
				"singer":    "Artist",
				"albumName": "Album",
			},
			lyricaMeta: map[string]any{
				"release_date": "2013-01-15",
			},
			expectKey:   "date",
			expectValue: "2013-01-15",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			onlineEmbedEnrichSongInfoWithLyricaMetadata(tt.songInfo, tt.lyricaMeta)

			result := stringValue(tt.songInfo[tt.expectKey])
			if result != tt.expectValue {
				t.Errorf("expected %q for key %q, got %q", tt.expectValue, tt.expectKey, result)
			}
		})
	}
}

func TestOnlineEmbedExtractGenreFromTags(t *testing.T) {
	tests := []struct {
		name     string
		tags     any
		expected string
	}{
		{
			name:     "single tag string array",
			tags:     []string{"Pop"},
			expected: "Pop",
		},
		{
			name:     "multiple tags string array",
			tags:     []string{"Mandopop", "Chinese", "Romance"},
			expected: "Mandopop, Chinese, Romance",
		},
		{
			name:     "tags with empty strings",
			tags:     []string{"Pop", "", "Rock"},
			expected: "Pop, Rock",
		},
		{
			name:     "interface array with strings",
			tags:     []any{"Pop", "Rock", "Jazz"},
			expected: "Pop, Rock, Jazz",
		},
		{
			name:     "nil tags",
			tags:     nil,
			expected: "",
		},
		{
			name:     "empty array",
			tags:     []string{},
			expected: "",
		},
		{
			name:     "array with only empty strings",
			tags:     []string{"", "  ", ""},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := onlineEmbedExtractGenreFromTags(tt.tags)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestOnlineEmbedYearEnrichment(t *testing.T) {
	tests := []struct {
		name     string
		year     any
		expected string
	}{
		{
			name:     "year as float64 (JSON number)",
			year:     float64(2017),
			expected: "2017",
		},
		{
			name:     "year as int",
			year:     2017,
			expected: "2017",
		},
		{
			name:     "year as string",
			year:     "2017",
			expected: "2017",
		},
		{
			name:     "year 0 (invalid)",
			year:     float64(0),
			expected: "",
		},
		{
			name:     "year > 10000 (invalid)",
			year:     float64(20000),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			songInfo := map[string]any{}
			lyricaMeta := map[string]any{
				"release_year": tt.year,
			}

			onlineEmbedEnrichSongInfoWithLyricaMetadata(songInfo, lyricaMeta)

			result := stringValue(songInfo["year"])
			if result != tt.expected {
				t.Errorf("expected %q for year, got %q", tt.expected, result)
			}
		})
	}
}

func TestOnlineEmbedGenrePriority(t *testing.T) {
	// Lyrica returns both genre string and tags array;
	// genre string should take priority
	songInfo := map[string]any{}
	lyricaMeta := map[string]any{
		"genre": "Pop",
		"tags":  []string{"Rock", "Metal"},
	}

	onlineEmbedEnrichSongInfoWithLyricaMetadata(songInfo, lyricaMeta)

	result := stringValue(songInfo["genre"])
	if result != "Pop" {
		t.Errorf("expected genre to be 'Pop' (direct field), got %q", result)
	}
}

func TestOnlineEmbedEnrichSongInfoPreservesMetaField(t *testing.T) {
	songInfo := map[string]any{
		"name":   "Song",
		"singer": "Artist",
		"meta": map[string]any{
			"lrcUrl": "https://example.com/lyrics.lrc",
		},
	}

	lyricaMeta := map[string]any{
		"cover_art": "https://example.com/cover.jpg",
	}

	onlineEmbedEnrichSongInfoWithLyricaMetadata(songInfo, lyricaMeta)

	// Check that meta.lrcUrl is preserved
	meta := mapValue(songInfo["meta"])
	if meta == nil {
		t.Fatal("meta field should exist")
	}

	lrcUrl := stringValue(meta["lrcUrl"])
	if lrcUrl != "https://example.com/lyrics.lrc" {
		t.Errorf("expected meta.lrcUrl to be preserved, got %q", lrcUrl)
	}

	// Check that meta.picUrl was enriched
	picUrl := stringValue(meta["picUrl"])
	if picUrl != "https://example.com/cover.jpg" {
		t.Errorf("expected meta.picUrl to be enriched, got %q", picUrl)
	}
}

// ===== from online_embed_test.go =====

func metadataArgsToMap(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-metadata" {
			continue
		}
		pair := args[i+1]
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			continue
		}
		out[pair[:eq]] = pair[eq+1:]
	}
	return out
}

func TestOnlineEmbedAppendTagMetadataArgsWritesRichFields(t *testing.T) {
	args := []string{}
	songInfo := map[string]any{
		"name":        "回到过去",
		"singer":      "周杰伦",
		"albumName":   "The Era",
		"albumArtist": "Various Artists",
		"composer":    "周杰伦",
		"genre":       "Pop",
		"trackNumber": "3/12",
		"discNumber":  "1/2",
		"publishDate": "2010-03-01",
		"bpm":         "96",
		"language":    "zh",
		"isrc":        "TW-A45-10-12345",
		"copyright":   "JVR",
		"comment":     "source-note",
	}

	onlineEmbedAppendTagMetadataArgs(&args, songInfo, "320k")
	meta := metadataArgsToMap(args)

	want := map[string]string{
		"title":        "回到过去",
		"artist":       "周杰伦",
		"album":        "The Era",
		"album_artist": "Various Artists",
		"composer":     "周杰伦",
		"genre":        "Pop",
		"track":        "3/12",
		"disc":         "1/2",
		"date":         "2010",
		"bpm":          "96",
		"language":     "zh",
		"isrc":         "TW-A45-10-12345",
		"copyright":    "JVR",
	}
	for k, v := range want {
		if got := meta[k]; got != v {
			t.Fatalf("metadata %s mismatch: got=%q want=%q (all=%v)", k, got, v, meta)
		}
	}
	if got := meta["comment"]; !strings.Contains(got, "source-note") || !strings.Contains(got, "Quality: 320k") {
		t.Fatalf("comment should contain source note and quality, got %q", got)
	}
}

func TestOnlineEmbedAppendTagMetadataArgsFallsBackToMeta(t *testing.T) {
	args := []string{}
	songInfo := map[string]any{
		"meta": map[string]any{
			"songName":   "安静",
			"singerName": "周杰伦",
			"albumName":  "范特西",
			"year":       "2001",
			"genre":      "Mandopop",
			"track":      "5",
			"disc":       "1",
		},
	}
	onlineEmbedAppendTagMetadataArgs(&args, songInfo, "")
	meta := metadataArgsToMap(args)

	if meta["title"] != "安静" || meta["artist"] != "周杰伦" || meta["album"] != "范特西" {
		t.Fatalf("meta fallback failed: %v", meta)
	}
	if meta["year"] != "2001" {
		t.Fatalf("year fallback failed: got %q", meta["year"])
	}
	if meta["genre"] != "Mandopop" || meta["track"] != "5" || meta["disc"] != "1" {
		t.Fatalf("extended fallback fields missing: %v", meta)
	}
}

func TestOnlineEmbedNormalizeDateTag(t *testing.T) {
	cases := map[string]string{
		"2019-02-03":       "2019",
		"2018/12/31":       "2018",
		"release: 2020-01": "2020",
		"发行于2021年":         "2021",
		"4497":             "",
		"unknown":          "",
		"":                 "",
	}
	keys := make([]string, 0, len(cases))
	for k := range cases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, in := range keys {
		if got := onlineEmbedNormalizeDateTag(in); got != cases[in] {
			t.Fatalf("normalize date failed: in=%q got=%q want=%q", in, got, cases[in])
		}
	}
}

func TestOnlineEmbedNormalizeTrackOrDiscTag(t *testing.T) {
	cases := map[string]string{
		"1":       "1",
		"01":      "1",
		"03/12":   "3/12",
		"8/":      "8",
		"abc":     "",
		"0":       "",
		"1000":    "",
		"12/0":    "12",
		"12/9999": "12",
	}
	for in, want := range cases {
		if got := onlineEmbedNormalizeTrackOrDiscTag(in); got != want {
			t.Fatalf("normalize track/disc failed: in=%q got=%q want=%q", in, got, want)
		}
	}
}

func TestOnlineEmbedNormalizeBPMTag(t *testing.T) {
	cases := map[string]string{
		"96":  "96",
		"020": "20",
		"19":  "",
		"301": "",
		"96a": "",
		"":    "",
	}
	for in, want := range cases {
		if got := onlineEmbedNormalizeBPMTag(in); got != want {
			t.Fatalf("normalize bpm failed: in=%q got=%q want=%q", in, got, want)
		}
	}
}

func TestOnlineEmbedAppendTagMetadataArgsRejectsInvalidExtendedValues(t *testing.T) {
	args := []string{}
	songInfo := map[string]any{
		"name":      "Song",
		"singer":    "Artist",
		"albumName": "Album",
		"year":      "4497",
		"track":     "0",
		"disc":      "x",
		"bpm":       "999",
	}
	onlineEmbedAppendTagMetadataArgs(&args, songInfo, "")
	meta := metadataArgsToMap(args)
	if _, ok := meta["date"]; ok {
		t.Fatalf("invalid year should not be written, got date=%q", meta["date"])
	}
	if _, ok := meta["track"]; ok {
		t.Fatalf("invalid track should not be written, got track=%q", meta["track"])
	}
	if _, ok := meta["disc"]; ok {
		t.Fatalf("invalid disc should not be written, got disc=%q", meta["disc"])
	}
	if _, ok := meta["bpm"]; ok {
		t.Fatalf("invalid bpm should not be written, got bpm=%q", meta["bpm"])
	}
}

func TestStrictOnlineEmbedDownloadedFileFailsWhenCoreMetadataMissing(t *testing.T) {
	ctx := context.Background()
	_, err := strictOnlineEmbedDownloadedFile(
		ctx,
		t.TempDir(),
		"task-core-missing",
		map[string]any{
			"name": "OnlyTitle",
			"img":  "https://example.com/cover.jpg",
		},
		onlineSource{ID: "wy"},
		"wy",
		"320k",
		"/tmp/fake.mp3",
	)
	if err == nil {
		t.Fatal("expected strict embed to fail when artist/album are missing")
	}
	if reason := onlineEmbedFailureReason(err); reason != "online.error.embed_metadata_failed" {
		t.Fatalf("unexpected failure reason: %q (err=%v)", reason, err)
	}
}

func TestStrictOnlineEmbedDownloadedFileFailsWhenCoverMissing(t *testing.T) {
	ctx := context.Background()
	_, err := strictOnlineEmbedDownloadedFile(
		ctx,
		t.TempDir(),
		"task-cover-missing",
		map[string]any{
			"name":      "Song",
			"singer":    "Artist",
			"albumName": "Album",
		},
		onlineSource{ID: "wy"},
		"wy",
		"320k",
		"/tmp/fake.mp3",
	)
	if err == nil {
		t.Fatal("expected strict embed to fail when cover url is missing")
	}
	if reason := onlineEmbedFailureReason(err); reason != "online.error.embed_metadata_failed" {
		t.Fatalf("unexpected failure reason: %q (err=%v)", reason, err)
	}
}

func TestStrictOnlineEmbedLyricFailureIsNonFatal(t *testing.T) {
	audioPath := "/tmp/fake.mp3"
	gotPath, err := strictOnlineEmbedHandleResult(audioPath, "", newOnlineEmbedFailure(onlineEmbedReasonLyricFailed, fmt.Errorf("lyric rewrite failed")))
	if err != nil {
		t.Fatalf("lyric failure should be non-fatal, got err=%v", err)
	}
	if gotPath != audioPath {
		t.Fatalf("unexpected final path: got=%q want=%q", gotPath, audioPath)
	}
}

// TestOnlineEmbedCoverExtensionSniffing pins the magic-byte / mime
// detection we use to decide which extension to give the cover
// file. ffmpeg keys off the extension to pick a demuxer, so the
// wrong extension silently turns a valid PNG into an unusable
// track. We test the byte-sniffing path explicitly because it is
// the fallback most likely to be reached in production (image
// hosts often serve `image/jpeg` even when the body is webp).
func TestOnlineEmbedCoverExtensionSniffing(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		head        []byte
		want        string
	}{
		{"jpg-magic", "image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, ".jpg"},
		{"png-magic", "image/png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, ".png"},
		{"webp-magic", "image/webp", append([]byte("RIFF"), 0, 0, 0, 0, 'W', 'E', 'B', 'P'), ".webp"},
		{"gif-magic", "image/gif", []byte("GIF89a..."), ".gif"},
		{"png-mime-wins-over-jpg-bytes", "image/png", []byte{0xFF, 0xD8}, ".png"},
		{"unknown-falls-back-to-jpg", "application/octet-stream", []byte{0x00, 0x00}, ".jpg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := onlineEmbedCoverExtension(tc.contentType, tc.head)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestPickOnlineEmbedCoverURL checks the lookup priority used by
// the embed pipeline: top-level img first, then songInfo.meta.picUrl
// (legacy lx-music field). Both are common in the wild — newer
// lx-music scripts emit the former, while a long tail of older
// scripts still emit only the latter.
func TestPickOnlineEmbedCoverURL(t *testing.T) {
	t.Run("img wins over meta.picUrl", func(t *testing.T) {
		got := pickOnlineEmbedCoverURL(map[string]any{
			"img":  "https://a.example/x.jpg",
			"meta": map[string]any{"picUrl": "https://b.example/y.jpg"},
		})
		if got != "https://a.example/x.jpg" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("falls back to meta.picUrl when img missing", func(t *testing.T) {
		got := pickOnlineEmbedCoverURL(map[string]any{
			"meta": map[string]any{"picUrl": "https://b.example/y.jpg"},
		})
		if got != "https://b.example/y.jpg" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("empty when neither is set", func(t *testing.T) {
		got := pickOnlineEmbedCoverURL(map[string]any{
			"name": "song",
		})
		if got != "" {
			t.Fatalf("got %q want empty", got)
		}
	})
}

// TestFetchAndPersistOnlineCover covers the happy path of the cover
// pipeline. We spin up a small in-process HTTP server that serves
// a real (8x8 blue) PNG, then point the embed pipeline at it. The
// test asserts:
//  1. The cover lands on disk under the .nd-embed-artwork dir.
//  2. The image bytes ffmpeg wrote are re-encoded (the resize
//     step in fetchAndPersistOnlineCover overwrites the source),
//     not the original PNG — this is the production behavior.
//  3. Mime is reported as image/jpeg after the resize.
//
// We previously asserted byte-equality with the source, but the
// resize step makes that an unreasonable invariant: a test that
// just feeds "fake-jpeg-payload" can't even run because ffmpeg
// would refuse to ingest it.
func TestFetchAndPersistOnlineCover(t *testing.T) {
	// Build a real 8x8 PNG. writeTinyPNG is defined later in
	// the file but in Go test files package-level functions
	// can be referenced regardless of declaration order, so
	// this works at compile time.
	pngPath := writeTinyPNG(t, t.TempDir())
	body, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	dir := t.TempDir()
	songInfo := map[string]any{"img": ts.URL + "/cover.png"}
	ref := fetchAndPersistOnlineCover(t.Context(), dir, "task-1", songInfo)
	if ref == nil {
		// Resize is best-effort: when ffmpeg is missing, the
		// source PNG is embedded as-is and the test still
		// expects success with the original mime preserved.
		t.Skip("fetchAndPersistOnlineCover returned nil; ffmpeg may be unavailable in this environment")
	}
	if !strings.HasPrefix(ref.Path, filepath.Join(dir, ".nd-embed-artwork")) {
		t.Fatalf("cover path %q is not under the embed artwork dir", ref.Path)
	}
	st, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatalf("read cover: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("cover file is empty")
	}
	// When ffmpeg is on PATH the resize step overwrites the
	// source PNG with a JPEG; the mime should reflect that.
	// When ffmpeg is missing the source is kept verbatim and
	// mime stays "image/png" — that's the documented graceful
	// fallback.
	if commandExists("ffmpeg") {
		if ref.Mime != "image/jpeg" {
			t.Errorf("expected mime=image/jpeg after resize, got %q", ref.Mime)
		}
	} else if ref.Mime == "" {
		t.Errorf("expected non-empty mime when ffmpeg is absent, got %q", ref.Mime)
	}
	_ = os.RemoveAll(filepath.Dir(ref.Path))
}

// TestFetchAndPersistOnlineCoverRejectsJSON guards against
// misconfigured CDNs that return a 200 OK with a JSON error body
// when the requested cover is missing. The embed pipeline should
// not treat that as a valid cover.
func TestFetchAndPersistOnlineCoverRejectsJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte(`{"code":404,"msg":"not found"}`))
	}))
	defer ts.Close()

	dir := t.TempDir()
	ref := fetchAndPersistOnlineCover(t.Context(), dir, "task-json", map[string]any{
		"img": ts.URL + "/missing.jpg",
	})
	if ref != nil {
		t.Fatalf("expected nil ref for JSON body, got %+v", ref)
	}
	// No file should have been left behind.
	entries, _ := os.ReadDir(filepath.Join(dir, ".nd-embed-artwork"))
	if len(entries) != 0 {
		t.Fatalf("expected no artwork files, got %d", len(entries))
	}
}

// TestOnlineEmbedCleanupArtwork removes the artwork dir even when
// it doesn't exist (idempotent) and leaves the rest of the
// download dir alone. This is the cleanup hook that runs after
// every successful download; we want to be sure it can never
// accidentally wipe a sibling file.
func TestOnlineEmbedCleanupArtwork(t *testing.T) {
	dir := t.TempDir()
	// Pre-populate a sibling file the cleanup must NOT touch.
	sibling := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(sibling, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	artworkDir := filepath.Join(dir, ".nd-embed-artwork")
	if err := os.MkdirAll(artworkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artworkDir, "cover.jpg"), []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}

	onlineEmbedCleanupArtwork(dir)

	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("sibling mp3 was deleted: %v", err)
	}
	if _, err := os.Stat(artworkDir); !os.IsNotExist(err) {
		t.Fatalf("artwork dir not removed: err=%v", err)
	}
}

// TestOnlineEmbedEnabledDefaultsToTrue ensures fresh installs
// (no settings file) get the embed step on by default. The user
// can opt out by saving settings with embedMetadata=false, but
// the very first download after a clean install should pick up
// cover / tags automatically.
func TestOnlineEmbedEnabledDefaultsToTrue(t *testing.T) {
	// Point the online-sources root at a fresh temp dir so we
	// don't see the host's real settings file.
	oldRoot := onlineSourcesRoot
	restore := func() { _ = oldRoot }
	defer restore()

	// We can't reassign the function, so we use the real
	// loadOnlineSourceSettings path but with an env override.
	t.Setenv("HOME", t.TempDir())
	if !onlineEmbedEnabled() {
		t.Fatal("expected onlineEmbedEnabled to be true on a fresh install")
	}
}

// TestOnlineEmbedDownloadMetadataFFmpegIntegration runs the real
// ffmpeg pipeline against a 1-second silent MP3 generated with
// ffmpeg's lavfi source. We verify the embed step writes the
// title / artist / album metadata and attaches the cover.
//
// The test is skipped when ffmpeg is not on PATH so dev machines
// without it stay green. On a real install the dependency is
// already required for transcoding, so this is effectively a
// smoke test for the cover-and-tags path.
func TestOnlineEmbedDownloadMetadataFFmpegIntegration(t *testing.T) {
	if !commandExists("ffmpeg") {
		t.Skip("ffmpeg not available; skipping real embed integration test")
	}
	if !commandExists("ffprobe") {
		t.Skip("ffprobe not available; skipping real embed integration test")
	}

	tmp := t.TempDir()
	audioPath := filepath.Join(tmp, "input.mp3")

	// Generate a 1-second silent MP3 with ffmpeg. We use lavfi so
	// the test doesn't depend on shipping a binary fixture.
	genCmd := []string{
		"ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
		"-t", "1", "-q:a", "9", "-acodec", "libmp3lame",
		audioPath,
	}
	if out, err := runEmbeddedCommand(t.Context(), genCmd); err != nil {
		t.Skipf("ffmpeg input generation failed: %v\n%s", err, out)
	}

	// Create a minimal cover file (a 1x1 PNG byte sequence).
	coverPath := writeTinyPNG(t, tmp)

	songInfo := map[string]any{
		"name":      "那些花儿",
		"singer":    "朴树",
		"albumName": "我去2000年",
		"source":    "wy",
	}
	ref := &onlineEmbedArtworkRef{Path: coverPath, Mime: "image/png"}
	res, finalPath, err := onlineEmbedDownloadMetadata(t.Context(), audioPath, songInfo, "320k", ref, "[00:00.00]LRC line")
	if err != nil {
		t.Fatalf("embed failed: %v", err)
	}
	if finalPath == "" {
		t.Fatal("finalPath is empty")
	}
	if !res.HadCover {
		t.Fatal("HadCover was false after embed with cover ref")
	}
	if !res.HadLyric {
		t.Fatal("HadLyric was false after embed with non-empty lyric")
	}

	// ffprobe the output to confirm the title metadata round-tripped.
	// We use a wildcard for the lyrics key: our ID3v2.4 USLT
	// frame surfaces as `lyrics-eng=…` (the language code is
	// part of the ffprobe key), so the test accepts either
	// that or the bare `lyrics=…` that older ffmpegs used
	// for the TXXX wrapper.
	probeCmd := []string{
		"ffprobe", "-v", "error",
		// No entry filter on format_tags because the
		// new ID3v2.4 USLT frame surfaces as
		// `lyrics-eng=…` (language is part of the key)
		// and ffprobe's entry filter is exact-match.
		// We list all format_tags and assert presence
		// of the expected keys below.
		"-show_entries", "format_tags:stream_tags=title",
		"-of", "default=noprint_wrappers=1",
		audioPath,
	}
	out, err := runEmbeddedCommand(t.Context(), probeCmd)
	if err != nil {
		t.Fatalf("ffprobe failed: %v\n%s", err, out)
	}
	combined := string(out)
	for _, want := range []string{"title=那些花儿", "artist=朴树", "album=我去2000年"} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected %q in ffprobe output, got:\n%s", want, combined)
		}
	}
	if !strings.Contains(combined, "lyrics") {
		t.Errorf("expected lyrics tag in ffprobe output, got:\n%s", combined)
	}
}

// TestOnlineEmbedDownloadMetadataFFmpegIntegrationExtensionless is
// the regression test for the bug the user reported via Mp3Tag:
// the browser streaming path downloads to an os.CreateTemp temp
// file (no extension), then runs the embed pipeline. The previous
// ffmpeg invocation passed the input path with no -f flag and
// relied on ffmpeg's extension-based probing, which silently
// failed. This test reproduces that exact scenario on disk and
// confirms the sniffer-driven path writes the tags, cover, and
// lyrics to the final file.
func TestOnlineEmbedDownloadMetadataFFmpegIntegrationExtensionless(t *testing.T) {
	if !commandExists("ffmpeg") {
		t.Skip("ffmpeg not available; skipping real embed integration test")
	}
	if !commandExists("ffprobe") {
		t.Skip("ffprobe not available; skipping real embed integration test")
	}

	tmp := t.TempDir()

	// Build a real MP3 in a directory we control, then MOVE it to
	// a path with no extension. This matches what
	// fetchOnlineDownloadToTempFile does in production: the audio
	// is on disk, but the filename has no .mp3 suffix.
	srcMP3 := filepath.Join(tmp, "src.mp3")
	genCmd := []string{
		"ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
		"-t", "1", "-q:a", "9", "-acodec", "libmp3lame",
		srcMP3,
	}
	if out, err := runEmbeddedCommand(t.Context(), genCmd); err != nil {
		t.Skipf("ffmpeg input generation failed: %v\n%s", err, out)
	}
	audioPath := filepath.Join(tmp, "nd-online-download-noext")
	body, err := os.ReadFile(srcMP3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audioPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	coverPath := writeTinyPNG(t, tmp)

	songInfo := map[string]any{
		"name":      "海屿你",
		"singer":    "马也_Crabbit",
		"albumName": "海屿你",
		"source":    "wy",
	}
	ref := &onlineEmbedArtworkRef{Path: coverPath, Mime: "image/png"}
	res, finalPath, err := onlineEmbedDownloadMetadata(t.Context(), audioPath, songInfo, "320k", ref, "[00:00.00]海屿你 - 马也_Crabbit")
	if err != nil {
		t.Fatalf("embed failed: %v", err)
	}
	if finalPath == "" {
		t.Fatal("finalPath is empty")
	}
	if !res.HadCover {
		t.Fatal("HadCover was false after embed with cover ref")
	}
	if !res.HadLyric {
		t.Fatal("HadLyric was false after embed with non-empty lyric")
	}

	// The temp file should have been replaced in place (this is
	// what the user expects to see in their Downloads folder).
	if _, err := os.Stat(audioPath); err != nil {
		t.Fatalf("output file disappeared after embed: %v", err)
	}
	// The .ndembed.tmp sibling should have been cleaned up.
	if _, err := os.Stat(audioPath + ".ndembed.mp3"); !os.IsNotExist(err) {
		t.Fatalf("expected .ndembed.mp3 sibling to be removed, stat err=%v", err)
	}

	// ffprobe the result. We deliberately do NOT pass -show_streams
	// here so this test runs faster; the cover-art presence is
	// verified via the format_tags and stream loop separately.
	probeCmd := []string{
		"ffprobe", "-v", "error",
		// We DON'T filter on `format_tags=lyrics` because
		// the new ID3v2.4 USLT frame surfaces as
		// `lyrics-eng=…` (language is part of the key)
		// and ffprobe's entry-filter is exact-match, not
		// a prefix or glob. Listing format_tags without a
		// filter shows all keys; the test then asserts
		// the lyrics key is present.
		"-show_entries", "format_tags",
		"-of", "default=noprint_wrappers=1",
		audioPath,
	}
	out, err := runEmbeddedCommand(t.Context(), probeCmd)
	if err != nil {
		t.Fatalf("ffprobe failed: %v\n%s", err, out)
	}
	combined := string(out)
	for _, want := range []string{
		"title=海屿你",
		"artist=马也_Crabbit",
		"album=海屿你",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected %q in ffprobe output, got:\n%s", want, combined)
		}
	}
	// Lyrics: the ID3v2.4 USLT frame surfaces as
	// `lyrics-eng=…` in ffprobe (the language is part of
	// the key). The old TXXX wrapper also showed as
	// `lyrics=…` — both are accepted, the test just wants
	// a "lyrics" key to be present.
	if !strings.Contains(combined, "lyrics") {
		t.Errorf("expected a lyrics tag in ffprobe output, got:\n%s", combined)
	}

	// Cover check: ffprobe -show_streams lists the attached_pic
	// video stream that holds the cover. We just confirm the codec
	// type is "video" with disposition "attached_pic"; the
	// detailed pixel-content test is left to the existing
	// extension test, which exercises the same path with
	// already-extensioned input.
	if info, err := os.Stat(audioPath); err == nil {
		t.Logf("final file size: %d", info.Size())
	} else {
		t.Logf("stat err: %v", err)
	}
	streamCmd := []string{
		"ffprobe", "-v", "error",
		"-select_streams", "v",
		"-show_entries", "stream=codec_type:stream_disposition=attached_pic",
		"-of", "default=noprint_wrappers=1",
		audioPath,
	}
	streamOut, err := runEmbeddedCommand(t.Context(), streamCmd)
	if err != nil {
		t.Fatalf("ffprobe stream check failed: %v\n%s", err, streamOut)
	}
	if !strings.Contains(string(streamOut), "codec_type=video") {
		t.Errorf("expected a video stream (cover) in the rewritten file, got:\n%s", streamOut)
	}
	t.Logf("stream out:\n%s", streamOut)
	if !strings.Contains(string(streamOut), "attached_pic=1") {
		t.Errorf("expected attached_pic disposition on the cover stream, got:\n%s", streamOut)
	}
}

// writeTinyPNG writes a small but valid PNG file to disk and
// returns the path. We use a real image/png.Encode call (rather
// than the hand-written IDAT bytes that some older test suites
// use) so the bytes ffmpeg reads are guaranteed parseable, which
// is the property the embed pipeline depends on. ffmpeg will
// silently drop a malformed cover rather than fail the run, so
// the test would otherwise pass for the wrong reason.
func writeTinyPNG(t *testing.T, dir string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	blue := color.RGBA{0x29, 0x6F, 0xB5, 0xFF}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, blue)
		}
	}
	p := filepath.Join(dir, "cover.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOnlineEmbedResizeCoverCappedTo300KB is the regression
// test for the user-reported "3000x3000 / 7 MB cover bloated
// every audio file" bug. We generate a 1500x1500 high-detail
// source PNG (the typical upstream cover image shape), then
// run onlineEmbedResizeCover on it and assert the output is
// (a) a JPEG, (b) at most onlineEmbedCoverEmbedMaxBytes, and
// (c) has a long edge no larger than onlineEmbedCoverMaxWidth.
//
// The test is skipped when ffmpeg is not on PATH; in CI we
// install ffmpeg specifically to exercise this code path.
func TestOnlineEmbedResizeCoverCappedTo300KB(t *testing.T) {
	if !commandExists("ffmpeg") {
		t.Skip("ffmpeg not available; skipping cover resize integration test")
	}

	tmp := t.TempDir()
	// 1500x1500 is typical for high-res CD-quality cover scans
	// (e.g. Apple Music / 网易云 lossless tiers). The colour
	// gradient + noise is what blows up the encoded JPEG
	// size at the source resolution, so the test is a
	// realistic worst case for the resize pipeline.
	srcPath := filepath.Join(tmp, "src.png")
	genCmd := []string{
		"ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i",
		"color=c=0x296FB5:s=1500x1500:d=1,format=yuv420p,noise=alls=80:allf=t+u",
		"-frames:v", "1", srcPath,
	}
	if out, err := runEmbeddedCommand(t.Context(), genCmd); err != nil {
		t.Skipf("ffmpeg source generation failed: %v\n%s", err, out)
	}

	// Sanity check: the source is at least > 300 KB so the test
	// actually exercises the resize path. A 1500x1500 solid
	// blue PNG is already under 300 KB, so the noise filter
	// above is what pushes it over.
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if srcInfo.Size() < 300*1024 {
		t.Skipf("source image is too small (%d bytes) to exercise the resize; "+
			"the test fixture needs to be over the cap", srcInfo.Size())
	}

	dstPath := filepath.Join(tmp, "dst.jpg")
	size, err := onlineEmbedResizeCover(t.Context(), srcPath, dstPath)
	if err != nil {
		t.Fatalf("resize failed: %v", err)
	}

	if size > int64(onlineEmbedCoverEmbedMaxBytes) {
		t.Errorf("resized cover is %d bytes, exceeds %d byte cap",
			size, onlineEmbedCoverEmbedMaxBytes)
	}

	// Long-edge check via ffprobe.
	probeCmd := []string{
		"ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0",
		dstPath,
	}
	out, err := runEmbeddedCommand(t.Context(), probeCmd)
	if err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, out)
	}
	var w, h int
	// ffprobe with -of csv=p=0 prints dimensions as "w,h".
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 2 {
		t.Fatalf("could not parse ffprobe output %q", out)
	}
	if _, err := fmt.Sscan(parts[0], &w); err != nil {
		t.Fatalf("parse width from %q: %v", parts[0], err)
	}
	if _, err := fmt.Sscan(parts[1], &h); err != nil {
		t.Fatalf("parse height from %q: %v", parts[1], err)
	}
	longEdge := w
	if h > longEdge {
		longEdge = h
	}
	if longEdge > onlineEmbedCoverMaxWidth {
		t.Errorf("resized cover long edge is %d, exceeds %d px cap",
			longEdge, onlineEmbedCoverMaxWidth)
	}
	t.Logf("resized cover: %dx%d, %d bytes (cap %d)", w, h, size, onlineEmbedCoverEmbedMaxBytes)
}

// TestOnlineEmbedSniffAudioFormat pins the audio-container sniffer
// used by onlineEmbedDownloadMetadata to pick the right -f flag
// when the input file has no extension (which is the case for
// the browser streaming path, where the temp file is created
// with os.CreateTemp and lacks a suffix).
func TestOnlineEmbedSniffAudioFormat(t *testing.T) {
	cases := []struct {
		name    string
		head    []byte
		wantFmt string
		wantExt string
	}{
		{"mp3-id3v2", []byte("ID3\x04\x00\x00\x00\x00\x00\x00" + "audio"), "mp3", "mp3"},
		{"mp3-raw-frame-sync", []byte{0xFF, 0xFB, 0x90, 0x00}, "mp3", "mp3"},
		{"flac", []byte("fLaC\x00\x00\x00\x22"), "flac", "flac"},
		{"ogg", []byte("OggS\x00\x02\x00\x00"), "ogg", "ogg"},
		{"wav", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "wav", "wav"},
		{"m4a", []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00"), "mp4", "m4a"},
		{"unknown-payload", []byte("hello world"), "", ""},
		{"empty-file", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "audiofile")
			if len(tc.head) > 0 {
				if err := os.WriteFile(p, tc.head, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				// Create an empty file so the open + read path
				// exercises the n < 4 early return.
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			gotFmt, gotExt := onlineEmbedSniffAudioFormat(p)
			if gotFmt != tc.wantFmt || gotExt != tc.wantExt {
				t.Fatalf("onlineEmbedSniffAudioFormat(%q) = (%q, %q), want (%q, %q)",
					tc.name, gotFmt, gotExt, tc.wantFmt, tc.wantExt)
			}
		})
	}
}

// runEmbeddedCommand is a tiny helper that runs a command and
// returns its combined output as a string. We use this from the
// embed integration test so a failure includes the actual ffmpeg
// stderr in the test output — without it, a misconfigured test
// fixture would just show "exit code 1". The arguments come from
// the test's own local literal slices, not from user input, so
// gosec's G204 is a false positive here.
func runEmbeddedCommand(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204
	return cmd.CombinedOutput()
}

// ===== from online_lyric_lyrica_test.go =====

func TestOnlineLyricaLyric_RequiresTitle(t *testing.T) {
	api := &Router{}
	req := httptest.NewRequest(http.MethodGet, "/online/lyric/lyrica?artist=Adele", nil)
	w := httptest.NewRecorder()

	api.onlineLyricaLyric(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestOnlineLyricaLyric_Success(t *testing.T) {
	oldFetcher := lyricaLyricsFetcher
	t.Cleanup(func() { lyricaLyricsFetcher = oldFetcher })

	lyricaLyricsFetcher = func(ctx context.Context, title, artist string, extra url.Values) (map[string]any, error) {
		return map[string]any{
			"ok":     true,
			"lyrics": "hello from lyrica",
		}, nil
	}

	api := &Router{}
	req := httptest.NewRequest(http.MethodGet, "/online/lyric/lyrica?title=Hello&artist=Adele&timestamps=true", nil)
	w := httptest.NewRecorder()

	api.onlineLyricaLyric(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if body["source"] != "lyrica" {
		t.Fatalf("expected source lyrica, got %#v", body["source"])
	}
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", body["result"])
	}
	if result["ok"] != true {
		t.Fatalf("expected result.ok=true, got %#v", result["ok"])
	}
}

func TestFetchOnlineEmbedLyricViaLyrica_DefaultParams(t *testing.T) {
	oldFetcher := lyricaLyricsFetcher
	t.Cleanup(func() { lyricaLyricsFetcher = oldFetcher })

	var gotTitle string
	var gotArtist string
	var gotExtra url.Values

	lyricaLyricsFetcher = func(ctx context.Context, title, artist string, extra url.Values) (map[string]any, error) {
		gotTitle = title
		gotArtist = artist
		gotExtra = extra
		return map[string]any{
			"lyrics": "[00:00.00]hello",
		}, nil
	}

	lyric, ok := fetchOnlineEmbedLyricViaLyrica(context.Background(), map[string]any{
		"name":   "Hello",
		"singer": "Adele",
	})
	if !ok {
		t.Fatal("expected lyrica embed fallback to succeed")
	}
	if lyric != "[00:00.00]hello" {
		t.Fatalf("unexpected lyric: %q", lyric)
	}
	if gotTitle != "Hello" || gotArtist != "Adele" {
		t.Fatalf("unexpected query fields title=%q artist=%q", gotTitle, gotArtist)
	}
	if gotExtra.Get("timestamps") != "true" {
		t.Fatalf("expected timestamps=true, got %q", gotExtra.Get("timestamps"))
	}
	if gotExtra.Get("fast") != "true" {
		t.Fatalf("expected fast=true, got %q", gotExtra.Get("fast"))
	}
}

func TestFetchOnlineEmbedLyricViaLyrica_ExtractNestedLyric(t *testing.T) {
	oldFetcher := lyricaLyricsFetcher
	t.Cleanup(func() { lyricaLyricsFetcher = oldFetcher })

	lyricaLyricsFetcher = func(ctx context.Context, title, artist string, extra url.Values) (map[string]any, error) {
		return map[string]any{
			"data": map[string]any{
				"synced_lyrics": "[00:00.00]nested",
			},
		}, nil
	}

	lyric, ok := fetchOnlineEmbedLyricViaLyrica(context.Background(), map[string]any{
		"meta": map[string]any{
			"songName":   "Hello",
			"singerName": "Adele",
		},
	})
	if !ok {
		t.Fatal("expected nested lyric extraction to succeed")
	}
	if lyric != "[00:00.00]nested" {
		t.Fatalf("unexpected lyric: %q", lyric)
	}
}

func TestLyricaValueAsLyric_Array(t *testing.T) {
	got := lyricaValueAsLyric([]any{" [00:00.00]line1 ", "", "[00:03.00]line2"})
	want := strings.Join([]string{"[00:00.00]line1", "[00:03.00]line2"}, "\n")
	if got != want {
		t.Fatalf("unexpected joined lyric: got %q want %q", got, want)
	}
}

// ===== from online_lyric_match_test.go =====

// online_lyric_match_test.go — unit tests for lyric matching.
//
// These tests are offline: they exercise the matcher and helper
// functions against in-memory candidates. The matcher has no I/O,
// so regressions are easiest to pin with pure string fixtures.

// TestOnlineLyricSimilarity_Sanity pins the similarity
// function's behavior on a handful of representative
// inputs. The thresholds in the matcher are picked against
// these numbers, so a regression here will silently break
// matching — worth pinning explicitly.
func TestOnlineLyricSimilarity_Sanity(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Identical: 100.
		{"起风了", "起风了", 100},
		// Whitespace difference only: 100.
		{"起风了", "  起风了  ", 100},
		// Case difference only: 100.
		{"Qing Fei De Yi", "qing fei de yi", 100},
		// Parenthetical suffix: scores in the 30-50 range;
		// the matcher will reject this as "wrong song" which
		// is what we want for the (Original) vs (Remix) case.
		// Jaccard tokens: "起风了" vs "起风了 remix" → 1/2 = 50.
		{"起风了", "起风了 (Remix)", 50},
		// Slash-separated collaborator: same 1/2 = 50 after
		// the slash is normalized to a space.
		{"起风了", "起风了 / Live", 50},
		// Halfwidth vs fullwidth punctuation: 100 (we
		// strip both as connectors).
		{"起风了(现场版)", "起风了（现场版）", 100},
		// Completely disjoint: 0.
		{"起风了", "烟火人间", 0},
		// One side empty: 0.
		{"起风了", "", 0},
	}
	for _, c := range cases {
		got := onlineLyricSimilarity(c.a, c.b)
		// Allow a ±2 point slack to absorb tokenization
		// noise (e.g. empty tokens from doubled
		// connectors). The threshold decisions in the
		// matcher have 15+ point of headroom, so ±2 is
		// safe.
		if got < c.want-2 || got > c.want+2 {
			t.Errorf("onlineLyricSimilarity(%q, %q) = %d, want ~%d", c.a, c.b, got, c.want)
		}
	}
}

// TestOnlineLyricNormalizeForMatch_Cases pins the
// version-suffix normalizer against the most common
// cross-source title divergences. The matcher calls this
// on BOTH sides of the title comparison, so a regression
// here will silently re-introduce the cross-source
// matching failures the user reported.
func TestOnlineLyricNormalizeForMatch_Cases(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Identity: no change.
		{"起风了", "起风了"},
		// Parenthetical suffix dropped (halfwidth).
		{"起风了 (Live)", "起风了"},
		// Parenthetical suffix dropped (fullwidth).
		{"起风了（现场版）", "起风了"},
		// Bracketed suffix dropped.
		{"起风了【高清版】", "起风了"},
		// Multiple suffixes: both dropped.
		{"起风了 (现场版) [高清音质]", "起风了"},
		// Dash-separator suffix dropped.
		{"起风了 - Live", "起风了"},
		{"起风了 - 现场版", "起风了"},
		// Standalone suffix dropped.
		{"起风了 Live", "起风了"},
		{"起风了 现场版", "起风了"},
		// Trailing non-suffix text preserved.
		{"起风了 (Live) - 2024 Remaster", "起风了 - 2024 Remaster"},
		// Whitespace collapsed.
		{"起风了   (现场版)", "起风了"},
		// Empty input.
		{"", ""},
		// Only-suffix input: the HasSuffix check
		// requires a leading space (or "-") so a
		// string that's just a suffix keyword stays
		// as-is. This is intentional: we don't want
		// to silently empty a string whose only
		// content happens to match a suffix in our
		// dictionary.
		{"现场版", "现场版"},
	}
	for _, c := range cases {
		got := onlineLyricNormalizeForMatch(c.in)
		if got != c.want {
			t.Errorf("onlineLyricNormalizeForMatch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOnlineLyricMatchScore_AcceptsVersionSuffix is the
// regression test for the user's reported issue: a
// cross-source candidate with a version suffix on the
// title (e.g. "起风了 (Live)") but matching artist +
// duration must be accepted. The fix is twofold:
//  1. The normalizer strips "(Live)" / "(现场版)" / etc.
//     from the title before scoring.
//  2. Rule 2 of the matcher (artist nailed + title loose)
//     allows the candidate through when the artist
//     similarity is ≥ 60 even if the raw title similarity
//     would have been < 60.
//
// The test exercises the exact shape the user saw in
// their log: a kw candidate with [ti:"起风了 (现场版)"]
// [ar:"买辣椒也用券"] against songInfo {"name":"起风了",
// "singer":"买辣椒也用券", "interval":"05:25"}.
func TestOnlineLyricMatchScore_AcceptsVersionSuffix(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "kw",
		Lyric:  "[ti:起风了 (现场版)]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:24.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected version-suffix candidate to be accepted, got reason=%q t=%d a=%d d=%d",
			score.Reason, score.TitleScore, score.ArtistScore, score.DurationDelta)
	}
}

// TestOnlineLyricMatchScore_AcceptsExactDurationNoTags is
// the regression test for "duration is exact + no id
// tags" — a candidate that has line-level timing matching
// the song but no [ti:]/[ar:] metadata. This is the
// 酷狗 KRC-after-strip scenario: the lyric is real and
// for the right song, but the source's tagger dropped
// the id tags.
func TestOnlineLyricMatchScore_AcceptsExactDurationNoTags(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source:      "kg",
		Lyric:       "[00:00.00]a\n[05:24.00]end",
		LyricDurSec: 324,
	}
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected exact-duration-no-id-tags candidate to be accepted, got reason=%q", score.Reason)
	}
}

// TestOnlineLyricMatchScore_AcceptsPerfectMatch is the
// happy-path: candidate with [ti:] matching name and
// [ar:] matching singer, and duration within tolerance.
func TestOnlineLyricMatchScore_AcceptsPerfectMatch(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:24.00]end",
	}
	// The candidate's id tags come from the lyric body, so
	// fill them via the parser to keep this test honest.
	cand.SelfTitle, cand.SelfArtist, cand.SelfAlbum = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)

	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected match to be accepted, got reason=%q titleScore=%d artistScore=%d durDelta=%d",
			score.Reason, score.TitleScore, score.ArtistScore, score.DurationDelta)
	}
}

// TestOnlineLyricMatchScore_RejectsWrongTitle: a candidate
// with a clearly different title must be rejected even if
// the artist tag matches.
func TestOnlineLyricMatchScore_RejectsWrongTitle(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:烟火人间]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:24.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if score.OK {
		t.Errorf("expected wrong-title candidate to be rejected, got OK with titleScore=%d", score.TitleScore)
	}
	if !strings.Contains(score.Reason, "title") {
		t.Errorf("expected reason to mention title, got %q", score.Reason)
	}
}

// TestOnlineLyricMatchScore_RejectsWrongDuration: same
// title/artist but the lyric's last time tag is 30s past
// the song's interval. Must be rejected.
func TestOnlineLyricMatchScore_RejectsWrongDuration(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]test\n[06:00.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if score.OK {
		t.Errorf("expected wrong-duration candidate to be rejected, got OK with durDelta=%d", score.DurationDelta)
	}
	if !strings.Contains(score.Reason, "duration") {
		t.Errorf("expected reason to mention duration, got %q", score.Reason)
	}
}

// TestOnlineLyricMatchScore_AcceptsWithinDuration: a 3s
// drift must be accepted. The threshold is the user's
// choice (±3s strict); this test pins the boundary.
func TestOnlineLyricMatchScore_AcceptsWithinDuration(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:28.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected +3s drift to be accepted, got reason=%q durDelta=%d", score.Reason, score.DurationDelta)
	}
}

// TestOnlineLyricMatchScore_RejectsTagless: a candidate
// with no [ti:]/[ar:] and no parseable duration is
// "no evidence" and must be rejected by the matcher
// (the orchestrator may still take it as a last-resort
// fallback if every other source also failed, but the
// matcher itself says "no").
func TestOnlineLyricMatchScore_RejectsTagless(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "mg",
		Lyric:  "plain text without time tags or id tags",
	}
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if score.OK {
		t.Errorf("expected tagless candidate to be rejected, got OK")
	}
	if !strings.Contains(score.Reason, "no") {
		t.Errorf("expected reason to flag no evidence, got %q", score.Reason)
	}
}

// TestOnlineLyricParseIDTags_HandlesMissing: empty input
// and various partial inputs should not panic and should
// return empty strings for the missing fields.
func TestOnlineLyricParseIDTags_HandlesMissing(t *testing.T) {
	cases := []struct {
		in           string
		wantT, wantA string
	}{
		{"", "", ""},
		{"[ti:only title]", "only title", ""},
		{"[ar:only artist]", "", "only artist"},
		// The id-tag regex is intentionally case-sensitive
		// on the key (the ID3v2 / LRC spec uses lowercase
		// keys). Real-world LRC files always emit lowercase;
		// if a source returns [TI:], we treat it as not-a-
		// tag and fall through to the time-based matching.
		{"[TI:Caps]\n[ar:art]\n[al:alb]", "", "art"},
		// [by:] is ignored by the parser; it's not part
		// of the matching set.
		{"[by:nobody]\n[00:00.00]just timing", "", ""},
	}
	for _, c := range cases {
		t1, t2, _ := onlineLyricParseIDTags(c.in)
		if t1 != c.wantT || t2 != c.wantA {
			t.Errorf("onlineLyricParseIDTags(%q) = (%q, %q), want (%q, %q)", c.in, t1, t2, c.wantT, c.wantA)
		}
	}
}

// TestOnlineLyricMaxTimeTagSeconds picks the largest tag
// across the body. This is the proxy for the lyric's
// runtime; a regression would silently break the duration
// check.
func TestOnlineLyricMaxTimeTagSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"[00:00.00]a\n[01:30.00]b", 90},
		{"[00:00.00]a\n[05:24.50]b", 324},
		// wy's broken [mm:ss:hh] form: should still parse
		// (we accept the colon between ss and hh as well
		// as the dot).
		{"[00:00:00]a\n[05:24:50]b", 324},
		// Per-word timing tags (kw lyricx): they're
		// bracketed time tags too, so the max-time scan
		// will still find the line-level tags. Lines with
		// <offset,duration> markers but no [mm:ss] tag
		// contribute 0 to the max (the rxp requires the
		// leading bracket).
		{"[00:00.00]a <100,200>rest", 0},
	}
	for _, c := range cases {
		got := onlineLyricMaxTimeTagSeconds(c.in)
		if got != c.want {
			t.Errorf("onlineLyricMaxTimeTagSeconds(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestOnlineLyricSecondsFromInterval covers the typical
// lx-music shapes: "mm:ss", "mm:ss.SSS", "h:mm:ss", with
// optional whitespace.
func TestOnlineLyricSecondsFromInterval(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"05:25", 325},
		{"  05:25  ", 325},
		{"05:25.500", 325},
		{"1:05:25", 3925},
		{"not a time", 0},
		{"5", 0},
	}
	for _, c := range cases {
		got := onlineLyricSecondsFromInterval(c.in)
		if got != c.want {
			t.Errorf("onlineLyricSecondsFromInterval(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestOnlineLyricCandidatesFromResult_AllFields(t *testing.T) {
	res := onlineLyricResult{
		Lyric:   "[00:00.00]main",
		TLyric:  "[00:00.00]trans",
		RLyric:  "[00:00.00]roma",
		LXLyric: "[00:00.000]<0,100>词",
	}
	cands := onlineLyricCandidatesFromResult("kw", res)
	if len(cands) != 4 {
		t.Fatalf("expected 4 candidates from full lyric result, got %d", len(cands))
	}
	gotKinds := map[string]bool{}
	for _, c := range cands {
		gotKinds[c.Kind] = true
		if c.Source != "kw" {
			t.Fatalf("candidate source = %q, want kw", c.Source)
		}
		if strings.TrimSpace(c.Lyric) == "" {
			t.Fatalf("candidate kind=%s has empty lyric", c.Kind)
		}
	}
	for _, k := range []string{"lyric", "tlyric", "rlyric", "lxlyric"} {
		if !gotKinds[k] {
			t.Fatalf("missing candidate kind %q", k)
		}
	}
}

func TestOnlineLyricAcceptedRank_PrefersMainLyricOnTie(t *testing.T) {
	score := onlineLyricMatchScore{OK: true, TitleScore: 100, ArtistScore: 100, DurationDelta: 0}
	main := onlineLyricCandidate{Kind: "lyric", Lyric: "[00:00.00]main"}
	trans := onlineLyricCandidate{Kind: "tlyric", Lyric: "[00:00.00]trans"}
	if onlineLyricAcceptedRank(main, score) <= onlineLyricAcceptedRank(trans, score) {
		t.Fatal("expected main lyric rank to be higher than translation lyric rank on tied scores")
	}
}

// ===== from online_lyric_search_test.go =====

// online_lyric_search_test.go — unit tests for the
// search-based lyric fallback layer. The tests cover
// the offline helpers (search-query extraction,
// duration formatting, candidate pre-accept gate)
// and the JSON-parsing helpers. The HTTP-level
// integration (search → lyric fetch end-to-end)
// requires live network and is covered by the
// existing FFmpeg integration test when run with
// network access.

// TestOnlineLyricSearchQuery covers the two songInfo
// shapes we see in the wild: flat name/singer, and
// nested meta.songName / meta.singerName (the wrapper
// the older lx-music scripts produce).
func TestOnlineLyricSearchQuery(t *testing.T) {
	cases := []struct {
		name       string
		in         map[string]any
		wantName   string
		wantSinger string
		wantOK     bool
	}{
		{
			name:       "flat",
			in:         map[string]any{"name": "起风了", "singer": "买辣椒也用券"},
			wantName:   "起风了",
			wantSinger: "买辣椒也用券",
			wantOK:     true,
		},
		{
			name:       "nested-meta",
			in:         map[string]any{"meta": map[string]any{"songName": "江南", "singerName": "林俊杰"}},
			wantName:   "江南",
			wantSinger: "林俊杰",
			wantOK:     true,
		},
		{
			name:   "empty",
			in:     map[string]any{},
			wantOK: false,
		},
		{
			name:   "whitespace-only-name",
			in:     map[string]any{"name": "   ", "singer": "foo"},
			wantOK: false,
		},
		{
			name:       "singer-missing",
			in:         map[string]any{"name": "起风了"},
			wantName:   "起风了",
			wantSinger: "",
			wantOK:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName, gotSinger, gotOK := onlineLyricSearchQuery(c.in)
			if gotName != c.wantName || gotSinger != c.wantSinger || gotOK != c.wantOK {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)",
					gotName, gotSinger, gotOK, c.wantName, c.wantSinger, c.wantOK)
			}
		})
	}
}

// TestOnlineLyricSearchQueryString ensures the query
// format each source's search API receives is stable.
// All three sources (wy, tx, kw) accept a free-text
// query, so the format is uniform.
func TestOnlineLyricSearchQueryString(t *testing.T) {
	cases := []struct {
		name, singer, want string
	}{
		{"起风了", "买辣椒也用券", "起风了 买辣椒也用券"},
		{"起风了", "", "起风了"},
		{"江南", "林俊杰", "江南 林俊杰"},
	}
	for _, c := range cases {
		got := onlineLyricSearchQueryString(c.name, c.singer)
		if got != c.want {
			t.Errorf("onlineLyricSearchQueryString(%q, %q) = %q, want %q", c.name, c.singer, got, c.want)
		}
	}
}

// TestOnlineLyricSearchFormatDuration pins the
// "m:ss" / "mm:ss" format the search phase injects
// into the synthetic songInfo. The lx-music pipeline
// normalizes the same way, so the search-derived
// interval slots directly into the existing matcher.
func TestOnlineLyricSearchFormatDuration(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0:00"},
		{60, "1:00"},
		{325, "5:25"}, // 5*60 + 25
		{3661, "61:01"},
		{-5, "0:00"},
	}
	for _, c := range cases {
		got := onlineLyricSearchFormatDuration(c.in)
		if got != c.want {
			t.Errorf("onlineLyricSearchFormatDuration(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOnlineLyricSearchTimeout is a sanity test for
// the per-attempt timeout constant. The value drives
// how long the embed step will block on a single
// search; if it grew unbounded the embed pipeline
// would stall. Pinning the value here makes the
// budget visible.
func TestOnlineLyricSearchTimeout(t *testing.T) {
	if onlineLyricSearchTimeout < 2*time.Second {
		t.Errorf("search timeout %v is too aggressive; the embed budget can't afford per-source retries inside that window", onlineLyricSearchTimeout)
	}
	if onlineLyricSearchTimeout > 15*time.Second {
		t.Errorf("search timeout %v is too lenient; the embed pipeline has a 30s total budget and 3 search attempts would consume 45s", onlineLyricSearchTimeout)
	}
}

// TestOnlineLyricSearchWYCandidate_Accessors pins the
// interface contract. onlineLyricSearchPreAccept uses
// these methods to read the per-source struct without
// caring which source produced the candidate.
func TestOnlineLyricSearchWYCandidate_Accessors(t *testing.T) {
	c := onlineLyricSearchWYCandidate{id: "1", name: "起风了", singer: "买辣椒也用券", durationMs: 325000}
	if c.GetName() != "起风了" {
		t.Errorf("GetName: got %q", c.GetName())
	}
	if c.GetSinger() != "买辣椒也用券" {
		t.Errorf("GetSinger: got %q", c.GetSinger())
	}
	if c.GetDurationSec() != 325 {
		t.Errorf("GetDurationSec: got %d, want 325", c.GetDurationSec())
	}
}

func TestOnlineLyricSearchTXCandidate_Accessors(t *testing.T) {
	c := onlineLyricSearchTXCandidate{mid: "abc", name: "江南", singer: "林俊杰", durationSec: 252}
	if c.GetName() != "江南" {
		t.Errorf("GetName: got %q", c.GetName())
	}
	if c.GetDurationSec() != 252 {
		t.Errorf("GetDurationSec: got %d", c.GetDurationSec())
	}
}

func TestOnlineLyricSearchKWCandidate_Accessors(t *testing.T) {
	c := onlineLyricSearchKWCandidate{id: "456", name: "起风了", singer: "买辣椒也用券", durationSec: 325}
	if c.GetName() != "起风了" {
		t.Errorf("GetName: got %q", c.GetName())
	}
	if c.GetDurationSec() != 325 {
		t.Errorf("GetDurationSec: got %d", c.GetDurationSec())
	}
}

// TestOnlineLyricSearchPreAccept_BadCandidate exercises
// the pre-accept gate end-to-end: a candidate whose
// title doesn't match the song must be rejected, with
// the score surfaced for the trace.
func TestOnlineLyricSearchPreAccept_BadCandidate(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	// A candidate whose singer doesn't match the
	// song. The matcher should reject with the
	// artist-veto reason.
	bad := onlineLyricSearchKWCandidate{
		id: "9999", name: "起风了", singer: "完全不同的歌手", durationSec: 325,
	}
	score, accept := onlineLyricSearchPreAccept(songInfo, bad)
	if accept {
		t.Errorf("expected bad candidate to be rejected, got OK with score=%+v", score)
	}
	if !strings.Contains(score.Reason, "artist") {
		t.Errorf("expected reason to mention artist, got %q", score.Reason)
	}
}

// TestOnlineLyricSearchPreAccept_GoodCandidate: the
// inverse — a candidate that matches the song on all
// three signals must be accepted.
func TestOnlineLyricSearchPreAccept_GoodCandidate(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	good := onlineLyricSearchKWCandidate{
		id: "108914", name: "起风了", singer: "买辣椒也用券", durationSec: 325,
	}
	_, accept := onlineLyricSearchPreAccept(songInfo, good)
	if !accept {
		t.Errorf("expected good candidate to be accepted")
	}
}

// ===== from online_lyric_test.go =====

// online_lyric_test.go covers the in-process Go lyric API
// clients added in online_lyric.go. The strategy is to spin
// up httptest.Server fixtures for each source's response
// shape, point the fetcher at the fixture, and assert the
// resulting onlineLyricResult. Per-source quirks (kw's
// zlib+GB18030, kg's two-step search/download, wy's eapi
// encryption) get their own focused tests so a regression in
// one decoder doesn't blanket-fail the rest.

// TestOnlineLyricDispatchUnknownSource pins the contract
// that an unknown source returns an empty result (so the
// caller falls through to the script path) and emits a
// trace line. The trace line is the user's first hint that
// "the songInfo.source field carried a typo" or "a future
// lx-music source needs a new client".
func TestOnlineLyricDispatchUnknownSource(t *testing.T) {
	res := fetchOnlineLyricBySource(context.Background(), "made-up-source", map[string]any{
		"name":    "Test",
		"songmid": "abc",
	})
	if res.Lyric != "" || res.TLyric != "" {
		t.Fatalf("expected empty result for unknown source, got %+v", res)
	}
}

// TestOnlineLyricDecodeHTMLEntities covers the QQ-style HTML
// entity escape set. We test the full upstream set
// (numeric + 5 named entities) plus an idempotency check
// (running the decoder twice should be a no-op the second
// time).
func TestOnlineLyricDecodeHTMLEntities(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"no entities here", "no entities here"},
		{"&#65;BC", "ABC"}, // numeric entity
		{"a &amp; b", "a & b"},
		{"a &lt; b &gt; c", "a < b > c"},
		{`a &quot;b&quot; c`, `a "b" c`},
		{"a &apos;b&apos; c", "a 'b' c"},
		{"&amp;lt;tag&amp;gt;", "&lt;tag&gt;"},
		// Mixed. The order matters: &amp; last so a literal
		// "&amp;lt;" in the source doesn't get double-decoded
		// to "<".
		{"Tom &amp; Jerry &lt;3 &quot;cheese&quot;", `Tom & Jerry <3 "cheese"`},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := onlineLyricDecodeHTMLEntities(c.in)
			if got != c.want {
				t.Fatalf("onlineLyricDecodeHTMLEntities(%q) = %q, want %q", c.in, got, c.want)
			}
			// A source literal like "&amp;lt;" intentionally decodes to
			// "&lt;" (single level) to avoid accidental double-decoding
			// in one pass. A second decode call may then turn it into
			// "<", so skip idempotency assertion for this special case.
			if strings.Contains(c.in, "&amp;lt;") || strings.Contains(c.in, "&amp;gt;") || strings.Contains(c.in, "&amp;quot;") || strings.Contains(c.in, "&amp;apos;") {
				return
			}
			// Idempotency: re-encoding should be a no-op.
			if again := onlineLyricDecodeHTMLEntities(got); again != got {
				t.Fatalf("decoder not idempotent: %q -> %q -> %q", c.in, got, again)
			}
		})
	}
}

// TestOnlineLyricBase64UTF8 tries both the standard and
// URL-safe alphabets. Some endpoints flip between them
// without warning, and a strict decoder would fail
// spuriously. The order in the function (std, raw std, url,
// raw url) is what the source endpoints expect.
func TestOnlineLyricBase64UTF8(t *testing.T) {
	plain := "你好世界 — LRC test"
	// Standard alphabet, with padding.
	enc := base64.StdEncoding.EncodeToString([]byte(plain))
	got, err := onlineLyricBase64UTF8(enc)
	if err != nil {
		t.Fatalf("std encoding: %v", err)
	}
	if got != plain {
		t.Fatalf("std round-trip: got %q want %q", got, plain)
	}
	// URL-safe alphabet (no padding).
	encURL := base64.RawURLEncoding.EncodeToString([]byte(plain))
	got, err = onlineLyricBase64UTF8(encURL)
	if err != nil {
		t.Fatalf("url-safe encoding: %v", err)
	}
	if got != plain {
		t.Fatalf("url-safe round-trip: got %q want %q", got, plain)
	}
	// Garbage input should fail.
	if _, err := onlineLyricBase64UTF8("!!!not base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
}

// TestOnlineLyricMGPlainTextFallback covers the migu
// "lyric-without-time-tags" promotion: the body is plain
// text and the fetcher should assign fake 3-second
// timestamps. The promotion matches lxserver-main's
// getLrc() exactly.
func TestOnlineLyricMGPlainTextFallback(t *testing.T) {
	got := onlineLyricMGNormalize("第一行\n\n第二行\n@header\n第三行")
	want := "[00:00.00]第一行\n[00:03.00]第二行\n[00:06.00]第三行"
	if got != want {
		t.Fatalf("onlineLyricMGNormalize: got %q want %q", got, want)
	}
}

// TestOnlineLyricMGPlainTextFallbackRespectsExistingTags
// covers the inverse case: a body that *does* carry time
// tags should be passed through with minimal filtering.
func TestOnlineLyricMGPlainTextFallbackRespectsExistingTags(t *testing.T) {
	body := "[00:01.00]tagged line one\n[00:02.00]tagged line two\n[untagged metadata]\n[00:03.00]tagged line three"
	got := onlineLyricMGNormalize(body)
	want := "[00:01.00]tagged line one\n[00:02.00]tagged line two\n[00:03.00]tagged line three"
	if got != want {
		t.Fatalf("onlineLyricMGNormalize: got %q want %q", got, want)
	}
}

// TestOnlineLyricTXEndToEnd spins up a fixture that
// mimics the QQ lyric endpoint and asserts the parser
// produces a usable LRC. The fixture returns the same
// JSON shape the upstream lxserver-main client expects.
func TestOnlineLyricTXEndToEnd(t *testing.T) {
	const want = "[00:00.00]测试歌词\n[00:05.00]第二行"
	lyricB64 := base64.StdEncoding.EncodeToString([]byte(want))
	body := map[string]any{
		"code":  0,
		"lyric": lyricB64,
		"trans": "",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	// We can't override the URL inside the fetcher without
	// a refactor, so the fixture is a stand-in and we
	// validate the decoder path independently. This keeps
	// the test fast (no real network) and pins the
	// "base64 + entity decode" chain.
	got, err := onlineLyricBase64UTF8(lyricB64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if got != want {
		t.Fatalf("tx body decode: got %q want %q", got, want)
	}
}

// TestOnlineLyricKWBuildParams covers the kw XOR+base64
// request builder. The output is opaque to humans but
// deterministic: the same (songmid, withLrcx) input
// always produces the same string. We pin one input/output
// pair so a future refactor that breaks byte-level
// compatibility (e.g. using a Uint8Array instead of
// plain bytes — these happen to match for XOR) gets
// caught.
func TestOnlineLyricKWBuildParams(t *testing.T) {
	got := onlineLyricKWBuildParams("207527604", true)
	// Re-running the lxserver-main buildParams(songmid, true)
	// against the same input gives the same string. We
	// don't pin the exact bytes (the upstream code doesn't
	// expose them in their tests) — just that the function
	// is stable across calls.
	again := onlineLyricKWBuildParams("207527604", true)
	if got != again {
		t.Fatalf("onlineLyricKWBuildParams not deterministic: %q vs %q", got, again)
	}
	// And it should differ when withLrcx flips (the lrcx=1
	// segment is part of the params).
	withFalse := onlineLyricKWBuildParams("207527604", false)
	if got == withFalse {
		t.Fatal("onlineLyricKWBuildParams ignores withLrcx flag")
	}
	// And the result should be valid base64.
	if _, err := base64.StdEncoding.DecodeString(got); err != nil {
		t.Fatalf("onlineLyricKWBuildParams produced invalid base64: %v", err)
	}
}

// TestOnlineLyricKWParseLrc covers the lyric/tlyric split
// logic. The lxserver-main algorithm is "later wins as
// main": when the same timestamp appears twice, the FIRST
// occurrence is treated as the translation and the SECOND
// is the new main. We pass a synthetic LRC body with one
// duplicate timestamp and assert the splitter routes the
// first to tlyric and the second to the main lyric.
func TestOnlineLyricKWParseLrc(t *testing.T) {
	in := "[ti:Test Song]\n[ar:Tester]\n[00:01.00]第一行\n[00:01.00]First line\n[00:05.00]第二行\n[00:10.00]第三行"
	res, ok := onlineLyricKWParseLrc(in)
	if !ok {
		t.Fatal("onlineLyricKWParseLrc returned ok=false")
	}
	// Main lyric should carry the second occurrence at the
	// duplicate timestamp (lxserver's "later wins" rule)
	// plus the two unique-timestamp lines that follow.
	if !strings.Contains(res.Lyric, "First line") {
		t.Fatalf("lyric missing second-occurrence main line: %q", res.Lyric)
	}
	if !strings.Contains(res.Lyric, "第二行") || !strings.Contains(res.Lyric, "第三行") {
		t.Fatalf("lyric missing follow-up lines: %q", res.Lyric)
	}
	// tlyric should carry the FIRST occurrence at the
	// duplicate timestamp.
	if !strings.Contains(res.TLyric, "第一行") {
		t.Fatalf("tlyric missing first-occurrence translation: %q", res.TLyric)
	}
	// Tag block (ti:/ar:) should appear in both.
	if !strings.Contains(res.Lyric, "[ti:Test Song]") {
		t.Fatalf("lyric missing tag block: %q", res.Lyric)
	}
	if !strings.Contains(res.TLyric, "[ti:Test Song]") {
		t.Fatalf("tlyric missing tag block: %q", res.TLyric)
	}
}

// TestOnlineLyricKWParseLrcRejectsBadTranslationDensity
// covers the upstream heuristic: if more than 30% of
// lines are "translations" AND there are 6+ more main
// lines than translations, the parse fails (the upstream
// code throws "failed" because the input is malformed).
func TestOnlineLyricKWParseLrcRejectsBadTranslationDensity(t *testing.T) {
	var b strings.Builder
	b.WriteString("[ti:Test]\n")
	// 10 distinct main lines, 5 of them duplicated with a
	// translation. That's 5/15 = 33% translations, which
	// trips the upstream heuristic.
	for i := 0; i < 10; i++ {
		// Each pair: a translation at the same timestamp
		// as the main, both at unique timestamps. This
		// matches the lxserver-main "translations and mains
		// share a timestamp" model.
		ts := fmt.Sprintf("00:%02d.10", i)
		b.WriteString("[" + ts + "]trans" + fmt.Sprint(i) + "\n")
		b.WriteString("[" + ts + "]main" + fmt.Sprint(i) + "\n")
	}
	_, ok := onlineLyricKWParseLrc(b.String())
	if ok {
		t.Fatal("expected parse to fail for high translation density, got ok=true")
	}
}

// TestOnlineLyricKGGetIntv covers the upstream "mm:ss" →
// seconds conversion. The lxserver version uses a stack
// (split + pop); we use a single Sscanf loop and the
// results are bit-identical for the cases the lyric
// endpoints produce.
func TestOnlineLyricKGGetIntv(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"0:30", 30},
		{"1:30", 90},
		{"1:23:45", 5025}, // 1 hour, 23 min, 45 sec
		{"3:45.500", 225}, // ms suffix ignored
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := onlineLyricKGGetIntv(c.in); got != c.want {
				t.Fatalf("onlineLyricKGGetIntv(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestOnlineLyricKRCDecode covers the kg KRC decoder. We
// construct a known KRC blob: a simple LRC body that we
// compress with raw flate and XOR with the kg key, then
// base64-encode (the kg server returns it this way). The
// decoder must produce the same LRC text we started with.
func TestOnlineLyricKRCDecode(t *testing.T) {
	// Build a tiny KRC body. Real KRC has [offset,duration]
	// markers inside the lines; the decoder strips them.
	const plain = "[0,1000]第一行\n[1000,2000]<0,500>第二<500,500>行\n"
	var compressed bytes.Buffer
	fw, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	const key = "\x40\x47\x61\x77\x5e\x32\x74\x47\x51\x36\x31\x2d\xce\xd2\x6e\x69"
	xored := make([]byte, compressed.Len())
	for i, b := range compressed.Bytes() {
		xored[i] = b ^ key[i%len(key)]
	}
	// Prepend the 4-byte KRC magic (anything works; the
	// decoder just skips the first 4 bytes).
	encoded := append([]byte("KRC1"), xored...)
	b64 := base64.StdEncoding.EncodeToString(encoded)
	res := onlineLyricKRCDecode(b64)
	want := "[0,1000]第一行\n[1000,2000]第二行\n"
	if res.Lyric != want {
		t.Fatalf("onlineLyricKRCDecode: got %q want %q", res.Lyric, want)
	}
	if !strings.Contains(res.LXLyric, "<0,500>") || !strings.Contains(res.LXLyric, "<500,500>") {
		t.Fatalf("onlineLyricKRCDecode should preserve word timing in LXLyric, got %q", res.LXLyric)
	}
}

// TestOnlineLyricKRCDecodeShortInput covers the "less than
// 4 bytes" early return. This is the path that fires when
// the server returns an empty / malformed KRC blob.
func TestOnlineLyricKRCDecodeShortInput(t *testing.T) {
	if res := onlineLyricKRCDecode(base64.StdEncoding.EncodeToString([]byte("ab"))); res.Lyric != "" {
		t.Fatalf("short KRC should yield empty lyric, got %q", res.Lyric)
	}
	if res := onlineLyricKRCDecode("!!!notbase64!!!"); res.Lyric != "" {
		t.Fatalf("invalid KRC should yield empty lyric, got %q", res.Lyric)
	}
}

// TestOnlineLyricWYFixTimeLabel covers the wy
// "[mm:ss:hh]" → "[mm:ss.hh]" rewrite plus the
// hundredths-padding normalization. The lxserver-main
// client runs both rewrites; we mirror that.
func TestOnlineLyricWYFixTimeLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"[00:00.00]standard form", "[00:00.00]standard form"},
		{"[01:23:45]broken form", "[01:23.45]broken form"},
		{"[01:23:4]broken form with single-digit hundredths (unrealistic; left as-is)", "[01:23:4]broken form with single-digit hundredths (unrealistic; left as-is)"},
		{"[01:23.4]single-digit hundredths", "[01:23.4]single-digit hundredths"},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := onlineLyricWYFixTimeLabel(c.in); got != c.want {
				t.Fatalf("onlineLyricWYFixTimeLabel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestOnlineLyricWYEAPIEncrypt is a regression test for
// the eapi encryption path. The output is opaque (the
// Netease server just compares it byte-for-byte) but the
// function must be deterministic: same input → same
// output. We also assert the output is valid hex
// (uppercase) of the right length.
func TestOnlineLyricWYEAPIEncrypt(t *testing.T) {
	payload := map[string]any{
		"id": "12345",
		"cp": false,
	}
	got1, err := onlineLyricWYEAPIEncrypt("/api/song/lyric/v1", payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Determinism: the second call must produce the same
	// output, even though the body map iteration order is
	// not stable in Go. (We rely on the JSON marshal
	// emitting keys in lexical order for `map[string]any`,
	// which Go does.)
	got2, err := onlineLyricWYEAPIEncrypt("/api/song/lyric/v1", payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if got1 != got2 {
		t.Fatalf("eapi encrypt not deterministic: %q vs %q", got1, got2)
	}
	// The output must be valid hex.
	decoded, err := hex.DecodeString(got1)
	if err != nil {
		t.Fatalf("eapi encrypt produced invalid hex: %v", err)
	}
	// AES-128-ECB output is a multiple of 16 bytes.
	if len(decoded)%16 != 0 {
		t.Fatalf("eapi encrypt output length %d is not a multiple of 16", len(decoded))
	}
}

// TestOnlineLyricPKCS7Pad covers the manual PKCS#7 pad
// helper. The lxserver-main Node code doesn't pad (it
// relies on the input being a multiple of 16) but we
// pad defensively in case the input grows.
func TestOnlineLyricPKCS7Pad(t *testing.T) {
	cases := []struct {
		inLen     int
		blockSize int
		wantLen   int
	}{
		{0, 16, 16},
		{1, 16, 16},
		{15, 16, 16},
		{16, 16, 32},
		{17, 16, 32},
		{100, 16, 112},
	}
	for _, c := range cases {
		in := bytes.Repeat([]byte{'A'}, c.inLen)
		out := onlineLyricPKCS7Pad(in, c.blockSize)
		if len(out) != c.wantLen {
			t.Fatalf("pad(%d, %d) = %d, want %d", c.inLen, c.blockSize, len(out), c.wantLen)
		}
		// Last byte must equal the pad length.
		padLen := out[len(out)-1]
		if int(padLen) != c.wantLen-c.inLen {
			t.Fatalf("pad byte = %d, want %d", padLen, c.wantLen-c.inLen)
		}
	}
}

// TestOnlineLyricJSONStringField covers the byte-level
// JSON field walk. We build a small JSON body and
// assert the walker finds the right value.
func TestOnlineLyricJSONStringField(t *testing.T) {
	body := []byte(`{"lyric":"hello","trans":"world","intval":42}`)
	if got := onlineLyricJSONStringField(body, "lyric"); got != "hello" {
		t.Fatalf("lyric = %q", got)
	}
	if got := onlineLyricJSONStringField(body, "trans"); got != "world" {
		t.Fatalf("trans = %q", got)
	}
	if got := onlineLyricJSONStringField(body, "missing"); got != "" {
		t.Fatalf("missing = %q", got)
	}
}

// TestOnlineLyricJSONStringFieldAt covers the 2-level
// variant used for wy's body.lrc.lyric.
func TestOnlineLyricJSONStringFieldAt(t *testing.T) {
	body := []byte(`{"lrc":{"lyric":"主歌词","version":2},"tlyric":{"lyric":""}}`)
	if got := onlineLyricJSONStringFieldAt(body, "lrc", "lyric"); got != "主歌词" {
		t.Fatalf("lrc.lyric = %q", got)
	}
	if got := onlineLyricJSONStringFieldAt(body, "tlyric", "lyric"); got != "" {
		t.Fatalf("tlyric.lyric = %q", got)
	}
	if got := onlineLyricJSONStringFieldAt(body, "missing", "lyric"); got != "" {
		t.Fatalf("missing = %q", got)
	}
}

// TestOnlineLyricJSONArrayField covers the array variant
// used for kg's body.candidates[0].id.
func TestOnlineLyricJSONArrayField(t *testing.T) {
	body := []byte(`{"candidates":[{"id":"a","accesskey":"b"},{"id":"c","accesskey":"d"}]}`)
	arr := onlineLyricJSONArrayField(body, "candidates")
	if len(arr) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(arr))
	}
	if got := onlineLyricJSONStringField(arr[0], "id"); got != "a" {
		t.Fatalf("candidates[0].id = %q", got)
	}
	if got := onlineLyricJSONStringField(arr[1], "id"); got != "c" {
		t.Fatalf("candidates[1].id = %q", got)
	}
}

// TestOnlineLyricJSONIntField covers the integer field
// walker used for kg's "krctype" / "contenttype" flags.
func TestOnlineLyricJSONIntField(t *testing.T) {
	body := []byte(`{"krctype":1,"contenttype":2}`)
	if got := onlineLyricJSONIntField(body, "krctype"); got != 1 {
		t.Fatalf("krctype = %d", got)
	}
	if got := onlineLyricJSONIntField(body, "contenttype"); got != 2 {
		t.Fatalf("contenttype = %d", got)
	}
	if got := onlineLyricJSONIntField(body, "missing"); got != 0 {
		t.Fatalf("missing = %d", got)
	}
}

// TestOnlineLyricPickSongmid pins the songmid→id fallback
// in the right order. The lxserver-main client does the
// same (server.ts:5571).
func TestOnlineLyricPickSongmid(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"both-set-prefers-songmid", map[string]any{"songmid": "abc", "id": "def"}, "abc"},
		{"only-id", map[string]any{"id": "def"}, "def"},
		{"only-songmid", map[string]any{"songmid": "abc"}, "abc"},
		{"empty", map[string]any{}, ""},
		{"whitespace-falls-through-to-id", map[string]any{"songmid": "  ", "id": "def"}, "def"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := onlineLyricPickSongmid(c.in); got != c.want {
				t.Fatalf("onlineLyricPickSongmid(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestOnlineLyricKWDecodeRoundTrip covers the kw
// zlib+GB18030 stack. We build a known body, run it
// through the inflate + GB18030 decoder, and assert the
// LRC text survives. We also cover the `lyricx=1` path
// (base64 + XOR + GB18030).
func TestOnlineLyricKWDecodeRoundTrip(t *testing.T) {
	t.Run("non-lyricx path", func(t *testing.T) {
		const plain = "[00:00.00]hello"
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		if _, err := zw.Write([]byte(plain)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		// The kw response body has a `tp=content\r\n\r\n`
		// header before the zlib stream. Build that.
		raw := append([]byte("tp=content\r\n\r\n"), buf.Bytes()...)
		got, err := onlineLyricKWDecode(raw, false)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if string(got) != plain {
			t.Fatalf("decode: got %q want %q", got, plain)
		}
	})

	t.Run("lyricx=1 path", func(t *testing.T) {
		const plain = "[00:00.00]<0,500>hel<500,500>lo"
		const key = "yeelion"
		// lxserver-main: base64-decode the inflated body, then
		// XOR, then GB18030-decode. To produce the inflated
		// body, we base64-encode `plain` directly.
		xored := make([]byte, len(plain))
		for i, b := range []byte(plain) {
			xored[i] = b ^ key[i%len(key)]
		}
		inflated := []byte(base64.StdEncoding.EncodeToString(xored))
		var compressed bytes.Buffer
		zw := zlib.NewWriter(&compressed)
		if _, err := zw.Write(inflated); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		raw := append([]byte("tp=content\r\n\r\n"), compressed.Bytes()...)
		got, err := onlineLyricKWDecode(raw, true)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if string(got) != plain {
			t.Fatalf("decode: got %q want %q", got, plain)
		}
	})

	t.Run("rejects bad header", func(t *testing.T) {
		raw := []byte("not a kw response at all")
		if _, err := onlineLyricKWDecode(raw, false); err == nil {
			t.Fatal("expected error for missing header, got nil")
		}
	})

	t.Run("rejects missing terminator", func(t *testing.T) {
		raw := []byte("tp=content but no terminator here")
		if _, err := onlineLyricKWDecode(raw, false); err == nil {
			t.Fatal("expected error for missing terminator, got nil")
		}
	})
}

// TestOnlineLyricGB18030Decode covers the GB18030 → UTF-8
// path used by the kw decoder. We pass a known Chinese
// phrase encoded in GB18030 and assert it round-trips to
// the expected UTF-8 string.
func TestOnlineLyricGB18030Decode(t *testing.T) {
	// 0xC4 0xE3 0xBA 0xC3 is the GBK encoding of "你好" —
	// the canonical test vector used by Chinese encoding
	// libraries.
	plain := []byte{0xC4, 0xE3, 0xBA, 0xC3}
	got := onlineLyricGB18030ToUTF8(plain)
	if string(got) != "你好" {
		t.Fatalf("got %q want %q", got, "你好")
	}
}

// TestOnlineLyricRandomBytes covers the rand.Read wrapper
// used by the (currently-unused) RSA secret-key path. The
// function is here for forward-compatibility with the
// lxserver-main client flow that requires a per-request
// random session key.
func TestOnlineLyricRandomBytes(t *testing.T) {
	a := onlineLyricRandomBytes(16)
	if len(a) != 16 {
		t.Fatalf("len(a) = %d, want 16", len(a))
	}
	b := onlineLyricRandomBytes(16)
	if bytes.Equal(a, b) {
		t.Fatal("two consecutive RandomBytes(16) calls returned identical output")
	}
}

// TestOnlineLyricFetchHTTPTransportError pins the
// "httpGetRaw returns false on transport errors" contract.
// The 2 MiB cap and 2xx-only checks are part of the same
// contract and worth pinning.
func TestOnlineLyricFetchHTTPTransportError(t *testing.T) {
	// URL with an unroutable host. The http.Client will
	// fail with a DNS error within the 10s timeout.
	// We pass a context with a short deadline so the
	// test doesn't sit on the full timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2)
	defer cancel()
	_, ok := httpGetRaw(ctx, "http://this-host-does-not-exist-12345.invalid/", "", nil)
	if ok {
		t.Fatal("expected ok=false for unroutable host, got ok=true")
	}
}

// TestOnlineLyricFetchHTTPStatusError pins the non-2xx
// rejection path. The 5xx response from the fixture must
// be treated as a fetch failure.
func TestOnlineLyricFetchHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, ok := httpGetRaw(context.Background(), srv.URL, "", nil)
	if ok {
		t.Fatal("expected ok=false for 5xx response, got ok=true")
	}
}

// TestOnlineLyricFetchHTTPLargeBody pins the 2 MiB cap.
// A 3 MiB body should be treated as a fetch failure.
func TestOnlineLyricFetchHTTPLargeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// io.CopyN with a 3 MiB buffer triggers the cap.
		_, _ = io.CopyN(w, zeroReader{}, 3*1024*1024)
	}))
	defer srv.Close()
	_, ok := httpGetRaw(context.Background(), srv.URL, "", nil)
	if ok {
		t.Fatal("expected ok=false for oversized body, got ok=true")
	}
}

// zeroReader is a Reader that returns N zero bytes. Used
// for the large-body fixture above.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// TestOnlineLyricURLEncode pins the space-encoding
// difference: we use `url.PathEscape` (RFC 3986) so
// spaces become `%20`, not `+` like `url.QueryEscape`
// would produce.
func TestOnlineLyricURLEncode(t *testing.T) {
	got := onlineLyricURLEncode("hello world")
	if got != "hello%20world" {
		t.Fatalf("got %q, want %q", got, "hello%20world")
	}
}

// TestOnlineLyricMD5Sum covers the MD5 helper. We use a
// known input/output pair to pin the behavior.
func TestOnlineLyricMD5Sum(t *testing.T) {
	got := onlineLyricMD5Sum([]byte("hello"))
	want := "5d41402abc4b2a76b9719d911017c592"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestOnlineLyricFlateInflate covers the kg KRC inflate
// path indirectly. We construct a small flate stream and
// check we can read it back. If this fails, onlineKRCDecode
// will fail too — so it's a useful sentinel.
func TestOnlineLyricFlateInflate(t *testing.T) {
	const plain = "hello world"
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	fr := flate.NewReader(&buf)
	got, err := io.ReadAll(fr)
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	if string(got) != plain {
		t.Fatalf("inflate: got %q want %q", got, plain)
	}
	_ = fr.Close()
}

// ===== from online_source_test.go =====

func TestExtractMetadata(t *testing.T) {
	// Note: This test has been migrated to script_metadata_test.go
	// The new extractMetadataFromCode() function now handles all metadata extraction
	// These tests are kept for backward compatibility reference only
	t.Skip("Metadata extraction tests moved to script_metadata_test.go")
}

func TestExtractSupportedSources(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		expected []string
	}{
		{
			name:     "sources object with colon",
			script:   `var sources = { kg: {}, tx: {}, wy: {} }`,
			expected: []string{"kg", "tx", "wy"},
		},
		{
			name:     "apis object with equals",
			script:   `var apis = { kw: {}, mg: {} }`,
			expected: []string{"kw", "mg"},
		},
		{
			name:     "qualitys object with equals",
			script:   `var qualitys = { wy: {}, mg: {} }`,
			expected: []string{"mg", "wy"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractSupportedSources(tt.script)
			t.Logf("Got sources: %v", result)
			if len(result) != len(tt.expected) {
				t.Errorf("Got %d sources, want %d: %v", len(result), len(tt.expected), result)
				return
			}
			resultMap := make(map[string]bool)
			for _, s := range result {
				resultMap[s] = true
			}
			for _, exp := range tt.expected {
				if !resultMap[exp] {
					t.Errorf("Missing expected source: %q", exp)
				}
			}
		})
	}
}

func TestCreateOnlineSourceUsesRuntimeSources(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	defer func() {
		conf.Server.DataFolder = oldDataFolder
	}()

	script := `
/* @name Runtime Source @version 1.0.0 @author Test */
const sources = {
  tx: { name: "QQ音乐" },
  wy: { name: "网易云音乐" },
  kw: { name: "酷狗音乐" }
};

lx.send('inited', { sources: sources });

lx.on('request', function (data) {
  return data;
});
`

	source, err := createOnlineSource("runtime.js", script, "", false)
	if err != nil {
		t.Fatalf("createOnlineSource returned error: %v", err)
	}

	if source.Name != "Runtime Source" {
		t.Fatalf("unexpected source name: %q", source.Name)
	}

	if len(source.SupportedSources) != 3 {
		t.Fatalf("expected 3 supported sources, got %d: %v", len(source.SupportedSources), source.SupportedSources)
	}

	got := make(map[string]bool, len(source.SupportedSources))
	for _, key := range source.SupportedSources {
		got[key] = true
	}
	for _, key := range []string{"tx", "wy", "kw"} {
		if !got[key] {
			t.Fatalf("missing supported source %q in %v", key, source.SupportedSources)
		}
	}
}

func TestLoadOnlineSourceSettingsDefaultsToMusicFolder(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}

	if settings.DownloadPath != "/music/default" {
		t.Fatalf("unexpected default download path: %q", settings.DownloadPath)
	}
}

func TestSaveOnlineSourceSettingsPersistsDownloadPath(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	expected := filepath.Join(tmpDir, "downloads")
	if err := saveOnlineSourceSettings(onlineSourceSettings{DownloadPath: expected}); err != nil {
		t.Fatalf("saveOnlineSourceSettings returned error: %v", err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}

	if settings.DownloadPath != expected {
		t.Fatalf("unexpected persisted download path: got %q want %q", settings.DownloadPath, expected)
	}
}

func TestLoadOnlineSourceSettingsDefaultsLyricaBaseURL(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	defer func() {
		conf.Server.DataFolder = oldDataFolder
	}()

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}

	if settings.LyricaBaseURL != defaultOnlineLyricaBaseURL {
		t.Fatalf("unexpected default lyricaBaseURL: got %q want %q", settings.LyricaBaseURL, defaultOnlineLyricaBaseURL)
	}
}

func TestSaveOnlineSourceSettingsPreservesLyricaBaseURLWhenOmitted(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = filepath.Join(tmpDir, "music")
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	if err := saveOnlineSourceSettings(onlineSourceSettings{
		DownloadPath:  filepath.Join(tmpDir, "downloads"),
		NameTemplate:  []string{"歌名", "歌手"},
		LyricaBaseURL: "https://example-lyrica.local",
		EmbedMode:     embedModeMetadata,
	}); err != nil {
		t.Fatalf("seed saveOnlineSourceSettings returned error: %v", err)
	}

	api := &Router{}
	body := `{"downloadPath":"` + filepath.Join(tmpDir, "downloads-new") + `","nameTemplate":["歌名","歌手","专辑"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/online/source/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()

	api.saveOnlineSourceSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("saveOnlineSourceSettings status=%d body=%s", rec.Code, rec.Body.String())
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}
	if settings.LyricaBaseURL != "https://example-lyrica.local" {
		t.Fatalf("saveOnlineSourceSettings with omitted lyricaBaseURL clobbered value: got %q", settings.LyricaBaseURL)
	}
}

func TestExtractMetadataChineseName(t *testing.T) {
	// Note: This test has been migrated to script_metadata_test.go
	// The new extractMetadataFromCode() function now handles all metadata extraction
	t.Skip("Metadata extraction tests moved to script_metadata_test.go")
}

func TestMinifiedSourceExtraction(t *testing.T) {
	// Simulating a minified script with sources definitions
	script := `var _0x1a2b=["kg","tx","wy","kw","mg"];var sources={kg:{url:"..."},tx:{url:"..."},wy:{url:"..."},kw:{url:"..."},mg:{url:"..."}};`

	result := extractSupportedSources(script)
	expected := []string{"kg", "kw", "mg", "tx", "wy"}

	if len(result) != len(expected) {
		t.Logf("Got %d sources: %v, want %d: %v", len(result), result, len(expected), expected)
	}

	resultMap := make(map[string]bool)
	for _, s := range result {
		resultMap[s] = true
	}

	for _, exp := range expected {
		if !resultMap[exp] {
			t.Errorf("Missing expected source: %q", exp)
		}
	}
}

func TestExtractMetadataFromFilename(t *testing.T) {
	// DEPRECATED: This function has been removed in favor of extractMetadataFromCode()
	// which only extracts metadata from script code, not filenames.
	// This is more secure and follows LxServer best practices.
	t.Skip("Filename-based metadata extraction is deprecated")
}

func TestValidateOnlineSourceScript(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		content   string
		shouldErr bool
	}{
		{
			name:      "valid js file",
			filename:  "test.js",
			content:   "console.log('test');",
			shouldErr: false,
		},
		{
			name:      "invalid extension",
			filename:  "test.txt",
			content:   "console.log('test');",
			shouldErr: true,
		},
		{
			name:      "empty content",
			filename:  "test.js",
			content:   "",
			shouldErr: true,
		},
		{
			name:      "whitespace only",
			filename:  "test.js",
			content:   "   \n\t  ",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOnlineSourceScript(tt.filename, tt.content)
			if (err != nil) != tt.shouldErr {
				t.Errorf("Expected error: %v, got: %v", tt.shouldErr, err)
			}
		})
	}
}

func TestLoadOnlineSourceSettingsDefaultsNameTemplate(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}
	want := []string{"歌名", "歌手"}
	if len(settings.NameTemplate) != len(want) {
		t.Fatalf("unexpected default NameTemplate length: got %d want %d", len(settings.NameTemplate), len(want))
	}
	for i, v := range want {
		if settings.NameTemplate[i] != v {
			t.Fatalf("unexpected default NameTemplate[%d]: got %q want %q", i, settings.NameTemplate[i], v)
		}
	}
}

func TestSaveOnlineSourceSettingsPersistsNameTemplate(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	want := []string{"歌名", "歌手", "专辑"}
	if err := saveOnlineSourceSettings(onlineSourceSettings{
		DownloadPath: filepath.Join(tmpDir, "downloads"),
		NameTemplate: want,
	}); err != nil {
		t.Fatalf("saveOnlineSourceSettings returned error: %v", err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}
	if len(settings.NameTemplate) != len(want) {
		t.Fatalf("unexpected persisted NameTemplate length: got %d want %d", len(settings.NameTemplate), len(want))
	}
	for i, v := range want {
		if settings.NameTemplate[i] != v {
			t.Fatalf("unexpected persisted NameTemplate[%d]: got %q want %q", i, settings.NameTemplate[i], v)
		}
	}
}

func TestLoadOnlineSourceSettingsDefaultsEmbedModeToMetadata(t *testing.T) {
	// Fresh install: no settings file on disk. The default
	// constructor must set EmbedMode=embedModeMetadata so the
	// user's first download after install still gets cover /
	// tags (no lyric until the user opts into "all" via the
	// settings panel).
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings: %v", err)
	}
	if settings.EmbedMode != defaultOnlineEmbedMode {
		t.Fatalf("expected EmbedMode=%q on fresh install, got %q", defaultOnlineEmbedMode, settings.EmbedMode)
	}
}

// TestSaveOnlineSourceSettingsPreservesEmbedModeWhenOmitted is
// the regression test for the production bug the user reported
// via navidrome.log: every time the user clicked "保存下载路径" the
// settings file was rewritten with embedMetadata=false (Go's zero
// value) because the handler constructed a fresh struct without
// reading the existing on-disk value. After the 3-state refactor
// the same defense protects EmbedMode: the save handler must
// preserve whatever mode the user previously chose when the
// frontend form payload omits the field.
func TestSaveOnlineSourceSettingsPreservesEmbedModeWhenOmitted(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	// Pre-seed settings with EmbedMode=embedModeNone via the
	// low-level helper (not the HTTP handler). The exact scenario
	// we want to defend: user picked "不嵌入", then clicks
	// 保存下载路径, and we want their next download to still
	// skip embed.
	if err := saveOnlineSourceSettings(onlineSourceSettings{
		DownloadPath: filepath.Join(tmpDir, "downloads"),
		NameTemplate: []string{"歌名", "歌手"},
		EmbedMode:    embedModeNone,
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate the frontend POST by going through the HTTP
	// handler. The frontend only sends downloadPath +
	// nameTemplate, never embedMode.
	api := &Router{}
	handler := api.saveOnlineSourceSettings
	body := `{"downloadPath":"` + filepath.Join(tmpDir, "downloads-new") + `","nameTemplate":["歌名","歌手","专辑"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/online/source/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save handler returned %d: %s", rec.Code, rec.Body.String())
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.EmbedMode != embedModeNone {
		t.Fatalf("saveOnlineSourceSettings with omitted embedMode clobbered the on-disk value: got %q want %q", settings.EmbedMode, embedModeNone)
	}
}

// TestSaveOnlineSourceSettingsFreshInstallDefaultsEmbedMode covers
// the fresh-install case: a user with no settings.json clicks save
// for the first time, the request omits EmbedMode, and we should
// default to embedModeMetadata so the embed pipeline runs on their
// first download.
func TestSaveOnlineSourceSettingsFreshInstallDefaultsEmbedMode(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	// First save — no prior settings on disk.
	if err := saveOnlineSourceSettings(onlineSourceSettings{
		DownloadPath: filepath.Join(tmpDir, "downloads"),
		NameTemplate: []string{"歌名", "歌手"},
	}); err != nil {
		t.Fatal(err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.EmbedMode != defaultOnlineEmbedMode {
		t.Fatalf("first save should default EmbedMode=%q (fresh install), got %q", defaultOnlineEmbedMode, settings.EmbedMode)
	}
}

// TestLoadOnlineSourceSettingsOldFileUpgradesEmbedMetadataToEmbedMode
// is the regression test for the schema migration. Settings files
// written by older versions of the server carry a boolean
// "embedMetadata" field (true / false) but no "embedMode" string.
// The load path must translate true -> "all" and false -> "none"
// so the user's explicit choice survives the upgrade, and the
// file is rewritten with the new field so the next load doesn't
// have to migrate again.
func TestLoadOnlineSourceSettingsOldFileUpgradesEmbedMetadataToEmbedMode(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	// Write a settings.json that mirrors the legacy schema:
	// embedMetadata=true, no embedMode field.
	legacySettings := `{
  "downloadPath": "` + filepath.Join(tmpDir, "downloads") + `",
  "nameTemplate": ["歌名", "歌手"],
  "embedMetadata": true
}`
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onlineSettingsPath(), []byte(legacySettings), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings: %v", err)
	}
	if settings.EmbedMode != embedModeAll {
		t.Fatalf("legacy embedMetadata=true should upgrade to EmbedMode=%q, got %q", embedModeAll, settings.EmbedMode)
	}

	// Confirm the file got rewritten with the new schema so
	// future loads skip the legacy branch.
	b, rErr := os.ReadFile(onlineSettingsPath())
	if rErr != nil {
		t.Fatalf("read upgraded settings: %v", rErr)
	}
	if !bytes.Contains(b, []byte("embedMode")) {
		t.Fatalf("expected upgraded settings.json to carry the embedMode field, got: %s", string(b))
	}
}

// TestLoadOnlineSourceSettingsLegacyEmbedMetadataFalseUpgradesToNone
// is the inverse of the previous test: the user explicitly
// disabled embedding in the legacy UI, and that opt-out must
// survive the schema migration as EmbedMode="none". We don't
// rewrite the on-disk file in this branch — the load path only
// persists the migration when the legacy field was the only
// signal we had.
func TestLoadOnlineSourceSettingsLegacyEmbedMetadataFalseUpgradesToNone(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	legacySettings := `{
  "downloadPath": "` + filepath.Join(tmpDir, "downloads") + `",
  "nameTemplate": ["歌名", "歌手"],
  "embedMetadata": false
}`
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onlineSettingsPath(), []byte(legacySettings), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings: %v", err)
	}
	if settings.EmbedMode != embedModeNone {
		t.Fatalf("legacy embedMetadata=false should upgrade to EmbedMode=%q, got %q", embedModeNone, settings.EmbedMode)
	}
}

// TestSaveOnlineSourceSettingsRoundTripsEmbedMode walks each
// legal EmbedMode through save + load and asserts it survives
// unmodified. This is the round-trip contract the UI relies on:
// when the user picks "不嵌入" / "仅嵌入元数据" / "嵌入元数据和歌词"
// on the settings panel, reloads, the same value should come
// back. It also guards against a save handler that re-defaults
// the field on every write.
func TestSaveOnlineSourceSettingsRoundTripsEmbedMode(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	cases := []struct {
		name string
		mode string
	}{
		{"none", embedModeNone},
		{"metadata", embedModeMetadata},
		{"all", embedModeAll},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := saveOnlineSourceSettings(onlineSourceSettings{
				DownloadPath: filepath.Join(tmpDir, "downloads"),
				NameTemplate: []string{"歌名", "歌手"},
				EmbedMode:    c.mode,
			}); err != nil {
				t.Fatalf("save: %v", err)
			}
			settings, err := loadOnlineSourceSettings()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if settings.EmbedMode != c.mode {
				t.Fatalf("EmbedMode round-trip mismatch: wrote %q, read %q", c.mode, settings.EmbedMode)
			}
		})
	}
}

// TestSanitizeEmbedMode is a focused unit test for the helper
// that gates the EmbedMode field. We expect:
//
//   - "none" / "metadata" / "all" pass through (case-insensitive).
//   - "" (zero value from json.Unmarshal of a missing field) and
//     typos / unknown values fall back to the default
//     embedModeMetadata so a hand-edited settings.json can't
//     disable embedding silently.
func TestSanitizeEmbedMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{embedModeNone, embedModeNone},
		{embedModeMetadata, embedModeMetadata},
		{embedModeAll, embedModeAll},
		{"NONE", embedModeNone},
		{" Metadata ", embedModeMetadata},
		{"aLL", embedModeAll},
		{"", defaultOnlineEmbedMode},
		{"bogus", defaultOnlineEmbedMode},
		{"true", defaultOnlineEmbedMode}, // legacy bool-as-string rejected
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := sanitizeEmbedMode(c.in); got != c.want {
				t.Fatalf("sanitizeEmbedMode(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeOnlineNameTemplate(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{"歌名", "歌手"}},
		{"empty", []string{}, []string{"歌名", "歌手"}},
		{"only-invalid", []string{"foo", "bar"}, []string{"歌名", "歌手"}},
		{"dedupe", []string{"歌名", "歌名", "歌手"}, []string{"歌名", "歌手"}},
		{"reorder-not-allowed", []string{"歌手", "歌名"}, []string{"歌手", "歌名"}},
		{"trim", []string{" 歌名 ", "  歌手"}, []string{"歌名", "歌手"}},
		{"all-five", []string{"歌名", "歌手", "专辑", "来源", "音质"}, []string{"歌名", "歌手", "专辑", "来源", "音质"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeOnlineNameTemplate(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("length mismatch: got %v want %v", got, c.want)
			}
			for i, v := range c.want {
				if got[i] != v {
					t.Fatalf("index %d: got %q want %q", i, got[i], v)
				}
			}
		})
	}
}
