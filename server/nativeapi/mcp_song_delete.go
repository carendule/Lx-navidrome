package nativeapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/id"
	"github.com/navidrome/navidrome/model/request"
)

// mcpDeleteCandidate is a song found by searchSongsToDelete, kept around just long enough
// for the agent to let the user pick one and confirm/execute the deletion.
type mcpDeleteCandidate struct {
	ID          string
	UserName    string
	MediaFileID string
	Title       string
	Artist      string
	Album       string
	AlbumID     string
	Duration    float32
	Path        string
	Created     time.Time
}

type mcpDeleteFlowState string

const (
	mcpDeleteFlowWaitingSelection    mcpDeleteFlowState = "WAITING_USER_SELECTION"
	mcpDeleteFlowWaitingConfirmation mcpDeleteFlowState = "WAITING_CONFIRMATION"
	mcpDeleteFlowDeleted             mcpDeleteFlowState = "DELETED"
)

// mcpDeleteFlowSession tracks the search -> select -> confirm -> delete flow for one request,
// enforcing that a deletion can only be executed after the impact has been shown and a fresh
// deletionToken has been issued for the exact candidate the user agreed to remove.
type mcpDeleteFlowSession struct {
	ID                string
	UserName          string
	State             mcpDeleteFlowState
	CandidateIDs      []string
	SelectedCandidate string
	DeletionToken     string
	Impact            []map[string]any
	Created           time.Time
	Updated           time.Time
}

var mcpDeleteCandidates sync.Map
var mcpDeleteFlowSessions sync.Map

func (api *Router) mcpSearchSongsToDelete(r *http.Request, args map[string]any) (any, *mcpRPCError) {
	keyword := strings.TrimSpace(asString(args["keyword"]))
	if keyword == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "keyword is required"}
	}
	limit := asInt(args["limit"], 10)
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	ctx := request.WithUser(r.Context(), model.User{IsAdmin: true})
	username, _ := request.UsernameFrom(r.Context())

	mfs, err := api.ds.MediaFile().Search(ctx, keyword, model.QueryOptions{Max: limit})
	if err != nil {
		structured := map[string]any{"reason": "search_failed", "keyword": keyword, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("search failed", structured, "next: retry searchSongsToDelete with a different keyword"), structured), nil
	}

	candidates := make([]map[string]any, 0, len(mfs))
	candidateIDs := make([]string, 0, len(mfs))
	for i, mf := range mfs {
		candidateID := saveMCPDeleteCandidate(username, mf)
		candidateIDs = append(candidateIDs, candidateID)
		candidates = append(candidates, map[string]any{
			"candidateId": candidateID,
			"index":       i + 1,
			"title":       mf.Title,
			"artist":      mf.Artist,
			"album":       mf.Album,
			"duration":    mf.Duration,
			"path":        mf.Path,
			"displayText": mcpDeleteCandidateDisplayText(i+1, mf),
		})
	}

	sessionID := createMCPDeleteFlowSession(username, candidateIDs)
	result := map[string]any{
		"keyword":    keyword,
		"total":      len(candidates),
		"candidates": candidates,
		"flow": map[string]any{
			"sessionId":             sessionID,
			"state":                 mcpDeleteFlowWaitingSelection,
			"requiresUserSelection": true,
			"nextAction":            "Show the candidates to the user and ask which one to delete, then call requestSongDeletion with sessionId + candidateId",
		},
	}
	return mcpToolResult(mcpDeleteSearchResponseText(result), result), nil
}

func (api *Router) mcpRequestSongDeletion(r *http.Request, args map[string]any) (any, *mcpRPCError) {
	sessionID := strings.TrimSpace(asString(args["sessionId"]))
	candidateID := strings.TrimSpace(asString(args["candidateId"]))
	if sessionID == "" || candidateID == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "sessionId and candidateId are required"}
	}
	username, _ := request.UsernameFrom(r.Context())

	session, err := loadMCPDeleteFlowSession(sessionID, username)
	if err != nil {
		structured := map[string]any{"sessionId": sessionID, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("session invalid", structured, "next: call searchSongsToDelete again"), structured), nil
	}
	if session.State != mcpDeleteFlowWaitingSelection {
		structured := map[string]any{"sessionId": sessionID, "state": session.State}
		return mcpToolErrorResult(mcpErrorText("flow state invalid", structured, "next: call searchSongsToDelete again for a fresh session"), structured), nil
	}
	if !containsCandidateID(session.CandidateIDs, candidateID) {
		structured := map[string]any{"sessionId": sessionID, "candidateId": candidateID}
		return mcpToolErrorResult(mcpErrorText("candidateId not in session", structured, "next: choose a candidateId from the searchSongsToDelete result"), structured), nil
	}

	candidate, err := loadMCPDeleteCandidate(candidateID, username)
	if err != nil {
		structured := map[string]any{"candidateId": candidateID, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("candidate unavailable", structured, "next: call searchSongsToDelete again"), structured), nil
	}

	ctx := request.WithUser(r.Context(), model.User{IsAdmin: true})
	impact, err := api.computeSongDeletionImpact(ctx, candidate.MediaFileID)
	if err != nil {
		structured := map[string]any{"candidateId": candidateID, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("could not analyze deletion impact", structured, "next: retry requestSongDeletion"), structured), nil
	}

	token := "mcpdt_" + id.NewRandom()
	session.SelectedCandidate = candidateID
	session.DeletionToken = token
	session.Impact = impact
	session.State = mcpDeleteFlowWaitingConfirmation
	session.Updated = time.Now()
	mcpDeleteFlowSessions.Store(session.ID, session)

	result := map[string]any{
		"sessionId":     session.ID,
		"state":         session.State,
		"deletionToken": token,
		"song": map[string]any{
			"title":  candidate.Title,
			"artist": candidate.Artist,
			"album":  candidate.Album,
			"path":   candidate.Path,
		},
		"impact":     impact,
		"nextAction": "Show the impact to the user and ask for explicit confirmation, then call executeSongDeletion with deletionToken",
	}
	return mcpToolResult(mcpDeleteImpactResponseText(result), result), nil
}

func (api *Router) mcpExecuteSongDeletion(r *http.Request, args map[string]any) (any, *mcpRPCError) {
	token := strings.TrimSpace(asString(args["deletionToken"]))
	if token == "" {
		return nil, &mcpRPCError{Code: -32602, Message: "deletionToken is required. Call requestSongDeletion first."}
	}
	username, _ := request.UsernameFrom(r.Context())

	session, err := loadMCPDeleteFlowSessionByToken(token, username)
	if err != nil {
		structured := map[string]any{"deletionToken": token, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("invalid deletion token", structured, "next: call requestSongDeletion to get a fresh deletionToken"), structured), nil
	}
	if session.State != mcpDeleteFlowWaitingConfirmation {
		structured := map[string]any{"sessionId": session.ID, "state": session.State}
		return mcpToolErrorResult(mcpErrorText("flow not ready for deletion", structured, "next: call requestSongDeletion first"), structured), nil
	}

	candidate, err := loadMCPDeleteCandidate(session.SelectedCandidate, username)
	if err != nil {
		structured := map[string]any{"sessionId": session.ID, "candidateId": session.SelectedCandidate, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("candidate unavailable", structured, "next: call searchSongsToDelete and requestSongDeletion again"), structured), nil
	}

	ctx := request.WithUser(r.Context(), model.User{IsAdmin: true})
	if err := api.maintenance.DeleteSongs(ctx, []string{candidate.MediaFileID}); err != nil {
		structured := map[string]any{"sessionId": session.ID, "error": err.Error()}
		return mcpToolErrorResult(mcpErrorText("failed to delete song", structured, "next: verify the file still exists and retry"), structured), nil
	}

	session.State = mcpDeleteFlowDeleted
	session.DeletionToken = ""
	session.Updated = time.Now()
	mcpDeleteFlowSessions.Store(session.ID, session)
	mcpDeleteCandidates.Delete(session.SelectedCandidate)

	result := map[string]any{
		"sessionId": session.ID,
		"state":     session.State,
		"deleted": map[string]any{
			"title":  candidate.Title,
			"artist": candidate.Artist,
			"album":  candidate.Album,
			"path":   candidate.Path,
		},
		"impact":  session.Impact,
		"message": "song, local file, and any emptied albums/playlists have been deleted",
	}
	return mcpToolResult(mcpDeleteCompletedResponseText(result), result), nil
}

// computeSongDeletionImpact reports which album/playlists will be deleted (because this was
// their last remaining song) versus just shrink by one song.
func (api *Router) computeSongDeletionImpact(ctx context.Context, mediaFileID string) ([]map[string]any, error) {
	mf, err := api.ds.MediaFile().Get(ctx, mediaFileID)
	if err != nil {
		return nil, err
	}

	impact := make([]map[string]any, 0, 4)

	if mf.AlbumID != "" {
		album, err := api.ds.Album().Get(ctx, mf.AlbumID)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			entry := map[string]any{"type": "album", "id": album.ID, "name": album.Name}
			if album.SongCount <= 1 {
				entry["willBeDeleted"] = true
			} else {
				entry["willBeDeleted"] = false
				entry["remainingSongs"] = album.SongCount - 1
			}
			impact = append(impact, entry)
		}
	}

	playlists, err := api.ds.Playlist().GetPlaylists(ctx, mediaFileID)
	if err != nil {
		return nil, err
	}
	// A song can appear more than once in the same playlist; count occurrences so we know
	// whether removing all of them empties the playlist.
	occurrences := map[string]int{}
	order := make([]string, 0, len(playlists))
	byID := map[string]model.Playlist{}
	for _, pls := range playlists {
		if _, ok := occurrences[pls.ID]; !ok {
			order = append(order, pls.ID)
			byID[pls.ID] = pls
		}
		occurrences[pls.ID]++
	}
	for _, plsID := range order {
		pls := byID[plsID]
		entry := map[string]any{"type": "playlist", "id": pls.ID, "name": pls.Name}
		if pls.SongCount <= occurrences[plsID] {
			entry["willBeDeleted"] = true
		} else {
			entry["willBeDeleted"] = false
			entry["remainingSongs"] = pls.SongCount - occurrences[plsID]
		}
		impact = append(impact, entry)
	}

	return impact, nil
}

func saveMCPDeleteCandidate(username string, mf model.MediaFile) string {
	key := "delcand_" + id.NewRandom()
	mcpDeleteCandidates.Store(key, mcpDeleteCandidate{
		ID:          key,
		UserName:    username,
		MediaFileID: mf.ID,
		Title:       mf.Title,
		Artist:      mf.Artist,
		Album:       mf.Album,
		AlbumID:     mf.AlbumID,
		Duration:    mf.Duration,
		Path:        mf.Path,
		Created:     time.Now(),
	})
	return key
}

func loadMCPDeleteCandidate(candidateID, username string) (mcpDeleteCandidate, error) {
	raw, ok := mcpDeleteCandidates.Load(candidateID)
	if !ok {
		return mcpDeleteCandidate{}, errors.New("candidate not found")
	}
	candidate := raw.(mcpDeleteCandidate)
	if time.Since(candidate.Created) > mcpCandidateTTL {
		mcpDeleteCandidates.Delete(candidateID)
		return mcpDeleteCandidate{}, errors.New("candidate expired")
	}
	if candidate.UserName != "" && username != "" && candidate.UserName != username {
		return mcpDeleteCandidate{}, errors.New("candidate belongs to another user")
	}
	return candidate, nil
}

func createMCPDeleteFlowSession(username string, candidateIDs []string) string {
	now := time.Now()
	sessionID := "mcpds_" + id.NewRandom()
	mcpDeleteFlowSessions.Store(sessionID, mcpDeleteFlowSession{
		ID:           sessionID,
		UserName:     username,
		State:        mcpDeleteFlowWaitingSelection,
		CandidateIDs: append([]string(nil), candidateIDs...),
		Created:      now,
		Updated:      now,
	})
	return sessionID
}

func loadMCPDeleteFlowSession(sessionID, username string) (mcpDeleteFlowSession, error) {
	raw, ok := mcpDeleteFlowSessions.Load(sessionID)
	if !ok {
		return mcpDeleteFlowSession{}, errors.New("flow session not found")
	}
	session := raw.(mcpDeleteFlowSession)
	if time.Since(session.Updated) > mcpFlowSessionTTL {
		mcpDeleteFlowSessions.Delete(sessionID)
		return mcpDeleteFlowSession{}, errors.New("flow session expired")
	}
	if session.UserName != "" && username != "" && session.UserName != username {
		return mcpDeleteFlowSession{}, errors.New("flow session belongs to another user")
	}
	return session, nil
}

func loadMCPDeleteFlowSessionByToken(deletionToken, username string) (mcpDeleteFlowSession, error) {
	var hit mcpDeleteFlowSession
	found := false
	mcpDeleteFlowSessions.Range(func(_, value any) bool {
		session := value.(mcpDeleteFlowSession)
		if strings.TrimSpace(session.DeletionToken) == deletionToken {
			hit = session
			found = true
			return false
		}
		return true
	})
	if !found {
		return mcpDeleteFlowSession{}, errors.New("deletion token not found")
	}
	return loadMCPDeleteFlowSession(hit.ID, username)
}

func mcpDeleteCandidateDisplayText(index int, mf model.MediaFile) string {
	title := titleOrUnknown(strings.TrimSpace(mf.Title))
	artist := singerOrUnknown(strings.TrimSpace(mf.Artist))
	album := strings.TrimSpace(mf.Album)
	if album == "" {
		album = "Unknown album"
	}
	duration := mcpSecondsToMMSS(int(mf.Duration + 0.5))
	return fmt.Sprintf("%d.%s [歌手:%s][专辑:%s][时长:%s]", index, title, artist, album, duration)
}

func mcpDeleteSearchResponseText(result map[string]any) string {
	candidates, _ := result["candidates"].([]map[string]any)
	sessionID := ""
	if flow, ok := result["flow"].(map[string]any); ok {
		sessionID = strings.TrimSpace(asString(flow["sessionId"]))
	}
	b := strings.Builder{}
	b.WriteString(fmt.Sprintf("found %d song(s) matching %q", len(candidates), asString(result["keyword"])))
	if sessionID != "" {
		b.WriteString("\nsessionId: ")
		b.WriteString(sessionID)
	}
	for _, c := range candidates {
		b.WriteString("\n")
		b.WriteString(asString(c["displayText"]))
		b.WriteString(fmt.Sprintf(" | candidateId=%s", asString(c["candidateId"])))
	}
	b.WriteString("\nnext: ask the user which song to delete, then call requestSongDeletion with sessionId + candidateId")
	return b.String()
}

func mcpDeleteImpactResponseText(result map[string]any) string {
	song, _ := result["song"].(map[string]any)
	impact, _ := result["impact"].([]map[string]any)
	b := strings.Builder{}
	b.WriteString("deletion impact analyzed, awaiting user confirmation")
	b.WriteString("\nsong: ")
	b.WriteString(singerOrUnknown(asString(song["artist"])))
	b.WriteString(" - ")
	b.WriteString(titleOrUnknown(asString(song["title"])))
	b.WriteString("\ndeletionToken: ")
	b.WriteString(asString(result["deletionToken"]))
	if len(impact) == 0 {
		b.WriteString("\nimpact: no albums or playlists will be affected")
	} else {
		b.WriteString("\nimpact:")
		for _, entry := range impact {
			b.WriteString("\n- ")
			b.WriteString(strings.ToUpper(asString(entry["type"])))
			b.WriteString(" \"")
			b.WriteString(asString(entry["name"]))
			b.WriteString("\": ")
			if asBool(entry["willBeDeleted"], false) {
				b.WriteString("will also be deleted (this is its last song)")
			} else {
				b.WriteString(fmt.Sprintf("will keep %d remaining song(s)", asInt(entry["remainingSongs"], 0)))
			}
		}
	}
	b.WriteString("\nnext: ask the user to explicitly confirm deletion, then call executeSongDeletion with deletionToken")
	return b.String()
}

func mcpDeleteCompletedResponseText(result map[string]any) string {
	deleted, _ := result["deleted"].(map[string]any)
	b := strings.Builder{}
	b.WriteString("song deleted")
	b.WriteString("\nsong: ")
	b.WriteString(singerOrUnknown(asString(deleted["artist"])))
	b.WriteString(" - ")
	b.WriteString(titleOrUnknown(asString(deleted["title"])))
	b.WriteString("\nnext: tell the user the deletion is complete")
	return b.String()
}
