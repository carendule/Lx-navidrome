package nativeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
)

type playlistSyncTask struct {
	mu                   sync.RWMutex
	ID                   string
	NavidromPlaylistID   string
	PlaylistID           string
	PlaylistName         string
	PlaylistCover        string
	SourceType           string
	Status               string // syncing, sync-completed, sync-error
	Progress             int
	RemainingCount       int
	CurrentSongIndex     int
	CurrentSongTitle     string
	SourceName           string
	Songs                []map[string]any
	CompletedSongs       []string // List of song IDs that have been added to Navidrome
	CurrentSongReused    bool     // Whether current song was matched from library
	FailedSongs          []string
	FailedSongDetails    []playlistSyncFailedSong
	DownloadQueue        []map[string]any
	DownloadTaskIDMap    map[string]int // Maps download task ID to song index
	CurrentDownloadID    string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	LastProgressUpdate   time.Time
	NameTemplate         []string
	DownloadPath         string
	PreferredQuality     string   // User-selected quality
	QualityFallbackOrder []string // Fallback order for quality degradation
	PauseRequested       bool
	CurrentCancel        context.CancelFunc
	Running              bool
	RequestUser          model.User
}

type playlistSyncStartRequest struct {
	TaskID             string           `json:"taskId"`
	NavidromPlaylistID string           `json:"navidromPlaylistId"`
	PlaylistName       string           `json:"playlistName,omitempty"`
	PlaylistCover      string           `json:"playlistCover,omitempty"`
	Songs              []map[string]any `json:"songs"`
	PreferredQuality   string           `json:"preferredQuality,omitempty"`
}

type playlistSyncFailedSong struct {
	Name   string `json:"name"`
	Singer string `json:"singer"`
	Reason string `json:"reason"`
}

type playlistSyncStatusResponse struct {
	ID                string                   `json:"id"`
	Status            string                   `json:"status"`
	Progress          int                      `json:"progress"`
	RemainingCount    int                      `json:"remainingCount"`
	CurrentSongTitle  string                   `json:"currentSongTitle"`
	SourceName        string                   `json:"sourceName"`
	CurrentSongReused bool                     `json:"currentSongReused"`
	FailedSongDetails []playlistSyncFailedSong `json:"failedSongDetails,omitempty"`
}

type playlistSyncTaskView struct {
	ID                string                   `json:"id"`
	TaskType          string                   `json:"taskType"`
	Title             string                   `json:"title"`
	Cover             string                   `json:"cover"`
	Status            string                   `json:"status"`
	Progress          int                      `json:"progress"`
	RemainingCount    int                      `json:"remainingCount"`
	CurrentSongTitle  string                   `json:"currentSongTitle"`
	SourceName        string                   `json:"sourceName"`
	Source            string                   `json:"source"`
	CurrentSongReused bool                     `json:"currentSongReused"`
	FailedSongDetails []playlistSyncFailedSong `json:"failedSongDetails,omitempty"`
	CreatedAt         string                   `json:"createdAt"`
	UpdatedAt         string                   `json:"updatedAt"`
}

var playlistSyncTasks = struct {
	sync.RWMutex
	items map[string]*playlistSyncTask
}{items: map[string]*playlistSyncTask{}}

var playlistImportService core.Library
var playlistSyncDataStore model.DataStore

const playlistSyncTaskTTL = 2 * time.Hour

// updatePlaylistSyncTaskStatus safely updates the task status and related fields
func (t *playlistSyncTask) updateStatus(status string, title string, progress int, remaining int, reused bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = status
	if title != "" {
		t.CurrentSongTitle = title
	}
	if progress >= 0 {
		t.Progress = progress
	}
	if remaining >= 0 {
		t.RemainingCount = remaining
	}
	t.CurrentSongReused = reused
	t.UpdatedAt = time.Now()
}

// updatePlaylistSyncTaskProgress safely updates progress-related fields
func (t *playlistSyncTask) updateProgress(index int, title string, sourceName string, reused bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.CurrentSongIndex = index
	if title != "" {
		t.CurrentSongTitle = title
		t.CurrentSongReused = reused
	}
	if sourceName != "" {
		t.SourceName = sourceName
	}
	t.UpdatedAt = time.Now()
}

// addCompletedSong safely adds a completed song to the list
func (t *playlistSyncTask) addCompletedSong(songID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.CompletedSongs = append(t.CompletedSongs, songID)
	t.RemainingCount = len(t.Songs) - len(t.CompletedSongs)
	t.Progress = int((len(t.CompletedSongs) * 100) / len(t.Songs))
	t.UpdatedAt = time.Now()
}

// addFailedSong safely adds a failed song to the list and sets error status
func (t *playlistSyncTask) addFailedSong(songTitle string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.FailedSongs = append(t.FailedSongs, songTitle)
	t.Status = "sync-error"
	t.UpdatedAt = time.Now()
}

func classifyPlaylistSyncFailureReason(err error, fallback string) string {
	if err == nil {
		if fallback != "" {
			return fallback
		}
		return "未知错误"
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "未找到支持") || strings.Contains(msg, "启用音源") || strings.Contains(msg, "no enabled source") || strings.Contains(msg, "no compatible quality") {
		return "无可用解析源"
	}

	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") || strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") || strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") || strings.Contains(msg, "tls") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "unexpected eof") || strings.Contains(msg, "eof") {
		return "下载网络中断"
	}

	if strings.Contains(msg, "import media failed") || strings.Contains(msg, "failed to import") || strings.Contains(msg, "import") {
		return "入库失败"
	}

	if strings.Contains(msg, "add media to playlist failed") || strings.Contains(msg, "playlist") {
		return "入歌单失败"
	}

	if fallback != "" {
		return fallback
	}
	return "下载源失败"
}

func appendFailedSongDetail(task *playlistSyncTask, song map[string]any, reason string) {
	if task == nil {
		return
	}
	name, singer, _ := playlistSyncSongInfoFields(song)
	if strings.TrimSpace(name) == "" {
		name = strings.TrimSpace(task.CurrentSongTitle)
	}
	if strings.TrimSpace(name) == "" {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "未知错误"
	}
	for _, item := range task.FailedSongDetails {
		if item.Name == name && item.Singer == singer {
			return
		}
	}
	task.FailedSongDetails = append(task.FailedSongDetails, playlistSyncFailedSong{Name: name, Singer: singer, Reason: reason})
}

func handlePlaylistSyncStart(w http.ResponseWriter, r *http.Request) {
	var req playlistSyncStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.TaskID == "" || req.NavidromPlaylistID == "" {
		http.Error(w, "Missing taskId or navidromPlaylistId", http.StatusBadRequest)
		return
	}

	requestUser, _ := request.UserFrom(r.Context())

	// Create or get the sync task
	playlistSyncTasks.Lock()
	task, exists := playlistSyncTasks.items[req.TaskID]
	playlistSyncTasks.Unlock()

	if !exists {
		// Load default download path from settings
		downloadPath := defaultOnlineDownloadPath()
		settings, err := loadOnlineSourceSettings()
		if err == nil && settings.DownloadPath != "" {
			downloadPath = settings.DownloadPath
		}

		// Load default name template from settings
		nameTemplate := []string{"歌名", "歌手"}
		if err == nil && len(settings.NameTemplate) > 0 {
			nameTemplate = settings.NameTemplate
		}

		// Create new task
		task = &playlistSyncTask{
			ID:                 req.TaskID,
			NavidromPlaylistID: req.NavidromPlaylistID,
			PlaylistName:       req.PlaylistName,
			PlaylistCover:      req.PlaylistCover,
			SourceType: func() string {
				if len(req.Songs) > 0 {
					if src := stringValue(req.Songs[0]["source"]); src != "" {
						return src
					}
				}
				return "wy"
			}(),
			Status:               "syncing",
			Progress:             0,
			RemainingCount:       len(req.Songs),
			CompletedSongs:       []string{},
			FailedSongs:          []string{},
			FailedSongDetails:    []playlistSyncFailedSong{},
			Songs:                req.Songs,
			DownloadTaskIDMap:    make(map[string]int),
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
			LastProgressUpdate:   time.Now(),
			DownloadPath:         downloadPath,
			NameTemplate:         nameTemplate,
			PreferredQuality:     req.PreferredQuality,
			QualityFallbackOrder: getQualityFallbackOrder(req.PreferredQuality),
			RequestUser:          requestUser,
		}

		playlistSyncTasks.Lock()
		playlistSyncTasks.items[req.TaskID] = task
		playlistSyncTasks.Unlock()
	} else {
		// Update existing task
		task.NavidromPlaylistID = req.NavidromPlaylistID
		task.PlaylistName = req.PlaylistName
		task.PlaylistCover = req.PlaylistCover
		task.Songs = req.Songs
		task.RequestUser = requestUser
		if len(req.Songs) > 0 {
			if src := stringValue(req.Songs[0]["source"]); src != "" {
				task.SourceType = src
			}
		}
		task.RemainingCount = len(req.Songs)
		task.Status = "syncing"
		task.PauseRequested = false
		task.UpdatedAt = time.Now()
	}

	// Start sync process in background
	startPlaylistSyncTask(task)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "started",
	})
}

func handlePlaylistSyncStatus(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	if taskID == "" {
		http.Error(w, "Missing taskID", http.StatusBadRequest)
		return
	}

	playlistSyncTasks.RLock()
	task, exists := playlistSyncTasks.items[taskID]
	playlistSyncTasks.RUnlock()

	if !exists {
		http.Error(w, "Sync task not found", http.StatusNotFound)
		return
	}

	task.mu.RLock()
	response := playlistSyncStatusResponse{
		ID:                task.ID,
		Status:            task.Status,
		Progress:          task.Progress,
		RemainingCount:    task.RemainingCount,
		CurrentSongTitle:  task.CurrentSongTitle,
		SourceName:        task.SourceName,
		CurrentSongReused: task.CurrentSongReused,
		FailedSongDetails: append([]playlistSyncFailedSong(nil), task.FailedSongDetails...),
	}
	task.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handlePlaylistSyncTasks(w http.ResponseWriter, _ *http.Request) {
	playlistSyncTasks.RLock()
	defer playlistSyncTasks.RUnlock()

	items := make([]*playlistSyncTask, 0, len(playlistSyncTasks.items))
	for _, task := range playlistSyncTasks.items {
		items = append(items, task)
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})

	views := make([]playlistSyncTaskView, 0, len(items))
	for _, task := range items {
		task.mu.RLock()
		title := task.PlaylistName
		if title == "" {
			title = "歌单同步"
		}
		source := task.SourceType
		if source == "" {
			source = "wy"
		}
		view := playlistSyncTaskView{
			ID:                task.ID,
			TaskType:          "playlist_sync",
			Title:             title,
			Cover:             task.PlaylistCover,
			Status:            task.Status,
			Progress:          task.Progress,
			RemainingCount:    task.RemainingCount,
			CurrentSongTitle:  task.CurrentSongTitle,
			SourceName:        task.SourceName,
			Source:            source,
			CurrentSongReused: task.CurrentSongReused,
			FailedSongDetails: append([]playlistSyncFailedSong(nil), task.FailedSongDetails...),
			CreatedAt:         task.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:         task.UpdatedAt.UTC().Format(time.RFC3339),
		}
		task.mu.RUnlock()
		views = append(views, view)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tasks": views})
}

func handlePlaylistSyncRetryAll(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	toStart := make([]*playlistSyncTask, 0)
	changed := false

	playlistSyncTasks.Lock()
	for _, task := range playlistSyncTasks.items {
		// Retry failed, paused, and canceled sync tasks.
		if task.Status != "sync-error" && task.Status != "paused" && task.Status != "canceled" {
			continue
		}
		if len(task.Songs) == 0 || len(task.CompletedSongs) >= len(task.Songs) {
			task.Status = "sync-completed"
			task.Progress = 100
			task.RemainingCount = 0
			task.CurrentSongTitle = "完成"
			task.UpdatedAt = now
			changed = true
			continue
		}
		task.Status = "syncing"
		task.PauseRequested = false
		task.CurrentSongTitle = "重试中"
		task.FailedSongs = []string{}
		task.FailedSongDetails = []playlistSyncFailedSong{}
		task.RemainingCount = len(task.Songs) - len(task.CompletedSongs)
		task.Progress = int((len(task.CompletedSongs) * 100) / len(task.Songs))
		task.UpdatedAt = now
		changed = true
		if !task.Running {
			task.Running = true
			toStart = append(toStart, task)
		}
	}
	playlistSyncTasks.Unlock()

	for _, task := range toStart {
		go syncPlaylistSongs(task)
	}

	if changed {
		broadcastPlaylistSyncChange()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func handlePlaylistSyncCancelAll(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	changed := false

	playlistSyncTasks.Lock()
	for _, task := range playlistSyncTasks.items {
		switch task.Status {
		case "syncing", "resolving", "downloading", "queued":
			task.PauseRequested = true
			task.Status = "canceled"
			task.CurrentSongTitle = "已取消"
			task.UpdatedAt = now
			if task.CurrentCancel != nil {
				task.CurrentCancel()
				task.CurrentCancel = nil
			}
			changed = true
		}
	}
	playlistSyncTasks.Unlock()

	if changed {
		broadcastPlaylistSyncChange()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func handlePlaylistSyncClearCompleted(w http.ResponseWriter, _ *http.Request) {
	removed := false

	playlistSyncTasks.Lock()
	for id, task := range playlistSyncTasks.items {
		if task.Status == "sync-completed" {
			delete(playlistSyncTasks.items, id)
			removed = true
		}
	}
	playlistSyncTasks.Unlock()

	if removed {
		broadcastPlaylistSyncChange()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func handlePlaylistSyncClearFailed(w http.ResponseWriter, _ *http.Request) {
	removed := false

	playlistSyncTasks.Lock()
	for id, task := range playlistSyncTasks.items {
		if task.Status == "sync-error" || task.Status == "paused" || task.Status == "canceled" {
			delete(playlistSyncTasks.items, id)
			removed = true
		}
	}
	playlistSyncTasks.Unlock()

	if removed {
		broadcastPlaylistSyncChange()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func startPlaylistSyncTask(task *playlistSyncTask) {
	if task == nil {
		return
	}

	playlistSyncTasks.Lock()
	if task.Running {
		playlistSyncTasks.Unlock()
		return
	}
	task.Running = true
	playlistSyncTasks.Unlock()

	go syncPlaylistSongs(task)
}

func syncPlaylistSongs(task *playlistSyncTask) {
	defer func() {
		playlistSyncTasks.Lock()
		task.Running = false
		if task.CurrentCancel != nil {
			task.CurrentCancel = nil
		}
		playlistSyncTasks.Unlock()
	}()

	log.Debug(nil, "syncPlaylistSongs started", "taskID", task.ID, "songCount", len(task.Songs), "navidromPlaylistID", task.NavidromPlaylistID, "userId", task.RequestUser.ID)
	if task.RequestUser.ID == "" {
		log.Warn(nil, "syncPlaylistSongs: RequestUser is empty, continue without user context", "taskID", task.ID)
	}

	if len(task.Songs) == 0 {
		log.Debug(nil, "No songs to sync, marking as completed", "taskID", task.ID)
		task.Status = "sync-completed"
		task.Progress = 100
		task.UpdatedAt = time.Now()
		broadcastPlaylistSyncChange()
		return
	}

	// Start downloading songs one by one
	completed := map[string]bool{}
	for _, id := range task.CompletedSongs {
		if id != "" {
			completed[id] = true
		}
	}

	for idx, song := range task.Songs {
		if task.Status == "paused" || task.PauseRequested || task.Status == "canceled" {
			log.Debug(nil, "Sync canceled/paused, breaking loop", "taskID", task.ID)
			break
		}

		songID := stringValue(song["id"])
		if songID != "" && completed[songID] {
			continue
		}

		task.CurrentSongIndex = idx
		task.CurrentSongTitle = stringValue(song["name"])
		task.CurrentSongReused = false
		sourceStr := stringValue(song["source"])
		if sourceStr == "" {
			sourceStr = "unknown"
		}
		task.SourceName = sourceStr
		if candidates, err := loadEnabledSourcesForSong(sourceStr); err == nil && len(candidates) > 0 {
			task.SourceName = candidates[0].Name
		}
		task.UpdatedAt = time.Now()
		log.Debug(nil, "Downloading song", "taskID", task.ID, "songIndex", idx, "songName", task.CurrentSongTitle)
		broadcastPlaylistSyncChange()

		matchedMediaID, matched, matchErr := findMatchingLibraryMediaID(task.RequestUser, song)
		if matchErr != nil {
			log.Warn(nil, "Playlist sync library match lookup failed", "taskID", task.ID, "songName", task.CurrentSongTitle, "error", matchErr)
		} else if matched {
			if err := addMediaToPlaylist(task.RequestUser, task.NavidromPlaylistID, matchedMediaID); err != nil {
				log.Error(nil, "Failed to add existing library song to Navidrome playlist", "error", err, "songName", task.CurrentSongTitle, "playlistId", task.NavidromPlaylistID, "mediaId", matchedMediaID)
				task.FailedSongs = append(task.FailedSongs, task.CurrentSongTitle)
				appendFailedSongDetail(task, song, classifyPlaylistSyncFailureReason(err, "入歌单失败"))
				task.Status = "syncing"
				task.UpdatedAt = time.Now()
				broadcastPlaylistSyncChange()
				continue
			}

			task.CompletedSongs = append(task.CompletedSongs, songID)
			task.CurrentSongReused = true
			if songID != "" {
				completed[songID] = true
			}
			task.RemainingCount = len(task.Songs) - len(task.CompletedSongs)
			task.Progress = int((len(task.CompletedSongs) * 100) / len(task.Songs))
			task.Status = "syncing"
			task.PauseRequested = false
			task.UpdatedAt = time.Now()
			log.Debug(nil, "Playlist sync reused existing library song", "taskID", task.ID, "songName", task.CurrentSongTitle, "mediaId", matchedMediaID, "progress", task.Progress, "remaining", task.RemainingCount)
			broadcastPlaylistSyncChange()
			continue
		}

		// Attempt to download with quality fallback
		downloadedFilePath, resolvedSourceName, err := downloadSongWithQualityFallback(
			song,
			sourceStr,
			task.QualityFallbackOrder,
			task,
		)
		if err != nil {
			if task.Status == "paused" || task.PauseRequested || errors.Is(err, context.Canceled) {
				task.Status = "canceled"
				task.CurrentSongTitle = "已取消"
				task.UpdatedAt = time.Now()
				broadcastPlaylistSyncChange()
				break
			}
			log.Error(nil, "Failed to download song for playlist sync after quality fallback", err, "songName", task.CurrentSongTitle)
			task.FailedSongs = append(task.FailedSongs, task.CurrentSongTitle)
			appendFailedSongDetail(task, song, classifyPlaylistSyncFailureReason(err, "下载源失败"))
			task.Status = "syncing"
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()
			continue
		}
		if resolvedSourceName != "" {
			task.SourceName = resolvedSourceName
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()
		}

		// Add the downloaded song to Navidrome playlist
		if err := addSongToNavidromPlaylist(task.RequestUser, task.NavidromPlaylistID, song, downloadedFilePath); err != nil {
			log.Error(nil, "Failed to add song to Navidrome playlist", "error", err, "songName", task.CurrentSongTitle, "playlistId", task.NavidromPlaylistID)
			task.FailedSongs = append(task.FailedSongs, task.CurrentSongTitle)
			appendFailedSongDetail(task, song, classifyPlaylistSyncFailureReason(err, "入库失败"))
			task.Status = "syncing"
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()
			continue
		}

		task.CompletedSongs = append(task.CompletedSongs, songID)
		if songID != "" {
			completed[songID] = true
		}
		task.RemainingCount = len(task.Songs) - len(task.CompletedSongs)
		task.Progress = int((len(task.CompletedSongs) * 100) / len(task.Songs))
		task.Status = "syncing"
		task.PauseRequested = false
		task.UpdatedAt = time.Now()
		log.Debug(nil, "Song downloaded and added", "taskID", task.ID, "progress", task.Progress, "remaining", task.RemainingCount)
		broadcastPlaylistSyncChange()
	}

	// Ensure final status is set correctly
	log.Debug(nil, "Sync loop finished", "taskID", task.ID, "status", task.Status, "remaining", task.RemainingCount, "completed", len(task.CompletedSongs), "total", len(task.Songs))

	if task.Status != "canceled" && task.Status != "paused" {
		if len(task.FailedSongDetails) > 0 {
			task.Status = "sync-error"
			task.CurrentSongTitle = "本轮完成，部分歌曲失败"
			task.RemainingCount = len(task.FailedSongDetails)
			log.Debug(nil, "Setting task to sync-error after full round", "taskID", task.ID, "failedCount", len(task.FailedSongDetails))
		} else {
			task.Status = "sync-completed"
			task.Progress = 100
			task.CurrentSongTitle = "完成"
			task.RemainingCount = 0
			log.Debug(nil, "Setting task to sync-completed", "taskID", task.ID)
		}
	}

	task.UpdatedAt = time.Now()
	log.Debug(nil, "Broadcasting final sync change", "taskID", task.ID, "finalStatus", task.Status, "finalProgress", task.Progress)
	broadcastPlaylistSyncChange()
	log.Debug(nil, "syncPlaylistSongs finished", "taskID", task.ID, "finalStatus", task.Status)
}

// waitForDownloadTaskCompletion waits for a download task to complete
func waitForDownloadTaskCompletion(downloadID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			task, ok := getOnlineDownloadTaskPointer(downloadID)
			if !ok {
				return fmt.Errorf("download task not found: %s", downloadID)
			}

			switch task.Status {
			case "completed":
				return nil
			case "failed":
				return fmt.Errorf("download failed: %s", task.Error)
			case "paused", "canceled":
				return fmt.Errorf("download %s: %s", task.Status, downloadID)
			}

			if time.Now().After(deadline) {
				return fmt.Errorf("download timeout: %s", downloadID)
			}

		case <-time.After(timeout):
			return fmt.Errorf("download timeout: %s", downloadID)
		}
	}
}

// getDownloadTaskFilePath retrieves the file path from a completed download task
func getDownloadTaskFilePath(downloadID string) string {
	task, ok := getOnlineDownloadTaskPointer(downloadID)
	if !ok {
		return ""
	}
	return task.FilePath
}

// addSongToNavidromPlaylist adds a downloaded song to the Navidrome playlist
func addSongToNavidromPlaylist(user model.User, playlistID string, song map[string]any, filePath string) error {
	// Import the audio file to Navidrome library with user context
	mediaID, err := importAudioFileToNavidrome(user, filePath)
	if err != nil {
		log.Error(nil, "Failed to import audio file to Navidrome", "error", err)
		return fmt.Errorf("import media failed: %w", err)
	}

	// Add the media to the playlist
	if err := addMediaToPlaylist(user, playlistID, mediaID); err != nil {
		log.Error(nil, "Failed to add media to playlist", "error", err)
		return fmt.Errorf("add media to playlist failed: %w", err)
	}

	log.Debug(nil, "Successfully added song to Navidrome playlist",
		"playlistId", playlistID,
		"songName", song["name"],
		"mediaId", mediaID,
	)

	return nil
}

// broadcastPlaylistSyncChange notifies all subscribers about playlist sync changes
func broadcastPlaylistSyncChange() {
	// Trigger the existing download task change event
	// which will notify the frontend about any changes
	broadcastDownloadTaskChange()
}

// getQualityFallbackOrder returns the fallback order for quality degradation
func getQualityFallbackOrder(preferredQuality string) []string {
	qualityOrder := []string{"flac24bit", "flac", "ape", "320k", "128k"}

	// If preferred quality is specified and in the list, start from there
	if preferredQuality != "" {
		result := []string{preferredQuality}
		for _, q := range qualityOrder {
			if q != preferredQuality {
				result = append(result, q)
			}
		}
		return result
	}

	return qualityOrder
}

func availableSongQualities(song map[string]any) []string {
	qualityMaps := []map[string]any{
		mapValue(song["qualitys"]),
		mapValue(song["_qualitys"]),
		mapValue(song["types"]),
		mapValue(song["_types"]),
		mapValue(mapValue(song["meta"])["qualitys"]),
		mapValue(mapValue(song["meta"])["_qualitys"]),
	}

	seen := map[string]bool{}
	result := make([]string, 0, 6)
	for _, m := range qualityMaps {
		for key, value := range m {
			if seen[key] {
				continue
			}
			if flag, ok := value.(bool); ok && flag {
				seen[key] = true
				result = append(result, key)
			}
		}
	}

	// Keep the list predictable in logs.
	sort.SliceStable(result, func(i, j int) bool {
		order := map[string]int{"master": 0, "flac24bit": 1, "ape": 2, "flac": 3, "320k": 4, "128k": 5}
		return order[result[i]] < order[result[j]]
	})
	return result
}

// downloadSongWithQualityFallback attempts to download a song with fallback quality options
func downloadSongWithQualityFallback(song map[string]any, sourceStr string, qualityOrder []string, task *playlistSyncTask) (string, string, error) {
	if task.Status == "paused" || task.PauseRequested {
		return "", "", context.Canceled
	}

	var lastErr error
	availableQualities := availableSongQualities(song)
	settings, settingsErr := loadOnlineSourceSettings()
	embedMode := defaultOnlineEmbedMode
	if settingsErr == nil {
		embedMode = settings.EmbedMode
	}
	downloadDir := strings.TrimSpace(task.DownloadPath)
	if downloadDir == "" {
		downloadDir = defaultOnlineDownloadPath()
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return "", "", err
	}
	candidates, err := loadEnabledSourcesForSong(sourceStr)
	if err != nil {
		return "", "", err
	}
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("未找到支持 %s 的启用音源脚本", sourceStr)
	}
	log.Debug(nil, "Playlist sync quality candidates",
		"songId", stringValue(song["id"]),
		"songName", stringValue(song["name"]),
		"source", sourceStr,
		"preferredQuality", func() string {
			if len(qualityOrder) > 0 {
				return qualityOrder[0]
			}
			return ""
		}(),
		"availableQualities", availableQualities,
		"rawQuality", song["quality"],
		"hasQualitys", mapValue(song["qualitys"]) != nil,
		"hasTypes", mapValue(song["types"]) != nil,
	)

	normalized := normalizeOnlineDownloadSongInfo(song)

	for _, quality := range qualityOrder {
		if task.Status == "paused" || task.PauseRequested {
			return "", "", context.Canceled
		}

		// If the song advertises available qualities, only try those.
		// If it does not advertise any, fall back to trying the full
		// order so we do not fail prematurely on sources that omit
		// quality metadata but still accept all quality arguments.
		if len(availableQualities) > 0 && !containsString(availableQualities, quality) {
			log.Debug(nil, "Skip unavailable quality for playlist sync", "songId", stringValue(song["id"]), "songName", stringValue(song["name"]), "quality", quality)
			continue
		}

		for _, candidate := range candidates {
			if task.Status == "paused" || task.PauseRequested {
				return "", "", context.Canceled
			}

			task.SourceName = candidate.Name
			task.Status = "resolving"
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()

			log.Debug(nil, "Playlist sync invoking lx script",
				"songId", stringValue(song["id"]),
				"songName", stringValue(song["name"]),
				"scriptName", candidate.Name,
				"quality", quality,
			)

			baseResolveCtx, baseResolveCancel := context.WithCancel(context.Background())
			task.CurrentCancel = baseResolveCancel
			resolveCtx, resolveCancel := context.WithTimeout(baseResolveCtx, onlineDownloadScriptTimeout*3)
			resolvedURL, resolvedHeaders, sourceName, resolveErr := resolveOnlineDownloadURLWithProgress(resolveCtx, candidate, sourceStr, normalized, quality)
			resolveCancel()
			baseResolveCancel()
			task.CurrentCancel = nil
			if resolveErr != nil {
				if task.Status == "paused" || task.PauseRequested || errors.Is(resolveErr, context.Canceled) {
					return "", "", context.Canceled
				}
				lastErr = resolveErr
				log.Debug(nil, "Playlist sync lx resolve failed", "scriptName", candidate.Name, "songName", stringValue(song["name"]), "quality", quality, "err", resolveErr.Error())
				continue
			}

			fileName := onlineDownloadFileName(normalized, quality, task.NameTemplate, resolvedURL)
			if !strings.Contains(fileName, ".") {
				fileName += ".mp3"
			}

			task.Status = "downloading"
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()

			baseDownloadCtx, baseDownloadCancel := context.WithCancel(context.Background())
			task.CurrentCancel = baseDownloadCancel
			downloadCtx, downloadCancel := context.WithTimeout(baseDownloadCtx, 10*time.Minute)
			result, fetchErr := fetchOnlineDownloadToTempFile(downloadCtx, resolvedURL, fileName, resolvedHeaders, nil)
			downloadCancel()
			baseDownloadCancel()
			task.CurrentCancel = nil
			if fetchErr != nil {
				if task.Status == "paused" || task.PauseRequested || errors.Is(fetchErr, context.Canceled) {
					_ = os.Remove(resultPathSafe(result))
					return "", "", context.Canceled
				}
				lastErr = fetchErr
				log.Debug(nil, "Playlist sync lx download failed", "scriptName", sourceName, "songName", stringValue(song["name"]), "quality", quality, "err", fetchErr.Error())
				_ = os.Remove(resultPathSafe(result))
				continue
			}

			finalPath := uniqueOnlineDownloadPath(downloadDir, fileName)
			if copyErr := copyFile(result.FilePath, finalPath); copyErr != nil {
				lastErr = copyErr
				_ = os.Remove(result.FilePath)
				log.Debug(nil, "Playlist sync copy to download dir failed", "songName", stringValue(song["name"]), "quality", quality, "path", finalPath, "err", copyErr.Error())
				continue
			}
			_ = os.Remove(result.FilePath)

			savedPath := finalPath
			if embedMode != embedModeNone {
				baseEmbedCtx, baseEmbedCancel := context.WithCancel(context.Background())
				task.CurrentCancel = baseEmbedCancel
				embedCtx, embedCancel := context.WithTimeout(baseEmbedCtx, 30*time.Second)
				embedTaskID := task.ID + "-" + stringValue(song["id"])
				coverRef := fetchAndPersistOnlineCover(embedCtx, downloadDir, embedTaskID, normalized)
				lyric := ""
				if embedMode == embedModeAll {
					lyric, _ = fetchOnlineEmbedLyric(embedCtx, candidate, sourceStr, normalized, quality)
				}
				if _, finalEmbedPath, embedErr := onlineEmbedDownloadMetadata(embedCtx, finalPath, normalized, quality, coverRef, lyric); embedErr != nil {
					log.Error(embedCtx, "Online embed: playlist sync embed failed", "task", task.ID, "songName", stringValue(song["name"]), "err", embedErr)
				} else if finalEmbedPath != "" {
					savedPath = finalEmbedPath
				}
				embedCancel()
				baseEmbedCancel()
				task.CurrentCancel = nil
				onlineEmbedCleanupArtwork(downloadDir)
			}

			log.Debug(nil, "Playlist sync lx download succeeded",
				"songName", stringValue(song["name"]),
				"scriptName", sourceName,
				"quality", quality,
				"filePath", savedPath,
			)
			return savedPath, sourceName, nil
		}
	}

	// All qualities failed
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", fmt.Errorf("no compatible quality found for download: available=%v requested=%v", availableQualities, qualityOrder)
}

func resultPathSafe(result *fetchedOnlineTempFile) string {
	if result == nil {
		return ""
	}
	return result.FilePath
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		_ = os.Remove(dstPath)
		return err
	}
	if err := dst.Sync(); err != nil {
		_ = os.Remove(dstPath)
		return err
	}
	return nil
}

// importAudioFileToNavidrome imports an audio file to Navidrome library and returns the media ID
func importAudioFileToNavidrome(user model.User, filePath string) (string, error) {
	if playlistImportService == nil {
		return "", fmt.Errorf("library import service is not configured")
	}

	ctx := context.Background()
	if user.ID != "" {
		ctx = request.WithUser(ctx, user)
	}
	mediaID, err := playlistImportService.ImportMediaFile(ctx, filePath)
	if err != nil {
		return "", err
	}

	log.Debug(nil, "Imported audio file to Navidrome", "filePath", filePath, "mediaId", mediaID, "userId", user.ID)
	return mediaID, nil
}

// addMediaToPlaylist adds a media ID to a Navidrome playlist
func addMediaToPlaylist(user model.User, playlistID string, mediaID string) error {
	if playlistSyncDataStore == nil {
		return fmt.Errorf("playlist datastore is not configured")
	}

	if playlistID == "" {
		return fmt.Errorf("playlist ID is empty")
	}

	if mediaID == "" {
		return fmt.Errorf("media ID is empty")
	}

	log.Debug(nil, "addMediaToPlaylist: starting", "playlistId", playlistID, "mediaId", mediaID, "userId", user.ID)

	ctx := context.Background()
	if user.ID != "" {
		ctx = request.WithUser(ctx, user)
	}
	playlistRepo := playlistSyncDataStore.Playlist(ctx)
	log.Debug(nil, "addMediaToPlaylist: got playlist repo", "playlistId", playlistID)

	pls, err := playlistRepo.GetWithTracks(playlistID, false, true)
	if err != nil {
		log.Error(nil, "addMediaToPlaylist: GetWithTracks failed", "error", err, "playlistId", playlistID, "userId", user.ID)
		return err
	}

	log.Debug(nil, "addMediaToPlaylist: got playlist", "playlistId", playlistID, "songCount", pls.SongCount)

	pls.AddMediaFilesByID([]string{mediaID})
	if err := playlistRepo.Put(pls); err != nil {
		log.Error(nil, "addMediaToPlaylist: Put failed", "error", err, "playlistId", playlistID, "mediaId", mediaID, "userId", user.ID)
		return err
	}

	log.Debug(nil, "Added media to playlist",
		"playlistId", playlistID,
		"mediaId", mediaID,
		"trackCount", pls.SongCount,
	)

	return nil
}

func findMatchingLibraryMediaID(user model.User, song map[string]any) (string, bool, error) {
	if playlistSyncDataStore == nil {
		return "", false, fmt.Errorf("playlist datastore is not configured")
	}

	name, singer, durationSec := playlistSyncSongInfoFields(song)
	if name == "" {
		log.Debug(nil, "Playlist sync skip matching: empty song name")
		return "", false, nil
	}

	ctx := context.Background()
	if user.ID != "" {
		ctx = request.WithUser(ctx, user)
	}

	query := strings.TrimSpace(strings.Join([]string{name, singer}, " "))
	log.Debug(nil, "Playlist sync searching library for matching song", "query", query, "songName", name, "songSinger", singer, "durationSec", durationSec)

	candidates, err := playlistSyncDataStore.MediaFile(ctx).Search(query, model.QueryOptions{Max: 20})
	if err != nil {
		log.Error(nil, "Playlist sync library search error (first attempt)", "query", query, "error", err)
		return "", false, err
	}
	log.Debug(nil, "Playlist sync first search result", "query", query, "candidates", len(candidates))

	if len(candidates) == 0 && singer != "" {
		log.Debug(nil, "Playlist sync retrying search without singer", "songName", name)
		candidates, err = playlistSyncDataStore.MediaFile(ctx).Search(name, model.QueryOptions{Max: 20})
		if err != nil {
			log.Error(nil, "Playlist sync library search error (second attempt)", "songName", name, "error", err)
			return "", false, err
		}
		log.Debug(nil, "Playlist sync second search result", "songName", name, "candidates", len(candidates))
	}

	best := playlistSyncBestLibraryMatch(song, candidates)
	if best == nil {
		log.Debug(nil, "Playlist sync no best match found", "songName", name, "songSinger", singer, "candidatesCount", len(candidates))
		return "", false, nil
	}

	log.Debug(nil, "Playlist sync matched existing library song",
		"songName", name,
		"songSinger", singer,
		"durationSec", durationSec,
		"mediaId", best.ID,
		"mediaTitle", best.Title,
		"mediaArtist", best.Artist,
		"mediaDuration", best.Duration,
	)
	return best.ID, true, nil
}

func playlistSyncBestLibraryMatch(song map[string]any, candidates model.MediaFiles) *model.MediaFile {
	wantName, wantSinger, wantDurationSec := playlistSyncSongInfoFields(song)
	wantNameNorm := onlineLyricNormalizeForMatch(wantName)
	wantSingerNorm := strings.TrimSpace(strings.ToLower(wantSinger))

	var best *model.MediaFile
	bestScore := -1
	for i := range candidates {
		candidate := &candidates[i]
		titleScore := onlineLyricSimilarity(onlineLyricNormalizeForMatch(candidate.Title), wantNameNorm)

		if titleScore < onlineLyricMatchTitleLooseWithArtistOrDuration {
			log.Debug(nil, "Playlist sync candidate rejected: title score too low",
				"candidateTitle", candidate.Title,
				"titleScore", titleScore,
				"threshold", onlineLyricMatchTitleLooseWithArtistOrDuration,
			)
			continue
		}

		artistName := candidate.Artist
		if artistName == "" {
			artistName = candidate.AlbumArtist
		}
		artistScore := 0
		if wantSingerNorm != "" && artistName != "" {
			artistScore = onlineLyricSimilarity(strings.ToLower(strings.TrimSpace(artistName)), wantSingerNorm)
		}

		hasDuration := wantDurationSec > 0 && candidate.Duration > 0
		durationDelta := 0
		if hasDuration {
			durationDelta = int(math.Abs(float64(int(math.Round(float64(candidate.Duration))) - wantDurationSec)))
			if durationDelta > onlineLyricMatchDurationToleranceSec {
				log.Debug(nil, "Playlist sync candidate rejected: duration mismatch",
					"candidateTitle", candidate.Title,
					"wantDuration", wantDurationSec,
					"candidateDuration", int(math.Round(float64(candidate.Duration))),
					"delta", durationDelta,
					"tolerance", onlineLyricMatchDurationToleranceSec,
				)
				continue
			}
		}

		matched := false
		score := titleScore
		switch {
		case titleScore >= onlineLyricMatchTitleMin && (wantSingerNorm == "" || artistScore >= onlineLyricMatchArtistMin) && (!hasDuration || durationDelta <= onlineLyricMatchDurationToleranceSec):
			matched = true
		case artistScore >= onlineLyricMatchArtistMin && titleScore >= onlineLyricMatchTitleLooseWithArtistOrDuration && (!hasDuration || durationDelta <= onlineLyricMatchDurationToleranceSec):
			matched = true
		case hasDuration && durationDelta <= onlineLyricMatchDurationStrongToleranceSec && titleScore >= onlineLyricMatchTitleLooseWithArtistOrDuration:
			matched = true
		case wantSingerNorm == "" && !hasDuration && titleScore >= onlineLyricMatchTitleOnlyMin:
			matched = true
		}
		if !matched {
			log.Debug(nil, "Playlist sync candidate rejected: matching rules",
				"candidateTitle", candidate.Title,
				"titleScore", titleScore,
				"artistScore", artistScore,
				"durationDelta", durationDelta,
				"hasDuration", hasDuration,
			)
			continue
		}

		score += artistScore
		if hasDuration {
			score += 100 - durationDelta*10
		}
		log.Debug(nil, "Playlist sync candidate matched",
			"candidateTitle", candidate.Title,
			"candidateArtist", artistName,
			"titleScore", titleScore,
			"artistScore", artistScore,
			"finalScore", score,
			"bestScore", bestScore,
		)
		if score > bestScore {
			best = candidate
			bestScore = score
		}
	}
	return best
}

func playlistSyncSongInfoFields(song map[string]any) (name, singer string, durationSec int) {
	name = strings.TrimSpace(stringValue(song["name"]))
	singer = strings.TrimSpace(stringValue(song["singer"]))
	if singer == "" {
		singer = strings.TrimSpace(stringValue(song["artist"]))
	}
	if singer == "" {
		singer = playlistSyncArtistsText(song["artists"])
	}
	if singer == "" {
		if meta := mapValue(song["meta"]); meta != nil {
			singer = strings.TrimSpace(stringValue(meta["singerName"]))
			if singer == "" {
				singer = strings.TrimSpace(stringValue(meta["artist"]))
			}
		}
	}
	durationSec = playlistSyncDurationSeconds(song)
	return name, singer, durationSec
}

func playlistSyncArtistsText(value any) string {
	artists, ok := value.([]any)
	if !ok || len(artists) == 0 {
		return ""
	}
	seen := map[string]struct{}{}
	names := make([]string, 0, len(artists))
	for _, item := range artists {
		var name string
		switch typed := item.(type) {
		case string:
			name = strings.TrimSpace(typed)
		case map[string]any:
			for _, key := range []string{"name", "artist", "singer"} {
				name = strings.TrimSpace(stringValue(typed[key]))
				if name != "" {
					break
				}
			}
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return strings.Join(names, " / ")
}

func playlistSyncDurationSeconds(song map[string]any) int {
	if sec := onlineLyricSecondsFromInterval(stringValue(song["interval"])); sec > 0 {
		return sec
	}
	for _, key := range []string{"duration", "dt"} {
		if sec := playlistSyncNumericDurationSeconds(song[key]); sec > 0 {
			return sec
		}
	}
	if meta := mapValue(song["meta"]); meta != nil {
		if sec := onlineLyricSecondsFromInterval(stringValue(meta["interval"])); sec > 0 {
			return sec
		}
		for _, key := range []string{"duration", "dt"} {
			if sec := playlistSyncNumericDurationSeconds(meta[key]); sec > 0 {
				return sec
			}
		}
	}
	return 0
}

func playlistSyncNumericDurationSeconds(value any) int {
	switch typed := value.(type) {
	case int:
		return playlistSyncDurationUnitToSeconds(float64(typed))
	case int32:
		return playlistSyncDurationUnitToSeconds(float64(typed))
	case int64:
		return playlistSyncDurationUnitToSeconds(float64(typed))
	case float32:
		return playlistSyncDurationUnitToSeconds(float64(typed))
	case float64:
		return playlistSyncDurationUnitToSeconds(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0
		}
		return playlistSyncDurationUnitToSeconds(parsed)
	default:
		return 0
	}
}

func playlistSyncDurationUnitToSeconds(value float64) int {
	if value <= 0 {
		return 0
	}
	if value >= 1000 {
		return int(math.Round(value / 1000))
	}
	return int(math.Round(value))
}
