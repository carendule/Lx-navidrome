package nativeapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/id"
	"github.com/navidrome/navidrome/model/request"
)

const (
	mcpProtocolVersion = "2025-03-26"
	mcpServerName      = "navidrome-online-mcp"
	mcpCandidateTTL    = 30 * time.Minute
	mcpFlowSessionTTL  = 30 * time.Minute
	mcpMaxWaitTimeout  = 120
	mcpSearchCacheTTL  = 20 * time.Second
	mcpSearchTimeout   = 12 * time.Second
)

type mcpJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpJSONRPCResponse struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      any          `json:"id,omitempty"`
	Result  any          `json:"result,omitempty"`
	Error   *mcpRPCError `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpToolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpCandidate struct {
	ID       string
	UserName string
	SongInfo map[string]any
	Created  time.Time
}

type mcpFlowState string

const (
	mcpFlowStateSearching            mcpFlowState = "SEARCHING"
	mcpFlowStateWaitingUserSelection mcpFlowState = "WAITING_USER_SELECTION"
	mcpFlowStateConfirmed            mcpFlowState = "CONFIRMED"
	mcpFlowStateDownloading          mcpFlowState = "DOWNLOADING"
)

type mcpFlowSession struct {
	ID                string
	UserName          string
	State             mcpFlowState
	CandidateIDs      []string
	SelectedCandidate string
	ConfirmationToken string
	TaskID            string
	Created           time.Time
	Updated           time.Time
}

var mcpCandidates sync.Map
var mcpFlowSessions sync.Map
var mcpTranslationCache sync.Map

type mcpSearchCacheEntry struct {
	Result    map[string]any
	ExpiresAt time.Time
}

type mcpSearchInflightCall struct {
	Done   chan struct{}
	Result map[string]any
	Err    error
}

var mcpSearchState = struct {
	sync.Mutex
	Cache    map[string]mcpSearchCacheEntry
	Inflight map[string]*mcpSearchInflightCall
}{
	Cache:    map[string]mcpSearchCacheEntry{},
	Inflight: map[string]*mcpSearchInflightCall{},
}

func (api *Router) addMCPRoute(r chi.Router) {
	r.Post("/mcp", api.handleMCP)
}

func (api *Router) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mcpJSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.writeMCPError(w, nil, -32700, "invalid JSON body", err.Error())
		return
	}

	if req.JSONRPC != "2.0" {
		api.writeMCPError(w, req.ID, -32600, "invalid request", "jsonrpc must be 2.0")
		return
	}

	if err := api.verifyMCPToken(r); err != nil {
		api.writeMCPError(w, req.ID, -32001, "unauthorized", err.Error())
		return
	}

	resp := mcpJSONRPCResponse{JSONRPC: "2.0", ID: req.ID}
	result, rpcErr := api.handleMCPMethod(r, req)
	if req.ID == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

func (api *Router) handleMCPMethod(r *http.Request, req mcpJSONRPCRequest) (any, *mcpRPCError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    mcpServerName,
				"version": consts.Version,
			},
			"instructions": "Use tools ping, searchSongs, confirmDownload, startDownload, getDownloadStatus and waitDownload for online music workflows. Enforced flow: SEARCHING -> WAITING_USER_SELECTION -> CONFIRMED -> DOWNLOADING.",
		}, nil
	case "notifications/initialized":
		return map[string]any{}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools()}, nil
	case "tools/call":
		var params mcpToolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &mcpRPCError{Code: -32602, Message: "invalid params", Data: err.Error()}
		}
		return api.handleMCPToolCall(r, params)
	default:
		return nil, &mcpRPCError{Code: -32601, Message: "method not found", Data: req.Method}
	}
}

func mcpTools() []mcpTool {
	return []mcpTool{
		{
			Name:        "ping",
			Description: "Health check for Navidrome online MCP service.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "searchSongs",
			Description: "Search online songs from configured sources and create a confirmation session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"keyword": map[string]any{
						"type":        "string",
						"description": "Search keyword.",
					},
					"source": map[string]any{
						"type":        "string",
						"description": "Online source code: wy/tx/kg/kw/mg.",
						"default":     "wy",
					},
					"page": map[string]any{
						"type":        "integer",
						"description": "Result page number, starting from 1.",
						"minimum":     1,
						"default":     1,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max number of results per page.",
						"minimum":     1,
						"maximum":     50,
						"default":     10,
					},
					"includeRawSongInfo": map[string]any{
						"type":        "boolean",
						"description": "Whether to include raw source payload under candidates[].songInfo. Defaults to false for cleaner agent parsing.",
						"default":     false,
					},
				},
				"required": []string{"keyword"},
			},
		},
		{
			Name:        "confirmDownload",
			Description: "Confirm a candidate selected by the user. Returns a confirmationToken required by startDownload.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{
						"type":        "string",
						"description": "Session ID returned by searchSongs.",
					},
					"candidateId": map[string]any{
						"type":        "string",
						"description": "Candidate ID chosen by the user.",
					},
				},
				"required": []string{"sessionId", "candidateId"},
			},
		},
		{
			Name:        "startDownload",
			Description: "Start an asynchronous server-side online song download task after explicit confirmation.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"confirmationToken": map[string]any{
						"type":        "string",
						"description": "Confirmation token returned by confirmDownload.",
					},
					"quality": map[string]any{
						"type":        "string",
						"description": "Requested quality, defaults to best available.",
					},
					"nameTemplate": map[string]any{
						"type":        "array",
						"description": "Optional filename template token list.",
						"items":       map[string]any{"type": "string"},
					},
				},
				"required": []string{"confirmationToken"},
			},
		},
		{
			Name:        "getDownloadStatus",
			Description: "Get status of an online server download task by task ID.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"taskId": map[string]any{
						"type":        "string",
						"description": "Task ID returned by startDownload.",
					},
				},
				"required": []string{"taskId"},
			},
		},
		{
			Name:        "waitDownload",
			Description: "Wait for download completion up to timeout seconds.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"taskId": map[string]any{
						"type":        "string",
						"description": "Task ID returned by startDownload.",
					},
					"timeoutSec": map[string]any{
						"type":        "integer",
						"description": "Max seconds to wait before returning timeout.",
						"minimum":     1,
						"maximum":     mcpMaxWaitTimeout,
						"default":     10,
					},
					"pollMs": map[string]any{
						"type":        "integer",
						"description": "Polling interval in milliseconds.",
						"minimum":     100,
						"maximum":     5000,
						"default":     500,
					},
				},
				"required": []string{"taskId"},
			},
		},
	}
}

func (api *Router) handleMCPToolCall(r *http.Request, params mcpToolsCallParams) (any, *mcpRPCError) {
	switch params.Name {
	case "ping":
		structured := map[string]any{
			"ok":         true,
			"serverTime": time.Now().UTC().Format(time.RFC3339),
		}
		return mcpToolResult(mcpPingResponseText(structured), structured), nil
	case "searchSongs":
		return api.mcpSearchSongs(r, params.Arguments)
	case "confirmDownload":
		return api.mcpConfirmDownload(r, params.Arguments)
	case "startDownload":
		return api.mcpStartDownload(r, params.Arguments)
	case "getDownloadStatus":
		return api.mcpGetDownloadStatus(params.Arguments)
	case "waitDownload":
		return api.mcpWaitDownload(r, params.Arguments)
	default:
		return nil, &mcpRPCError{Code: -32602, Message: "unknown tool", Data: params.Name}
	}
}

func (api *Router) mcpSearchSongs(r *http.Request, args map[string]any) (any, *mcpRPCError) {
	startedAt := time.Now()
	keyword := strings.TrimSpace(asString(args["keyword"]))
	if keyword == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "keyword is required"}
	}

	source := strings.TrimSpace(asString(args["source"]))
	if source == "" {
		source = "wy"
	}

	page := asInt(args["page"], 1)
	if page < 1 {
		page = 1
	}
	limit := asInt(args["limit"], 10)
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	includeRawSongInfo := asBool(args["includeRawSongInfo"], false)
	requestKey := mcpSearchRequestKey(source, keyword, page, limit, includeRawSongInfo)

	result, fromCache, sharedInflight, err := api.mcpGetOrLoadSearchSongsResult(r, source, keyword, page, limit, includeRawSongInfo, requestKey)
	if err != nil {
		elapsedMs := time.Since(startedAt).Milliseconds()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			structured := map[string]any{
				"reason":       "search_timeout",
				"error":        err.Error(),
				"retryAfterMs": 1000,
				"requestKey":   requestKey,
				"elapsedMs":    elapsedMs,
			}
			return mcpToolErrorResult(mcpSearchErrorText("search timeout", structured), structured), nil
		}
		structured := map[string]any{
			"reason":     "search_failed",
			"error":      err.Error(),
			"requestKey": requestKey,
			"elapsedMs":  elapsedMs,
		}
		return mcpToolErrorResult(mcpSearchErrorText("search failed", structured), structured), nil
	}
	if fromCache || sharedInflight {
		username, _ := request.UsernameFrom(r.Context())
		mcpRefreshSearchResultSession(result, username)
	}

	result["requestKey"] = requestKey
	result["fromCache"] = fromCache
	result["sharedInflight"] = sharedInflight
	result["elapsedMs"] = time.Since(startedAt).Milliseconds()
	return mcpToolResult(mcpSearchResponseText(result), result), nil
}

func (api *Router) mcpGetOrLoadSearchSongsResult(r *http.Request, source, keyword string, page, limit int, includeRawSongInfo bool, requestKey string) (map[string]any, bool, bool, error) {
	now := time.Now()
	mcpSearchState.Lock()
	for key, item := range mcpSearchState.Cache {
		if now.After(item.ExpiresAt) {
			delete(mcpSearchState.Cache, key)
		}
	}
	if entry, ok := mcpSearchState.Cache[requestKey]; ok && now.Before(entry.ExpiresAt) {
		cached := cloneStringMapAny(entry.Result)
		mcpSearchState.Unlock()
		return cached, true, false, nil
	}
	if inFlight, ok := mcpSearchState.Inflight[requestKey]; ok {
		done := inFlight.Done
		mcpSearchState.Unlock()
		select {
		case <-done:
			if inFlight.Err != nil {
				return nil, false, true, inFlight.Err
			}
			return cloneStringMapAny(inFlight.Result), false, true, nil
		case <-r.Context().Done():
			return nil, false, true, r.Context().Err()
		}
	}
	call := &mcpSearchInflightCall{Done: make(chan struct{})}
	mcpSearchState.Inflight[requestKey] = call
	mcpSearchState.Unlock()

	searchCtx, cancel := context.WithTimeout(r.Context(), mcpSearchTimeout)
	defer cancel()
	result, err := api.mcpBuildSearchSongsResult(searchCtx, source, keyword, page, limit, includeRawSongInfo)

	mcpSearchState.Lock()
	delete(mcpSearchState.Inflight, requestKey)
	call.Result = cloneStringMapAny(result)
	call.Err = err
	if err == nil {
		mcpSearchState.Cache[requestKey] = mcpSearchCacheEntry{
			Result:    cloneStringMapAny(result),
			ExpiresAt: time.Now().Add(mcpSearchCacheTTL),
		}
	}
	close(call.Done)
	mcpSearchState.Unlock()

	if err != nil {
		log.Warn(r.Context(), "MCP searchSongs failed", "source", source, "keyword", keyword, "requestKey", requestKey, "err", err)
		return nil, false, false, err
	}
	return cloneStringMapAny(result), false, false, nil
}

func (api *Router) mcpBuildSearchSongsResult(ctx context.Context, source, keyword string, page, limit int, includeRawSongInfo bool) (map[string]any, error) {
	list, total, err := fetchOnlineSearchList(ctx, source, "song", keyword, page, limit, "")
	if err != nil {
		return nil, err
	}

	username, _ := request.UsernameFrom(ctx)
	candidates := make([]map[string]any, 0, len(list))
	searchTokens := tokenizeSearchKeyword(keyword)
	candidateIDs := make([]string, 0, len(list))
	type scoredCandidate struct {
		raw   map[string]any
		score int
	}
	scored := make([]scoredCandidate, 0, len(list))
	for i, item := range list {
		songInfo := cloneStringMap(item)
		if songInfo["source"] == nil || strings.TrimSpace(asString(songInfo["source"])) == "" {
			songInfo["source"] = source
		}
		name, singer := extractSongNameAndSinger(songInfo)
		if name != "" {
			songInfo["name"] = name
		}
		if singer != "" {
			songInfo["singer"] = singer
		}
		candidateID := saveMCPCandidate(username, songInfo)
		candidateMap := map[string]any{
			"candidateId": candidateID,
			"index":       i + 1,
			"name":        name,
			"singer":      singer,
			"songName":    name,
			"artist":      singer,
			"displayText": fmt.Sprintf("%d. %s - %s", i+1, singerOrUnknown(singer), titleOrUnknown(name)),
			"albumName":   asString(songInfo["albumName"]),
			"duration":    songInfo["duration"],
			"source":      asString(songInfo["source"]),
			"qualitys":    songInfo["qualitys"],
		}
		if includeRawSongInfo {
			candidateMap["songInfo"] = songInfo
		}
		score := computeKeywordRelevance(searchTokens, name, singer, asString(songInfo["albumName"]))
		if len(searchTokens) == 0 || score > 0 {
			candidateIDs = append(candidateIDs, candidateID)
			scored = append(scored, scoredCandidate{raw: candidateMap, score: score})
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			ii := asInt(scored[i].raw["index"], 0)
			jj := asInt(scored[j].raw["index"], 0)
			return ii < jj
		}
		return scored[i].score > scored[j].score
	})

	for idx, entry := range scored {
		entry.raw["index"] = idx + 1
		entry.raw["displayText"] = fmt.Sprintf("%d. %s - %s", idx+1, singerOrUnknown(asString(entry.raw["artist"])), titleOrUnknown(asString(entry.raw["songName"])))
		candidates = append(candidates, entry.raw)
	}

	flowSessionID := createMCPFlowSession(username, candidateIDs)

	return map[string]any{
		"keyword":              keyword,
		"source":               source,
		"page":                 page,
		"limit":                limit,
		"total":                total,
		"candidates":           candidates,
		"matchedCandidates":    len(candidates),
		"candidatesFormatHint": "Prefer candidate.songName/artist; name/singer are backward-compatible aliases. Raw songInfo is omitted by default.",
		"flow": map[string]any{
			"sessionId":             flowSessionID,
			"state":                 mcpFlowStateWaitingUserSelection,
			"requiresUserSelection": true,
			"nextAction":            "Ask user to choose a candidate, then call confirmDownload",
		},
	}, nil
}

func (api *Router) mcpConfirmDownload(r *http.Request, args map[string]any) (any, *mcpRPCError) {
	sessionID := strings.TrimSpace(asString(args["sessionId"]))
	if sessionID == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "sessionId is required"}
	}
	candidateID := strings.TrimSpace(asString(args["candidateId"]))
	if candidateID == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "candidateId is required"}
	}

	username, _ := request.UsernameFrom(r.Context())
	session, err := confirmMCPFlowCandidate(sessionID, candidateID, username)
	if err != nil {
		structured := map[string]any{"sessionId": sessionID, "candidateId": candidateID, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("confirmation failed", structured, "next: call searchSongs again, then confirmDownload with returned sessionId + candidateId"), structured), nil
	}

	songInfo, err := loadMCPCandidate(candidateID, username)
	if err != nil {
		structured := map[string]any{"candidateId": candidateID, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("candidate unavailable", structured, "next: call searchSongs again and choose another candidate"), structured), nil
	}
	songName, singer := extractSongNameAndSinger(songInfo)

	result := map[string]any{
		"sessionId":           session.ID,
		"state":               session.State,
		"confirmationToken":   session.ConfirmationToken,
		"selectedCandidateId": candidateID,
		"songName":            songName,
		"artist":              singer,
		"source":              asString(songInfo["source"]),
		"nextAction":          "Call startDownload with confirmationToken",
	}
	return mcpToolResult(mcpConfirmResponseText(result), result), nil
}

func (api *Router) mcpStartDownload(r *http.Request, args map[string]any) (any, *mcpRPCError) {
	username, _ := request.UsernameFrom(r.Context())
	confirmationToken := strings.TrimSpace(asString(args["confirmationToken"]))
	if confirmationToken == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "confirmationToken is required. Call confirmDownload after user selection."}
	}

	flowSession, err := loadMCPFlowSessionByToken(confirmationToken, username)
	if err != nil {
		structured := map[string]any{"confirmationToken": confirmationToken, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("invalid confirmation", structured, "next: call confirmDownload to get a fresh confirmationToken"), structured), nil
	}
	if flowSession.State != mcpFlowStateConfirmed {
		structured := map[string]any{"sessionId": flowSession.ID, "state": flowSession.State}
		return mcpToolErrorResult(mcpErrorText("flow not confirmed", structured, "next: call confirmDownload first, then retry startDownload"), structured), nil
	}
	if strings.TrimSpace(flowSession.SelectedCandidate) == "" {
		structured := map[string]any{"sessionId": flowSession.ID}
		return mcpToolErrorResult(mcpErrorText("flow missing selected candidate", structured, "next: call confirmDownload with a candidateId"), structured), nil
	}

	songInfo, err := loadMCPCandidate(flowSession.SelectedCandidate, username)
	if err != nil {
		structured := map[string]any{"sessionId": flowSession.ID, "candidateId": flowSession.SelectedCandidate, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("invalid candidate", structured, "next: call searchSongs and confirmDownload again for a fresh candidate"), structured), nil
	}

	if reusedMediaID, exists, checkErr := mcpFindExistingLibraryMediaID(r.Context(), songInfo); checkErr != nil {
		log.Warn(r.Context(), "MCP library duplicate check failed", "err", checkErr)
	} else if exists {
		name, singer := extractSongNameAndSinger(songInfo)
		reasonKey := "online.error.song_already_exists_in_library"
		reasonText := mcpTranslateByRequest(r, reasonKey, "song already exists in library")
		structured := map[string]any{
			"reason":          "already_exists",
			"reasonKey":       reasonKey,
			"reasonLocalized": reasonText,
			"songName":        name,
			"artist":          singer,
			"mediaId":         reusedMediaID,
		}
		return mcpToolErrorResult(mcpErrorText(reasonText, structured, "next: ask user to play existing song or pick another candidate"), structured), nil
	}

	songSource := strings.TrimSpace(asString(songInfo["source"]))
	if songSource == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "songInfo.source is required"}
	}

	quality := strings.TrimSpace(asString(args["quality"]))
	if quality == "" {
		quality = bestOnlineDownloadQuality(songInfo)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		structured := map[string]any{"error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("could not load online settings", structured, "next: check online source configuration"), structured), nil
	}
	downloadDir := strings.TrimSpace(settings.DownloadPath)
	if downloadDir == "" {
		downloadDir = defaultOnlineDownloadPath()
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		structured := map[string]any{"error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("could not create download directory", structured, "next: verify directory permissions and free space"), structured), nil
	}

	normalized := normalizeOnlineDownloadSongInfo(songInfo)
	nameTemplate := settings.NameTemplate
	if rawTemplate, ok := args["nameTemplate"].([]any); ok && len(rawTemplate) > 0 {
		nameTemplate = make([]string, 0, len(rawTemplate))
		for _, entry := range rawTemplate {
			token := strings.TrimSpace(asString(entry))
			if token != "" {
				nameTemplate = append(nameTemplate, token)
			}
		}
	}
	if rawTemplate, ok := args["nameTemplate"].([]string); ok && len(rawTemplate) > 0 {
		nameTemplate = make([]string, 0, len(rawTemplate))
		for _, entry := range rawTemplate {
			token := strings.TrimSpace(asString(entry))
			if token != "" {
				nameTemplate = append(nameTemplate, token)
			}
		}
	}

	taskID := createOnlineServerDownloadTask(normalized, songSource, quality, downloadDir, nameTemplate)
	go runOnlineServerDownloadTask(taskID) //nolint:gosec
	if err := markMCPFlowSessionDownloading(flowSession.ID, taskID, username); err != nil {
		log.Warn(r.Context(), "MCP flow session update failed", "sessionId", flowSession.ID, "taskId", taskID, "err", err)
	}

	result := map[string]any{
		"taskId":        taskID,
		"status":        "queued",
		"quality":       quality,
		"source":        songSource,
		"songName":      asString(normalized["name"]),
		"flowSessionId": flowSession.ID,
		"flowState":     mcpFlowStateDownloading,
	}
	return mcpToolResult(mcpStartDownloadResponseText(result), result), nil
}

func (api *Router) mcpGetDownloadStatus(args map[string]any) (any, *mcpRPCError) {
	taskID := strings.TrimSpace(asString(args["taskId"]))
	if taskID == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "taskId is required"}
	}
	task, ok := getOnlineDownloadTask(taskID)
	if !ok {
		structured := map[string]any{"taskId": taskID}
		return mcpToolErrorResult(mcpErrorText("task not found", structured, "next: ensure taskId comes from startDownload"), structured), nil
	}
	status := mcpDownloadStatusFromTask(task)
	return mcpToolResult(mcpDownloadStatusResponseText("status retrieved", status), status), nil
}

func (api *Router) mcpWaitDownload(r *http.Request, args map[string]any) (any, *mcpRPCError) {
	taskID := strings.TrimSpace(asString(args["taskId"]))
	if taskID == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "taskId is required"}
	}

	timeoutSec := asInt(args["timeoutSec"], 10)
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	if timeoutSec > mcpMaxWaitTimeout {
		timeoutSec = mcpMaxWaitTimeout
	}
	pollMs := asInt(args["pollMs"], 500)
	if pollMs < 100 {
		pollMs = 100
	}
	if pollMs > 5000 {
		pollMs = 5000
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Duration(pollMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		task, ok := getOnlineDownloadTask(taskID)
		if !ok {
			structured := map[string]any{"taskId": taskID}
			return mcpToolErrorResult(mcpErrorText("task not found", structured, "next: ensure taskId comes from startDownload"), structured), nil
		}
		status := mcpDownloadStatusFromTask(task)
		switch task.Status {
		case "completed":
			status["waitResult"] = "completed"
			return mcpToolResult(mcpDownloadStatusResponseText("download completed", status), status), nil
		case "failed", "canceled":
			status["waitResult"] = "failed"
			return mcpToolErrorResult(mcpDownloadStatusResponseText("download failed", status), status), nil
		}

		select {
		case <-ctx.Done():
			status["waitResult"] = "timeout"
			return mcpToolResult(mcpDownloadStatusResponseText("download still in progress", status), status), nil
		case <-ticker.C:
		}
	}
}

func saveMCPCandidate(username string, songInfo map[string]any) string {
	key := "cand_" + id.NewRandom()
	mcpCandidates.Store(key, mcpCandidate{
		ID:       key,
		UserName: username,
		SongInfo: cloneStringMap(songInfo),
		Created:  time.Now(),
	})
	return key
}

func createMCPFlowSession(username string, candidateIDs []string) string {
	now := time.Now()
	sessionID := "mcpfs_" + id.NewRandom()
	mcpFlowSessions.Store(sessionID, mcpFlowSession{
		ID:           sessionID,
		UserName:     username,
		State:        mcpFlowStateWaitingUserSelection,
		CandidateIDs: append([]string(nil), candidateIDs...),
		Created:      now,
		Updated:      now,
	})
	return sessionID
}

func loadMCPFlowSession(sessionID, username string) (mcpFlowSession, error) {
	raw, ok := mcpFlowSessions.Load(sessionID)
	if !ok {
		return mcpFlowSession{}, errors.New("flow session not found")
	}
	session := raw.(mcpFlowSession)
	if time.Since(session.Updated) > mcpFlowSessionTTL {
		mcpFlowSessions.Delete(sessionID)
		return mcpFlowSession{}, errors.New("flow session expired")
	}
	if session.UserName != "" && username != "" && session.UserName != username {
		return mcpFlowSession{}, errors.New("flow session belongs to another user")
	}
	return session, nil
}

func confirmMCPFlowCandidate(sessionID, candidateID, username string) (mcpFlowSession, error) {
	session, err := loadMCPFlowSession(sessionID, username)
	if err != nil {
		return mcpFlowSession{}, err
	}
	if session.State != mcpFlowStateWaitingUserSelection {
		return mcpFlowSession{}, fmt.Errorf("flow state must be %s", mcpFlowStateWaitingUserSelection)
	}
	if !containsCandidateID(session.CandidateIDs, candidateID) {
		return mcpFlowSession{}, errors.New("candidateId not in flow session")
	}
	now := time.Now()
	session.SelectedCandidate = candidateID
	session.ConfirmationToken = "mcpct_" + id.NewRandom()
	session.State = mcpFlowStateConfirmed
	session.Updated = now
	mcpFlowSessions.Store(session.ID, session)
	return session, nil
}

func loadMCPFlowSessionByToken(confirmationToken, username string) (mcpFlowSession, error) {
	var hit mcpFlowSession
	found := false
	mcpFlowSessions.Range(func(_, value any) bool {
		session := value.(mcpFlowSession)
		if strings.TrimSpace(session.ConfirmationToken) == confirmationToken {
			hit = session
			found = true
			return false
		}
		return true
	})
	if !found {
		return mcpFlowSession{}, errors.New("confirmation token not found")
	}
	return loadMCPFlowSession(hit.ID, username)
}

func markMCPFlowSessionDownloading(sessionID, taskID, username string) error {
	session, err := loadMCPFlowSession(sessionID, username)
	if err != nil {
		return err
	}
	if session.State != mcpFlowStateConfirmed {
		return fmt.Errorf("flow state must be %s", mcpFlowStateConfirmed)
	}
	session.State = mcpFlowStateDownloading
	session.TaskID = taskID
	session.ConfirmationToken = ""
	session.Updated = time.Now()
	mcpFlowSessions.Store(session.ID, session)
	return nil
}

func containsCandidateID(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func loadMCPCandidate(candidateID, username string) (map[string]any, error) {
	raw, ok := mcpCandidates.Load(candidateID)
	if !ok {
		return nil, errors.New("candidate not found")
	}
	candidate := raw.(mcpCandidate)
	if time.Since(candidate.Created) > mcpCandidateTTL {
		mcpCandidates.Delete(candidateID)
		return nil, errors.New("candidate expired")
	}
	if candidate.UserName != "" && username != "" && candidate.UserName != username {
		return nil, errors.New("candidate belongs to another user")
	}
	return cloneStringMap(candidate.SongInfo), nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case float64:
		return fmt.Sprintf("%g", t)
	case int:
		return fmt.Sprintf("%d", t)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func asInt(v any, fallback int) int {
	switch t := v.(type) {
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return fallback
}

func asBool(v any, fallback bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		normalized := strings.ToLower(strings.TrimSpace(t))
		switch normalized {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return fallback
}

func extractSongNameAndSinger(songInfo map[string]any) (string, string) {
	meta, _ := songInfo["meta"].(map[string]any)
	name := pickBestDisplayString(
		asString(songInfo["name"]),
		asString(songInfo["songName"]),
		asString(songInfo["title"]),
		asString(songInfo["title_main"]),
		asString(songInfo["search_title"]),
		asString(meta["name"]),
		asString(meta["title"]),
		asString(meta["title_main"]),
	)
	singer := pickBestDisplayString(
		asString(songInfo["singer"]),
		asString(songInfo["artist"]),
		asString(songInfo["author"]),
		joinNamesFromArray(meta, "singer"),
		joinNamesFromArray(meta, "ar"),
	)

	if name == "" {
		name = "Unknown song"
	}
	if singer == "" {
		singer = "Unknown artist"
	}
	return name, singer
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		v = normalizeDisplayString(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func pickBestDisplayString(values ...string) string {
	best := ""
	bestScore := -1 << 30
	for _, raw := range values {
		v := normalizeDisplayString(raw)
		if v == "" {
			continue
		}
		score := textScore(v)
		if looksLikeMojibake(v) {
			score -= 6
		}
		if score > bestScore {
			best = v
			bestScore = score
		}
	}
	if best != "" {
		return best
	}
	return firstNonEmptyString(values...)
}

func joinNamesFromArray(container map[string]any, key string) string {
	if container == nil {
		return ""
	}
	raw, ok := container[key]
	if !ok {
		return ""
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := normalizeDisplayString(asString(obj["name"]))
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "/")
}

func normalizeDisplayString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return maybeRepairUTF8Mojibake(s)
}

func maybeRepairUTF8Mojibake(s string) string {
	if !looksLikeMojibake(s) {
		return s
	}
	buf := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 255 {
			return s
		}
		buf = append(buf, byte(r))
	}
	if !utf8.Valid(buf) {
		return s
	}
	repaired := strings.TrimSpace(string(buf))
	if repaired == "" {
		return s
	}
	if textScore(repaired) >= textScore(s)+2 {
		return repaired
	}
	return s
}

func looksLikeMojibake(s string) bool {
	if s == "" {
		return false
	}
	if hasCJK(s) {
		return false
	}
	for _, r := range s {
		if r == 'Ã' || r == 'Â' || r == 'å' || r == 'æ' || r == 'ç' || r == 'è' || r == 'é' || r == 'ê' || r == 'ï' || r == 'ð' || r == 'ò' || r == 'ó' || r == 'ô' || r == 'ù' || r == 'ú' || r == 'û' || r == 'ý' {
			return true
		}
	}
	return false
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func textScore(s string) int {
	score := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			score += 3
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			score += 1
		case r == '�':
			score -= 4
		case strings.ContainsRune("/ -_()[]{}&.,'\"", r):
			score += 0
		default:
			score -= 1
		}
	}
	return score
}

func titleOrUnknown(name string) string {
	if strings.TrimSpace(name) == "" {
		return "Unknown song"
	}
	return name
}

func singerOrUnknown(singer string) string {
	if strings.TrimSpace(singer) == "" {
		return "Unknown artist"
	}
	return singer
}

func mcpFindExistingLibraryMediaID(ctx context.Context, songInfo map[string]any) (string, bool, error) {
	var user model.User
	if u, ok := request.UserFrom(ctx); ok {
		user = u
	}
	mediaID, matched, err := findMatchingLibraryMediaID(user, songInfo)
	if err != nil {
		return "", false, err
	}
	if !matched {
		return "", false, nil
	}
	return mediaID, true, nil
}

func mcpTranslateByRequest(r *http.Request, key, fallback string) string {
	if r == nil {
		return fallback
	}
	locale := mcpLocaleFromRequest(r)
	return mcpTranslate(locale, key, fallback)
}

func mcpTranslate(locale, key, fallback string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" || locale == "en" {
		return fallback
	}
	translations, _ := loadTranslations()
	tr, ok := translations[locale]
	if !ok {
		return fallback
	}
	loaded, ok := mcpTranslationCache.Load(locale)
	if !ok {
		obj := map[string]any{}
		if err := json.Unmarshal([]byte(tr.Data), &obj); err != nil {
			return fallback
		}
		mcpTranslationCache.Store(locale, obj)
		loaded = obj
	}
	obj, ok := loaded.(map[string]any)
	if !ok {
		return fallback
	}
	if v, ok := lookupNestedString(obj, key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func mcpLocaleFromRequest(r *http.Request) string {
	if r == nil {
		return "en"
	}
	if lang := strings.TrimSpace(r.URL.Query().Get("lang")); lang != "" {
		return normalizeMCPPreferredLocale(lang)
	}
	if lang := strings.TrimSpace(r.Header.Get("X-ND-Locale")); lang != "" {
		return normalizeMCPPreferredLocale(lang)
	}
	if lang := strings.TrimSpace(r.Header.Get("Accept-Language")); lang != "" {
		parts := strings.Split(lang, ",")
		if len(parts) > 0 {
			return normalizeMCPPreferredLocale(parts[0])
		}
		return normalizeMCPPreferredLocale(lang)
	}
	return "en"
}

func normalizeMCPPreferredLocale(raw string) string {
	lang := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(lang, ";"); i >= 0 {
		lang = strings.TrimSpace(lang[:i])
	}
	if lang == "" {
		return "en"
	}
	if strings.HasPrefix(lang, "zh") {
		if strings.Contains(lang, "hant") || strings.Contains(lang, "tw") || strings.Contains(lang, "hk") || strings.Contains(lang, "mo") {
			return "zh-Hant"
		}
		return "zh-Hans"
	}
	return "en"
}

func lookupNestedString(root map[string]any, dottedKey string) (string, bool) {
	if root == nil {
		return "", false
	}
	parts := strings.Split(strings.TrimSpace(dottedKey), ".")
	if len(parts) == 0 {
		return "", false
	}
	var cur any = root
	for _, p := range parts {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		next, ok := obj[p]
		if !ok {
			return "", false
		}
		cur = next
	}
	v, ok := cur.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func tokenizeSearchKeyword(keyword string) []string {
	parts := strings.Fields(strings.ToLower(strings.TrimSpace(keyword)))
	if len(parts) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		tokens = append(tokens, p)
	}
	return tokens
}

func mcpSearchRequestKey(source, keyword string, page, limit int, includeRawSongInfo bool) string {
	return strings.ToLower(strings.TrimSpace(source)) + "|" + strings.ToLower(strings.TrimSpace(keyword)) + "|" + strconv.Itoa(page) + "|" + strconv.Itoa(limit) + "|" + strconv.FormatBool(includeRawSongInfo)
}

func mcpSearchCandidateCount(raw any) int {
	if items, ok := raw.([]map[string]any); ok {
		return len(items)
	}
	if arr, ok := raw.([]any); ok {
		return len(arr)
	}
	return 0
}

func mcpSearchResponseText(result map[string]any) string {
	count := mcpSearchCandidateCount(result["candidates"])
	sessionID := ""
	if flow, ok := result["flow"].(map[string]any); ok {
		sessionID = strings.TrimSpace(asString(flow["sessionId"]))
	}
	b := strings.Builder{}
	b.WriteString(fmt.Sprintf("found %d candidates", count))
	if sessionID != "" {
		b.WriteString("\n")
		b.WriteString("sessionId: ")
		b.WriteString(sessionID)
	}

	candidatesAny, ok := result["candidates"].([]any)
	if !ok || len(candidatesAny) == 0 {
		if cands, ok2 := result["candidates"].([]map[string]any); ok2 {
			candidatesAny = make([]any, 0, len(cands))
			for _, c := range cands {
				candidatesAny = append(candidatesAny, c)
			}
		}
	}
	if len(candidatesAny) == 0 {
		return b.String()
	}

	b.WriteString("\n")
	b.WriteString("candidates:")
	maxLines := len(candidatesAny)
	if maxLines > 10 {
		maxLines = 10
	}
	for i := 0; i < maxLines; i++ {
		candidate, ok := candidatesAny[i].(map[string]any)
		if !ok {
			continue
		}
		index := asInt(candidate["index"], i+1)
		artist := strings.TrimSpace(asString(candidate["artist"]))
		if artist == "" {
			artist = strings.TrimSpace(asString(candidate["singer"]))
		}
		title := strings.TrimSpace(asString(candidate["songName"]))
		if title == "" {
			title = strings.TrimSpace(asString(candidate["name"]))
		}
		candidateID := strings.TrimSpace(asString(candidate["candidateId"]))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%d. %s - %s | candidateId=%s", index, singerOrUnknown(artist), titleOrUnknown(title), candidateID))
	}
	if len(candidatesAny) > maxLines {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("... and %d more", len(candidatesAny)-maxLines))
	}
	b.WriteString("\n")
	b.WriteString("next: choose one candidateId and call confirmDownload with sessionId + candidateId")
	return b.String()
}

func mcpPingResponseText(result map[string]any) string {
	b := strings.Builder{}
	b.WriteString("pong")
	b.WriteString("\nok: ")
	b.WriteString(strconv.FormatBool(asBool(result["ok"], false)))
	b.WriteString("\nserverTime: ")
	b.WriteString(strings.TrimSpace(asString(result["serverTime"])))
	b.WriteString("\nnext: call searchSongs with keyword/source/page/limit")
	return b.String()
}

func mcpSearchErrorText(prefix string, structured map[string]any) string {
	b := strings.Builder{}
	b.WriteString(prefix)
	if reason := strings.TrimSpace(asString(structured["reason"])); reason != "" {
		b.WriteString("\nreason: ")
		b.WriteString(reason)
	}
	if requestKey := strings.TrimSpace(asString(structured["requestKey"])); requestKey != "" {
		b.WriteString("\nrequestKey: ")
		b.WriteString(requestKey)
	}
	b.WriteString("\nelapsedMs: ")
	b.WriteString(fmt.Sprintf("%d", asInt(structured["elapsedMs"], 0)))
	if retryAfterMs, ok := structured["retryAfterMs"]; ok {
		b.WriteString("\nretryAfterMs: ")
		b.WriteString(fmt.Sprintf("%d", asInt(retryAfterMs, 0)))
	}
	if errText := strings.TrimSpace(asString(structured["error"])); errText != "" {
		b.WriteString("\nerror: ")
		b.WriteString(errText)
	}
	b.WriteString("\nnext: retry searchSongs with same keyword or simplify keyword")
	return b.String()
}

func mcpConfirmResponseText(result map[string]any) string {
	b := strings.Builder{}
	b.WriteString("candidate confirmed")
	b.WriteString("\nsessionId: ")
	b.WriteString(strings.TrimSpace(asString(result["sessionId"])))
	b.WriteString("\nstate: ")
	b.WriteString(strings.TrimSpace(asString(result["state"])))
	b.WriteString("\nselectedCandidateId: ")
	b.WriteString(strings.TrimSpace(asString(result["selectedCandidateId"])))
	b.WriteString("\nconfirmationToken: ")
	b.WriteString(strings.TrimSpace(asString(result["confirmationToken"])))
	b.WriteString("\nselectedSong: ")
	b.WriteString(singerOrUnknown(strings.TrimSpace(asString(result["artist"]))))
	b.WriteString(" - ")
	b.WriteString(titleOrUnknown(strings.TrimSpace(asString(result["songName"]))))
	b.WriteString("\nnext: call startDownload with confirmationToken")
	return b.String()
}

func mcpStartDownloadResponseText(result map[string]any) string {
	b := strings.Builder{}
	b.WriteString("download task started")
	b.WriteString("\ntaskId: ")
	b.WriteString(strings.TrimSpace(asString(result["taskId"])))
	b.WriteString("\nstatus: ")
	b.WriteString(strings.TrimSpace(asString(result["status"])))
	b.WriteString("\nquality: ")
	b.WriteString(strings.TrimSpace(asString(result["quality"])))
	b.WriteString("\nsource: ")
	b.WriteString(strings.TrimSpace(asString(result["source"])))
	b.WriteString("\nsongName: ")
	b.WriteString(titleOrUnknown(strings.TrimSpace(asString(result["songName"]))))
	b.WriteString("\nflowSessionId: ")
	b.WriteString(strings.TrimSpace(asString(result["flowSessionId"])))
	b.WriteString("\nnext: poll with getDownloadStatus or waitDownload using taskId")
	return b.String()
}

func mcpDownloadStatusResponseText(prefix string, status map[string]any) string {
	b := strings.Builder{}
	b.WriteString(prefix)
	b.WriteString("\ntaskId: ")
	b.WriteString(strings.TrimSpace(asString(status["taskId"])))
	b.WriteString("\nstatus: ")
	b.WriteString(strings.TrimSpace(asString(status["status"])))
	b.WriteString("\nprogress: ")
	b.WriteString(fmt.Sprintf("%d", asInt(status["progress"], 0)))
	b.WriteString("\ncompleted: ")
	b.WriteString(strconv.FormatBool(asBool(status["completed"], false)))
	b.WriteString("\nfailed: ")
	b.WriteString(strconv.FormatBool(asBool(status["failed"], false)))
	if waitResult := strings.TrimSpace(asString(status["waitResult"])); waitResult != "" {
		b.WriteString("\nwaitResult: ")
		b.WriteString(waitResult)
	}
	if errText := strings.TrimSpace(asString(status["error"])); errText != "" {
		b.WriteString("\nerror: ")
		b.WriteString(errText)
	}
	b.WriteString("\nnext: if not terminal, continue waiting with waitDownload or poll getDownloadStatus")
	return b.String()
}

func mcpErrorText(prefix string, structured map[string]any, nextAction string) string {
	b := strings.Builder{}
	b.WriteString(prefix)
	if structured != nil {
		if sessionID := strings.TrimSpace(asString(structured["sessionId"])); sessionID != "" {
			b.WriteString("\nsessionId: ")
			b.WriteString(sessionID)
		}
		if candidateID := strings.TrimSpace(asString(structured["candidateId"])); candidateID != "" {
			b.WriteString("\ncandidateId: ")
			b.WriteString(candidateID)
		}
		if token := strings.TrimSpace(asString(structured["confirmationToken"])); token != "" {
			b.WriteString("\nconfirmationToken: ")
			b.WriteString(token)
		}
		if taskID := strings.TrimSpace(asString(structured["taskId"])); taskID != "" {
			b.WriteString("\ntaskId: ")
			b.WriteString(taskID)
		}
		if songName := strings.TrimSpace(asString(structured["songName"])); songName != "" {
			artist := strings.TrimSpace(asString(structured["artist"]))
			b.WriteString("\nsong: ")
			b.WriteString(singerOrUnknown(artist))
			b.WriteString(" - ")
			b.WriteString(titleOrUnknown(songName))
		}
		if mediaID := strings.TrimSpace(asString(structured["mediaId"])); mediaID != "" {
			b.WriteString("\nmediaId: ")
			b.WriteString(mediaID)
		}
		if state := strings.TrimSpace(asString(structured["state"])); state != "" {
			b.WriteString("\nstate: ")
			b.WriteString(state)
		}
		if reason := strings.TrimSpace(asString(structured["reason"])); reason != "" {
			b.WriteString("\nreason: ")
			b.WriteString(reason)
		}
		if errText := strings.TrimSpace(asString(structured["error"])); errText != "" {
			b.WriteString("\nerror: ")
			b.WriteString(errText)
		}
	}
	if strings.TrimSpace(nextAction) != "" {
		b.WriteString("\n")
		b.WriteString(nextAction)
	}
	return b.String()
}

func cloneStringMapAny(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	buf, err := json.Marshal(src)
	if err != nil {
		out := make(map[string]any, len(src))
		for k, v := range src {
			out[k] = v
		}
		return out
	}
	var dst map[string]any
	if err := json.Unmarshal(buf, &dst); err != nil {
		out := make(map[string]any, len(src))
		for k, v := range src {
			out[k] = v
		}
		return out
	}
	return dst
}

func mcpRefreshSearchResultSession(result map[string]any, username string) {
	if result == nil {
		return
	}
	rawCandidates, ok := result["candidates"].([]any)
	if !ok || len(rawCandidates) == 0 {
		return
	}
	newCandidateIDs := make([]string, 0, len(rawCandidates))
	for _, raw := range rawCandidates {
		candidate, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		songInfo := mcpCandidateSongInfoFromResult(candidate)
		candidateID := saveMCPCandidate(username, songInfo)
		candidate["candidateId"] = candidateID
		newCandidateIDs = append(newCandidateIDs, candidateID)
	}
	if len(newCandidateIDs) == 0 {
		return
	}
	flowSessionID := createMCPFlowSession(username, newCandidateIDs)
	flow, _ := result["flow"].(map[string]any)
	if flow == nil {
		flow = map[string]any{}
	}
	flow["sessionId"] = flowSessionID
	flow["state"] = mcpFlowStateWaitingUserSelection
	flow["requiresUserSelection"] = true
	flow["nextAction"] = "Ask user to choose a candidate, then call confirmDownload"
	result["flow"] = flow
}

func mcpCandidateSongInfoFromResult(candidate map[string]any) map[string]any {
	if candidate == nil {
		return map[string]any{}
	}
	if raw, ok := candidate["songInfo"].(map[string]any); ok && len(raw) > 0 {
		return cloneStringMap(raw)
	}
	name := strings.TrimSpace(asString(candidate["songName"]))
	if name == "" {
		name = strings.TrimSpace(asString(candidate["name"]))
	}
	singer := strings.TrimSpace(asString(candidate["artist"]))
	if singer == "" {
		singer = strings.TrimSpace(asString(candidate["singer"]))
	}
	source := strings.TrimSpace(asString(candidate["source"]))
	return map[string]any{
		"name":      name,
		"songName":  name,
		"singer":    singer,
		"artist":    singer,
		"albumName": asString(candidate["albumName"]),
		"duration":  candidate["duration"],
		"source":    source,
		"qualitys":  candidate["qualitys"],
	}
}

func computeKeywordRelevance(tokens []string, name, singer, album string) int {
	if len(tokens) == 0 {
		return 0
	}
	haystack := strings.ToLower(strings.Join([]string{name, singer, album}, " "))
	score := 0
	for _, t := range tokens {
		if t == "" {
			continue
		}
		if strings.Contains(haystack, t) {
			score++
		}
	}
	return score
}

func mcpToolResult(text string, structured map[string]any) map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"structuredContent": structured,
	}
}

func mcpToolErrorResult(text string, structured map[string]any) map[string]any {
	res := mcpToolResult(text, structured)
	res["isError"] = true
	return res
}

func mcpDownloadStatusFromTask(task onlineDownloadTask) map[string]any {
	return map[string]any{
		"taskId":       task.ID,
		"mode":         task.Mode,
		"status":       task.Status,
		"progress":     task.Progress,
		"received":     task.Received,
		"total":        task.Total,
		"speed":        task.Speed,
		"error":        task.Error,
		"title":        task.Title,
		"artist":       task.Artist,
		"source":       task.Source,
		"quality":      task.Quality,
		"sourceName":   task.SourceName,
		"isTerminal":   task.Status == "completed" || task.Status == "failed" || task.Status == "canceled",
		"completed":    task.Status == "completed",
		"failed":       task.Status == "failed" || task.Status == "canceled",
		"updatedAtUtc": task.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (api *Router) verifyMCPToken(r *http.Request) error {
	settings, err := loadOnlineSourceSettings()
	if err != nil {
		return fmt.Errorf("could not load MCP settings")
	}
	expected := strings.TrimSpace(settings.MCPToken)
	if expected == "" {
		return fmt.Errorf("MCP token is not configured")
	}
	provided := strings.TrimSpace(mcpTokenFromRequest(r))
	if provided == "" {
		return fmt.Errorf("missing MCP token")
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return fmt.Errorf("invalid MCP token")
	}
	return nil
}

func mcpTokenFromRequest(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-ND-MCP-Token")); token != "" {
		return token
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}
	return ""
}

func (api *Router) writeMCPError(w http.ResponseWriter, id any, code int, message, data string) {
	resp := mcpJSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &mcpRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}
