package nativeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/log"
)

type playlistSyncTask struct {
	ID                    string
	NavidromPlaylistID    string
	PlaylistID            string
	PlaylistName          string
	PlaylistCover         string
	SourceType            string
	Status                string // syncing, sync-completed, sync-error
	Progress              int
	RemainingCount        int
	CurrentSongIndex      int
	CurrentSongTitle      string
	SourceName            string
	Songs                 []map[string]any
	CompletedSongs        []string // List of song IDs that have been added to Navidrome
	FailedSongs           []string
	DownloadQueue         []map[string]any
	DownloadTaskIDMap     map[string]int // Maps download task ID to song index
	CurrentDownloadID     string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	LastProgressUpdate    time.Time
	NameTemplate          []string
	DownloadPath          string
	PreferredQuality      string // User-selected quality
	QualityFallbackOrder  []string // Fallback order for quality degradation
	PauseRequested        bool
	CurrentCancel         context.CancelFunc
	Running               bool
}

type playlistSyncStartRequest struct {
	TaskID                string         `json:"taskId"`
	NavidromPlaylistID    string         `json:"navidromPlaylistId"`
	PlaylistName          string         `json:"playlistName,omitempty"`
	PlaylistCover         string         `json:"playlistCover,omitempty"`
	Songs                 []map[string]any `json:"songs"`
	PreferredQuality      string         `json:"preferredQuality,omitempty"`
}

type playlistSyncStatusResponse struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Progress         int    `json:"progress"`
	RemainingCount   int    `json:"remainingCount"`
	CurrentSongTitle string `json:"currentSongTitle"`
	SourceName       string `json:"sourceName"`
}

type playlistSyncTaskView struct {
	ID               string `json:"id"`
	TaskType         string `json:"taskType"`
	Title            string `json:"title"`
	Cover            string `json:"cover"`
	Status           string `json:"status"`
	Progress         int    `json:"progress"`
	RemainingCount   int    `json:"remainingCount"`
	CurrentSongTitle string `json:"currentSongTitle"`
	SourceName       string `json:"sourceName"`
	Source           string `json:"source"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

var playlistSyncTasks = struct {
	sync.RWMutex
	items map[string]*playlistSyncTask
}{items: map[string]*playlistSyncTask{}}

const playlistSyncTaskTTL = 2 * time.Hour

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
			ID:                   req.TaskID,
			NavidromPlaylistID:   req.NavidromPlaylistID,
			PlaylistName:         req.PlaylistName,
			PlaylistCover:        req.PlaylistCover,
			SourceType:           func() string {
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
			Songs:                req.Songs,
			DownloadTaskIDMap:    make(map[string]int),
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
			LastProgressUpdate:   time.Now(),
			DownloadPath:         downloadPath,
			NameTemplate:         nameTemplate,
			PreferredQuality:     req.PreferredQuality,
			QualityFallbackOrder: getQualityFallbackOrder(req.PreferredQuality),
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

	response := playlistSyncStatusResponse{
		ID:               task.ID,
		Status:           task.Status,
		Progress:         task.Progress,
		RemainingCount:   task.RemainingCount,
		CurrentSongTitle: task.CurrentSongTitle,
		SourceName:       task.SourceName,
	}

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
		title := task.PlaylistName
		if title == "" {
			title = "歌单同步"
		}
		source := task.SourceType
		if source == "" {
			source = "wy"
		}
		views = append(views, playlistSyncTaskView{
			ID:               task.ID,
			TaskType:         "playlist_sync",
			Title:            title,
			Cover:            task.PlaylistCover,
			Status:           task.Status,
			Progress:         task.Progress,
			RemainingCount:   task.RemainingCount,
			CurrentSongTitle: task.CurrentSongTitle,
			SourceName:       task.SourceName,
			Source:           source,
			CreatedAt:        task.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:        task.UpdatedAt.UTC().Format(time.RFC3339),
		})
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
		// Retry both failed and paused sync tasks.
		if task.Status != "sync-error" && task.Status != "paused" {
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
			task.Status = "paused"
			task.CurrentSongTitle = "已暂停"
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
		if task.Status == "sync-error" || task.Status == "paused" {
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

	log.Debug(nil, "syncPlaylistSongs started", "taskID", task.ID, "songCount", len(task.Songs))
	
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
		if task.Status == "sync-error" || task.Status == "paused" || task.PauseRequested {
			log.Debug(nil, "Sync error detected, breaking loop", "taskID", task.ID)
			break
		}

		songID := stringValue(song["id"])
		if songID != "" && completed[songID] {
			continue
		}

		task.CurrentSongIndex = idx
		task.CurrentSongTitle = stringValue(song["name"])
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

		// Attempt to download with quality fallback
		downloadedFilePath, resolvedSourceName, err := downloadSongWithQualityFallback(
			song,
			sourceStr,
			task.QualityFallbackOrder,
			task,
		)
		if err != nil {
			if task.Status == "paused" || task.PauseRequested || errors.Is(err, context.Canceled) {
				task.Status = "paused"
				task.CurrentSongTitle = "已暂停"
				task.UpdatedAt = time.Now()
				broadcastPlaylistSyncChange()
				break
			}
			log.Error(nil, "Failed to download song for playlist sync after quality fallback", err, "songName", task.CurrentSongTitle)
			task.FailedSongs = append(task.FailedSongs, task.CurrentSongTitle)
			task.Status = "sync-error"
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()
			break
		}
		if resolvedSourceName != "" {
			task.SourceName = resolvedSourceName
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()
		}

		// Add the downloaded song to Navidrome playlist
		if err := addSongToNavidromPlaylist(task.NavidromPlaylistID, song, downloadedFilePath); err != nil {
			log.Error(nil, "Failed to add song to Navidrome playlist", err, "songName", task.CurrentSongTitle)
			task.FailedSongs = append(task.FailedSongs, task.CurrentSongTitle)
			task.Status = "sync-error"
			task.UpdatedAt = time.Now()
			broadcastPlaylistSyncChange()
			break
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
	
	if task.Status == "syncing" {
		task.Status = "sync-completed"
		task.Progress = 100
		task.CurrentSongTitle = "完成"
		task.RemainingCount = 0
		log.Debug(nil, "Setting task to sync-completed", "taskID", task.ID)
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
func addSongToNavidromPlaylist(playlistID string, song map[string]any, filePath string) error {
	// Import the audio file to Navidrome library
	mediaID, err := importAudioFileToNavidrome(filePath)
	if err != nil {
		log.Error(nil, "Failed to import audio file to Navidrome", err)
		return err
	}

	// Add the media to the playlist
	if err := addMediaToPlaylist(playlistID, mediaID); err != nil {
		log.Error(nil, "Failed to add media to playlist", err)
		return err
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
func importAudioFileToNavidrome(filePath string) (string, error) {
	// TODO: Implement actual file import to Navidrome library
	// This should:
	// 1. Copy or scan the file into Navidrome's configured music folder
	// 2. Trigger library refresh if needed
	// 3. Return the generated media ID
	
	log.Debug(nil, "Importing audio file to Navidrome", "filePath", filePath)
	
	// Placeholder: simulate import and return a dummy media ID
	// In real implementation, this would interact with Navidrome's media library
	return fmt.Sprintf("import_%d", time.Now().UnixNano()), nil
}

// addMediaToPlaylist adds a media ID to a Navidrome playlist
func addMediaToPlaylist(playlistID string, mediaID string) error {
	// TODO: Implement actual API call to add media to playlist
	// This should call the Navidrome API: PUT /rest/updatePlaylist.view
	// with parameters: playlistId, songIdToAdd
	
	log.Debug(nil, "Adding media to playlist",
		"playlistId", playlistID,
		"mediaId", mediaID,
	)
	
	// Placeholder: in real implementation, this would make an HTTP call
	// to the Navidrome API endpoint
	return nil
}


