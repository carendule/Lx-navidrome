package nativeapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/server"
)

var onlineSourceMu sync.Mutex

type onlineSource struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Author           string   `json:"author"`
	Version          string   `json:"version"`
	Description      string   `json:"description,omitempty"`
	Homepage         string   `json:"homepage,omitempty"`
	Size             int64    `json:"size"`
	SupportedSources []string `json:"supportedSources,omitempty"`
	AllowUnsafeVM    bool     `json:"allowUnsafeVM,omitempty"`
	Enabled          bool     `json:"enabled"`
	EnabledOrder     int      `json:"enabledOrder,omitempty"`
	Status           string   `json:"status,omitempty"`
	SourceURL        string   `json:"sourceUrl,omitempty"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

type scriptMetadata struct {
	Name        string
	Author      string
	Version     string
	Description string
	Homepage    string
}

type onlineSourceUploadRequest struct {
	Filename      string `json:"filename"`
	Content       string `json:"content"`
	AllowUnsafeVM bool   `json:"allowUnsafeVM,omitempty"`
}

type onlineSourceImportRequest struct {
	URL           string `json:"url"`
	Filename      string `json:"filename"`
	AllowUnsafeVM bool   `json:"allowUnsafeVM,omitempty"`
}

type onlineSourceValidationError struct {
	message       string
	requireUnsafe bool
	disabledVM    bool
}

func (e *onlineSourceValidationError) Error() string {
	return e.message
}

type onlineSourceToggleRequest struct {
	Enabled *bool `json:"enabled"`
}

type onlineSourceReorderRequest struct {
	SourceIDs []string `json:"sourceIds"`
}

type onlineSourceSettings struct {
	DownloadPath string   `json:"downloadPath"`
	NameTemplate []string `json:"nameTemplate,omitempty"`
	// EmbedMode controls what the downloader writes into the
	// audio file's native tag container after a successful
	// download. The three legal values are:
	//
	//   "none"      — write nothing. The file plays as a plain
	//                 bitstream. Cheapest, no ffmpeg cost.
	//   "metadata"  — write cover art, title, artist, album,
	//                 and quality. The user gets a useful file
	//                 in their library but no lyrics.
	//   "all"       — also write lyrics. Reserved for a
	//                 follow-up; the embed pipeline accepts
	//                 this value today and behaves the same as
	//                 "metadata" until the lyric path lands.
	//
	// Requires ffmpeg on PATH for anything other than "none".
	// Defaults to "metadata" on fresh install; persisted
	// explicitly so users can opt out without losing the
	// choice on the next save.
	EmbedMode string `json:"embedMode"`
}

// embedModeNone / embedModeMetadata / embedModeAll are the legal
// values for onlineSourceSettings.EmbedMode. They're compared
// against the on-disk value with sanitizeEmbedMode, so a typo
// or a hand-edited settings.json silently falls back to
// embedModeMetadata rather than disabling embedding.
const (
	embedModeNone     = "none"
	embedModeMetadata = "metadata"
	embedModeAll      = "all"
)

// defaultOnlineEmbedMode is the value seeded into a fresh
// settings file. We pick "metadata" (not "all") because lyric
// fetching requires the source script to expose a lyric action
// and many of the popular lx-music scripts only ship musicUrl,
// so the lyric dispatch would be a wasted round-trip. Users
// who want lyrics can opt in via the settings panel.
const defaultOnlineEmbedMode = embedModeMetadata

// sanitizeEmbedMode normalizes the persisted EmbedMode value to
// one of the three legal strings. Any value that doesn't match
// the legal set (including empty string from a fresh install,
// "true"/"false" leftover from the pre-mode settings, or a
// typo) returns embedModeMetadata so the user keeps the most
// useful default rather than accidentally losing metadata.
func sanitizeEmbedMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case embedModeNone:
		return embedModeNone
	case embedModeAll:
		return embedModeAll
	case embedModeMetadata:
		return embedModeMetadata
	}
	return defaultOnlineEmbedMode
}

// defaultOnlineNameTemplate is the default value for
// onlineSourceSettings.NameTemplate. It is applied whenever the
// settings file is missing, empty, or pre-dates the field. Keep in
// sync with Online_setting.jsx NAME_TEMPLATE_DEFAULT.
var defaultOnlineNameTemplate = []string{"歌名", "歌手"}

// sanitizeOnlineNameTemplate filters an incoming NameTemplate slice
// down to the allowed token set, de-duplicates, and falls back to the
// default if the result is empty.
func sanitizeOnlineNameTemplate(in []string) []string {
	allowed := map[string]bool{"歌名": true, "歌手": true, "专辑": true, "来源": true, "音质": true}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		token := strings.TrimSpace(raw)
		if !allowed[token] || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	if len(out) == 0 {
		return append([]string{}, defaultOnlineNameTemplate...)
	}
	return out
}

func (api *Router) addOnlineSourceRoute(r chi.Router) {
	r.Route("/online/source", func(r chi.Router) {
		r.Get("/", api.listOnlineSources)
		r.Get("/settings", api.getOnlineSourceSettings)
		r.Post("/settings", api.saveOnlineSourceSettings)
		r.Post("/upload", api.uploadOnlineSource)
		r.Post("/import", api.importOnlineSource)
		r.Post("/reorder", api.reorderOnlineSources)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(server.URLParamsMiddleware)
			r.Post("/toggle", api.toggleOnlineSource)
			r.Delete("/", api.deleteOnlineSource)
		})
	})
}

func (api *Router) addOnlineSourceStatusRoute(r chi.Router) {
	r.Get("/online/source/status", api.onlineSourceStatus)
}

func (api *Router) onlineSourceStatus(w http.ResponseWriter, r *http.Request) {
	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	sources, err := loadOnlineSources()
	if err != nil {
		log.Error(r.Context(), "Could not load online source status", err)
		http.Error(w, "Could not load online source status", http.StatusInternalServerError)
		return
	}

	enabledCount := 0
	for _, source := range sources {
		if source.Enabled {
			enabledCount++
		}
	}

	writeJSON(w, map[string]any{
		"hasSources":         len(sources) > 0,
		"hasEnabledSource":   enabledCount > 0,
		"enabledSourceCount": enabledCount,
		"totalSourceCount":   len(sources),
	})
}

func (api *Router) listOnlineSources(w http.ResponseWriter, r *http.Request) {
	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	sources, err := loadOnlineSources()
	if err != nil {
		log.Error(r.Context(), "Could not load online sources", err)
		http.Error(w, "Could not load online sources", http.StatusInternalServerError)
		return
	}

	sources = normalizeOnlineSourcesOrder(sources)

	writeJSON(w, sources)
}

func (api *Router) getOnlineSourceSettings(w http.ResponseWriter, r *http.Request) {
	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		log.Error(r.Context(), "Could not load online source settings", err)
		http.Error(w, "Could not load online source settings", http.StatusInternalServerError)
		return
	}

	writeJSON(w, settings)
}

func (api *Router) saveOnlineSourceSettings(w http.ResponseWriter, r *http.Request) {
	var req onlineSourceSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	settings := onlineSourceSettings{
		DownloadPath: strings.TrimSpace(req.DownloadPath),
		NameTemplate: sanitizeOnlineNameTemplate(req.NameTemplate),
	}
	if settings.DownloadPath == "" {
		settings.DownloadPath = defaultOnlineDownloadPath()
	}

	// EmbedMode has its own UI on the settings panel now, so we
	// honor whatever the request supplied. The form sends a
	// sanitized string (one of "none" / "metadata" / "all"), so
	// sanitizeEmbedMode is belt-and-braces against a hand-edited
	// settings.json or a stale client. When the request OMITS
	// the field (the legacy "save download path" payload), we
	// preserve whatever the user previously chose so the Save
	// button can't clobber an unrelated setting. A fresh install
	// falls through to the default via loadOnlineSourceSettings.
	requestMode := strings.TrimSpace(req.EmbedMode)
	if requestMode == "" {
		existing, err := loadOnlineSourceSettings()
		if err != nil {
			settings.EmbedMode = defaultOnlineEmbedMode
		} else {
			settings.EmbedMode = existing.EmbedMode
		}
	} else {
		settings.EmbedMode = sanitizeEmbedMode(requestMode)
	}

	if err := saveOnlineSourceSettings(settings); err != nil {
		log.Error(r.Context(), "Could not save online source settings", err)
		http.Error(w, "Could not save online source settings", http.StatusInternalServerError)
		return
	}

	writeJSON(w, settings)
}

func (api *Router) uploadOnlineSource(w http.ResponseWriter, r *http.Request) {
	var req onlineSourceUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "Script content is required", http.StatusBadRequest)
		return
	}

	source, err := createOnlineSource(req.Filename, req.Content, "", req.AllowUnsafeVM)
	if err != nil {
		var validationErr *onlineSourceValidationError
		if errors.As(err, &validationErr) {
			writeJSON(w, map[string]any{
				"success":       false,
				"requireUnsafe": validationErr.requireUnsafe,
				"disabledVM":    validationErr.disabledVM,
				"message":       validationErr.message,
			})
			return
		}
		log.Error(r.Context(), "Could not upload online source", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, source)
}

func (api *Router) importOnlineSource(w http.ResponseWriter, r *http.Request) {
	var req onlineSourceImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(req.URL) //nolint:gosec
	if err != nil {
		http.Error(w, "Could not download script", http.StatusBadRequest)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Download failed: status %d", resp.StatusCode), http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Could not read downloaded script", http.StatusBadRequest)
		return
	}

	source, err := createOnlineSource(req.Filename, string(body), req.URL, req.AllowUnsafeVM)
	if err != nil {
		var validationErr *onlineSourceValidationError
		if errors.As(err, &validationErr) {
			writeJSON(w, map[string]any{
				"success":       false,
				"requireUnsafe": validationErr.requireUnsafe,
				"disabledVM":    validationErr.disabledVM,
				"message":       validationErr.message,
			})
			return
		}
		log.Error(r.Context(), "Could not import online source", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, source)
}

func (api *Router) toggleOnlineSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req onlineSourceToggleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	sources, err := loadOnlineSources()
	if err != nil {
		http.Error(w, "Could not load online sources", http.StatusInternalServerError)
		return
	}

	for i := range sources {
		if sources[i].ID != id {
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if req.Enabled == nil {
			sources[i].Enabled = !sources[i].Enabled
		} else {
			sources[i].Enabled = *req.Enabled
		}
		sources[i].UpdatedAt = now
		if sources[i].Enabled {
			sources[i].Status = "正常"
			sources[i].EnabledOrder = maxEnabledOrder(sources) + 1
		} else {
			sources[i].Status = "已禁用"
			sources[i].EnabledOrder = 0
		}

		sources = normalizeOnlineSourcesOrder(sources)

		if err := saveOnlineSources(sources); err != nil {
			http.Error(w, "Could not save online sources", http.StatusInternalServerError)
			return
		}
		writeJSON(w, sources[i])
		return
	}

	http.Error(w, "Source not found", http.StatusNotFound)
}

func (api *Router) deleteOnlineSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	sources, err := loadOnlineSources()
	if err != nil {
		http.Error(w, "Could not load online sources", http.StatusInternalServerError)
		return
	}

	filtered := make([]onlineSource, 0, len(sources))
	found := false
	for _, src := range sources {
		if src.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, src)
	}
	if !found {
		http.Error(w, "Source not found", http.StatusNotFound)
		return
	}

	if err := saveOnlineSources(filtered); err != nil {
		http.Error(w, "Could not save online sources", http.StatusInternalServerError)
		return
	}

	_ = os.Remove(filepath.Join(onlineScriptsDir(), id))
	w.WriteHeader(http.StatusNoContent)
}

func (api *Router) reorderOnlineSources(w http.ResponseWriter, r *http.Request) {
	var req onlineSourceReorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	sources, err := loadOnlineSources()
	if err != nil {
		http.Error(w, "Could not load online sources", http.StatusInternalServerError)
		return
	}

	byID := make(map[string]onlineSource, len(sources))
	for _, src := range sources {
		byID[src.ID] = src
	}

	enabledRank := map[string]int{}
	for i, id := range req.SourceIDs {
		src, ok := byID[id]
		if !ok || !src.Enabled {
			continue
		}
		enabledRank[id] = i + 1
	}

	nextRank := len(enabledRank) + 1
	for i := range sources {
		if !sources[i].Enabled {
			sources[i].EnabledOrder = 0
			continue
		}
		if rank, ok := enabledRank[sources[i].ID]; ok {
			sources[i].EnabledOrder = rank
			continue
		}
		sources[i].EnabledOrder = nextRank
		nextRank++
	}

	sources = normalizeOnlineSourcesOrder(sources)

	if err := saveOnlineSources(sources); err != nil {
		http.Error(w, "Could not save online sources", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]bool{"success": true})
}

func createOnlineSource(filename, content, sourceURL string, allowUnsafeVM bool) (onlineSource, error) {
	onlineSourceMu.Lock()
	defer onlineSourceMu.Unlock()

	if err := validateOnlineSourceScript(filename, content); err != nil {
		return onlineSource{}, err
	}

	sources, err := loadOnlineSources()
	if err != nil {
		return onlineSource{}, err
	}

	meta, execution := ValidateScriptWithMetadataOptions(content, allowUnsafeVM)
	if execution.RequireUnsafe {
		return onlineSource{}, &onlineSourceValidationError{
			message:       "该脚本需要原生 VM 模式运行，可能存在安全风险，是否继续？",
			requireUnsafe: true,
		}
	}
	if !execution.Valid {
		return onlineSource{}, fmt.Errorf("script validation failed: %s", execution.Error)
	}
	supportedSources := execution.Sources
	if len(supportedSources) == 0 {
		return onlineSource{}, fmt.Errorf("script validation failed: no supported sources registered")
	}

	// Generate display name (use script metadata if available, otherwise filename)
	displayName := getScriptDisplayName(meta, filename)

	id := generateSourceID(displayName, filename, sources)
	if id == "" {
		return onlineSource{}, fmt.Errorf("could not determine source id")
	}

	// Set default values for missing metadata
	if meta.Name == "" {
		meta.Name = displayName
	}
	if meta.Version == "" {
		meta.Version = "1.0.0"
	}
	if meta.Author == "" {
		meta.Author = "unknown"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	source := onlineSource{
		ID:               id,
		Name:             meta.Name,
		Author:           meta.Author,
		Version:          meta.Version,
		Description:      meta.Description,
		Homepage:         meta.Homepage,
		Size:             int64(len(content)),
		SupportedSources: supportedSources,
		AllowUnsafeVM:    allowUnsafeVM,
		Enabled:          false,
		EnabledOrder:     0,
		Status:           "已禁用",
		SourceURL:        sourceURL,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := os.MkdirAll(onlineScriptsDir(), 0o755); err != nil {
		return onlineSource{}, err
	}
	if err := os.WriteFile(filepath.Join(onlineScriptsDir(), id), []byte(content), 0o600); err != nil {
		return onlineSource{}, err
	}

	sources = append(sources, source)
	if err := saveOnlineSources(sources); err != nil {
		return onlineSource{}, err
	}

	return source, nil
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func onlineSourcesRoot() string {
	return filepath.Join(conf.Server.DataFolder.String(), "online-sources")
}

func onlineScriptsDir() string {
	return filepath.Join(onlineSourcesRoot(), "scripts")
}

func onlineMetaPath() string {
	return filepath.Join(onlineSourcesRoot(), "sources.json")
}

func onlineSettingsPath() string {
	return filepath.Join(onlineSourcesRoot(), "settings.json")
}

func defaultOnlineDownloadPath() string {
	return strings.TrimSpace(conf.Server.MusicFolder)
}

func loadOnlineSourceSettings() (onlineSourceSettings, error) {
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		return onlineSourceSettings{}, err
	}

	// defaultsForOnlineSourceSettings is the single source of
	// truth for "what does a fresh install look like?". We keep it
	// separate from the load function so we can re-apply it after
	// an Unmarshal on an old settings file that doesn't carry
	// every field we now expect — without this, JSON unmarshal
	// silently fills missing fields with Go's zero value (false /
	// "" / nil) and overrides any default the struct literal set.
	defaultsForOnlineSourceSettings := func() onlineSourceSettings {
		return onlineSourceSettings{
			DownloadPath: defaultOnlineDownloadPath(),
			NameTemplate: append([]string{}, defaultOnlineNameTemplate...),
			EmbedMode:    defaultOnlineEmbedMode,
		}
	}

	settings := defaultsForOnlineSourceSettings()
	path := onlineSettingsPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return settings, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return onlineSourceSettings{}, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return settings, nil
	}
	// Capture the on-disk JSON for two upgrade passes:
	//
	//   1. legacy "embedMetadata" boolean (pre-3-state field) — we
	//      translate true → "all" and false → "none" so the user's
	//      intent survives the schema migration.
	//   2. "embedMode" string is missing or carries an unknown
	//      value (typo, hand-edit) — sanitizeEmbedMode falls back
	//      to the default.
	rawOnDiskHasEmbedMode := bytes.Contains(b, []byte("embedMode"))
	rawOnDiskHasEmbedMetadata := bytes.Contains(b, []byte("embedMetadata"))
	// Decode the legacy field as a separate type so the unmarshal
	// can populate it without colliding with the new EmbedMode
	// string field on onlineSourceSettings. We then drop it from
	// the payload before unmarshaling the rest of the file.
	var legacy struct {
		EmbedMetadata *bool `json:"embedMetadata"`
	}
	if rawOnDiskHasEmbedMetadata {
		if lErr := json.Unmarshal(b, &legacy); lErr == nil &&
			legacy.EmbedMetadata != nil {
			// Only honor the legacy field when EmbedMode isn't
			// already in the file — a modern file with both is
			// considered authoritative on the new field.
			if !rawOnDiskHasEmbedMode {
				if *legacy.EmbedMetadata {
					settings.EmbedMode = embedModeAll
				} else {
					settings.EmbedMode = embedModeNone
				}
			}
		}
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		return onlineSourceSettings{}, err
	}
	// Normalize whatever the file said to one of the three legal
	// values. sanitizeEmbedMode also handles the case where
	// Unmarshal produced the zero value (empty string) because
	// the field was missing from the on-disk file.
	settings.EmbedMode = sanitizeEmbedMode(settings.EmbedMode)
	// If the file pre-dates the EmbedMode field, rewrite it
	// with the migrated value so the next load doesn't have to
	// repeat the dance. The on-disk rewrite is best-effort: a
	// failure here doesn't block the load.
	if !rawOnDiskHasEmbedMode {
		upgraded := onlineSourceSettings{
			DownloadPath: settings.DownloadPath,
			NameTemplate: settings.NameTemplate,
			EmbedMode:    settings.EmbedMode,
		}
		if buf, mErr := json.MarshalIndent(upgraded, "", "  "); mErr == nil {
			_ = os.WriteFile(onlineSettingsPath(), buf, 0o600)
		}
	}
	_ = rawOnDiskHasEmbedMetadata // kept for clarity of intent (legacy field detection)
	settings.DownloadPath = strings.TrimSpace(settings.DownloadPath)
	if settings.DownloadPath == "" {
		settings.DownloadPath = defaultOnlineDownloadPath()
	}
	settings.NameTemplate = sanitizeOnlineNameTemplate(settings.NameTemplate)
	return settings, nil
}

func saveOnlineSourceSettings(settings onlineSourceSettings) error {
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		return err
	}
	settings.DownloadPath = strings.TrimSpace(settings.DownloadPath)
	if settings.DownloadPath == "" {
		settings.DownloadPath = defaultOnlineDownloadPath()
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(onlineSettingsPath(), b, 0o600)
}

func loadOnlineSources() ([]onlineSource, error) {
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		return nil, err
	}
	metaPath := onlineMetaPath()
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		return []onlineSource{}, nil
	}
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return []onlineSource{}, nil
	}
	var sources []onlineSource
	if err := json.Unmarshal(b, &sources); err != nil {
		return nil, err
	}
	return sources, nil
}

func saveOnlineSources(sources []onlineSource) error {
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(onlineMetaPath(), b, 0o600)
}

func maxEnabledOrder(sources []onlineSource) int {
	maxOrder := 0
	for _, src := range sources {
		if src.Enabled && src.EnabledOrder > maxOrder {
			maxOrder = src.EnabledOrder
		}
	}
	return maxOrder
}

func normalizeOnlineSourcesOrder(sources []onlineSource) []onlineSource {
	type indexedSource struct {
		source onlineSource
		index  int
	}

	enabled := make([]indexedSource, 0, len(sources))
	disabled := make([]onlineSource, 0, len(sources))

	for i, src := range sources {
		if src.Enabled {
			enabled = append(enabled, indexedSource{source: src, index: i})
			continue
		}
		src.EnabledOrder = 0
		disabled = append(disabled, src)
	}

	sort.SliceStable(enabled, func(i, j int) bool {
		left := enabled[i]
		right := enabled[j]

		leftOrder := left.source.EnabledOrder
		rightOrder := right.source.EnabledOrder

		leftHasOrder := leftOrder > 0
		rightHasOrder := rightOrder > 0

		if leftHasOrder && rightHasOrder {
			if leftOrder == rightOrder {
				return left.index < right.index
			}
			return leftOrder < rightOrder
		}
		if leftHasOrder != rightHasOrder {
			return leftHasOrder
		}
		return left.index < right.index
	})

	reordered := make([]onlineSource, 0, len(sources))
	for i := range enabled {
		enabled[i].source.EnabledOrder = i + 1
		reordered = append(reordered, enabled[i].source)
	}
	reordered = append(reordered, disabled...)
	return reordered
}

func generateSourceID(name, filename string, existing []onlineSource) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = strings.TrimSpace(filename)
	}
	if base == "" {
		base = "source"
	}
	base = filepath.Base(base)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	re := regexp.MustCompile(`[\\/:*?"<>|]+`)
	base = strings.TrimSpace(re.ReplaceAllString(base, "_"))
	if base == "" {
		base = "source"
	}

	used := map[string]bool{}
	for _, src := range existing {
		used[src.ID] = true
	}

	candidate := base + ".js"
	if !used[candidate] {
		return candidate
	}
	for i := 2; i < 10000; i++ {
		candidate = fmt.Sprintf("%s_%d.js", base, i)
		if !used[candidate] {
			return candidate
		}
	}
	return ""
}

func validateOnlineSourceScript(filename, script string) error {
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(filename)), ".js") {
		return fmt.Errorf("script parse failed: only .js files are supported")
	}

	script = strings.TrimSpace(script)
	if script == "" {
		return fmt.Errorf("script parse failed: empty content")
	}

	return nil
}

func extractSupportedSources(script string) []string {
	set := map[string]bool{}

	// Find the start of sources/apis/qualitys objects
	reStart := regexp.MustCompile(`(?i)(sources|apis|qualitys)\s*[:=]\s*\{`)

	matches := reStart.FindAllStringIndex(script, -1)
	for _, match := range matches {
		startIdx := match[1] - 1 // Position of the opening brace

		// Find the matching closing brace by counting braces
		braceCount := 0
		endIdx := startIdx
		for i := startIdx; i < len(script); i++ {
			if script[i] == '{' {
				braceCount++
			} else if script[i] == '}' {
				braceCount--
				if braceCount == 0 {
					endIdx = i
					break
				}
			}
		}

		// Extract content between braces
		if endIdx > startIdx {
			content := script[startIdx+1 : endIdx]
			extractSourceKeysFromBlock(content, set)
		}
	}

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func extractSourceKeysFromBlock(blockContent string, set map[string]bool) {
	reKey := regexp.MustCompile(`['"]?([a-z0-9_]+)['"]?\s*:\s*\{`)
	keys := reKey.FindAllStringSubmatch(blockContent, -1)
	for _, k := range keys {
		if len(k) < 2 {
			continue
		}
		key := strings.TrimSpace(k[1])
		if key != "" && len(key) <= 10 {
			set[key] = true
		}
	}

	reDirectKey := regexp.MustCompile(`['"]([a-z0-9_]+)['"]\s*:\s*\{`)
	directKeys := reDirectKey.FindAllStringSubmatch(blockContent, -1)
	for _, k := range directKeys {
		if len(k) < 2 {
			continue
		}
		key := strings.TrimSpace(k[1])
		if key != "" && len(key) <= 10 {
			set[key] = true
		}
	}
}

// validateScriptExecution validates that the uploaded script is executable and safe.
// This is a placeholder for future JS execution validation.
// When ready, this can be extended to use goja or a similar JS engine to:
// 1. Run the script in a sandbox environment
// 2. Check for required functions (search, parse, etc.)
// 3. Verify script exports the expected API
