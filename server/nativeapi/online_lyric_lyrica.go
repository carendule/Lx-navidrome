package nativeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/navidrome/navidrome/log"
)

const (
	lyricaTimeout = 20 * time.Second
)

var lyricaLyricsFetcher = fetchLyricaLyrics

func (api *Router) onlineLyricaLyric(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	if title == "" {
		http.Error(w, "missing required query param: title", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lyricaTimeout)
	defer cancel()

	result, err := lyricaLyricsFetcher(ctx, title, artist, lyricaPassThrough(r.URL.Query()))
	if err != nil {
		log.Warn(r.Context(), "Lyrica lyric fetch failed", "title", title, "artist", artist, "err", err)
		writeJSON(w, map[string]any{
			"source": "lyrica",
			"query": map[string]string{
				"title":  title,
				"artist": artist,
			},
			"upstream": lyricaBaseURL(),
			"error":    err.Error(),
		})
		return
	}

	writeJSON(w, map[string]any{
		"source": "lyrica",
		"query": map[string]string{
			"title":  title,
			"artist": artist,
		},
		"upstream": lyricaBaseURL(),
		"result":   result,
	})
}

func lyricaBaseURL() string {
	settings, err := loadOnlineSourceSettings()
	if err == nil {
		return sanitizeLyricaBaseURL(settings.LyricaBaseURL)
	}
	if v := strings.TrimSpace(os.Getenv("ND_LYRICA_BASE_URL")); v != "" {
		return sanitizeLyricaBaseURL(v)
	}
	return defaultOnlineLyricaBaseURL
}

func lyricaPassThrough(in url.Values) url.Values {
	out := url.Values{}
	for _, k := range []string{"timestamps", "fast", "mood", "metadata", "source", "lang"} {
		if v := strings.TrimSpace(in.Get(k)); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

func fetchLyricaLyrics(ctx context.Context, title, artist string, extra url.Values) (map[string]any, error) {
	q := url.Values{}
	q.Set("song", title)
	if artist != "" {
		q.Set("artist", artist)
	}
	for k, vals := range extra {
		for _, v := range vals {
			q.Add(k, v)
		}
	}

	endpoint := lyricaBaseURL() + "/lyrics/?" + q.Encode()
	client := &http.Client{Timeout: lyricaTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Navidrome-Lyrica-Proxy/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lyrica request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("lyrica read failed: %w", err)
	}
	if len(body) > 2*1024*1024 {
		return nil, fmt.Errorf("lyrica response too large")
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		preview := string(body)
		if len(preview) > 220 {
			preview = preview[:220]
		}
		return nil, fmt.Errorf("lyrica non-json response (status=%d): %s", resp.StatusCode, preview)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if msg, ok := out["error"].(string); ok && strings.TrimSpace(msg) != "" {
			return nil, fmt.Errorf("lyrica error (status=%d): %s", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("lyrica returned status %d", resp.StatusCode)
	}

	return out, nil
}

func fetchOnlineEmbedLyricViaLyrica(ctx context.Context, songInfo map[string]any) (string, bool) {
	title, artist, ok := onlineLyricSearchQuery(songInfo)
	if !ok {
		embedTrace(ctx, "lyric:lyrica:no-title")
		return "", false
	}

	extra := url.Values{}
	extra.Set("timestamps", "true")
	extra.Set("fast", "true")

	result, err := lyricaLyricsFetcher(ctx, title, artist, extra)
	if err != nil {
		embedTrace(ctx, "lyric:lyrica:fetch-failed", "title", title, "artist", artist, "err", err.Error())
		return "", false
	}

	lyric := strings.TrimSpace(lyricaExtractLyricText(result))
	if lyric == "" {
		embedTrace(ctx, "lyric:lyrica:empty", "title", title, "artist", artist)
		return "", false
	}

	embedTrace(ctx, "lyric:lyrica:ok", "title", title, "artist", artist, "lyricLen", len(lyric))
	return lyric, true
}

func lyricaExtractLyricText(result map[string]any) string {
	if len(result) == 0 {
		return ""
	}

	for _, key := range []string{"lyrics", "lyric", "lrc", "synced_lyrics", "syncedLyrics", "timestamped_lyrics", "timestampedLyrics", "text", "content"} {
		if s := lyricaValueAsLyric(result[key]); s != "" {
			return s
		}
	}

	for _, key := range []string{"result", "data", "payload", "track", "song"} {
		if child, ok := result[key].(map[string]any); ok {
			if s := lyricaExtractLyricText(child); s != "" {
				return s
			}
		}
	}

	for _, v := range result {
		if child, ok := v.(map[string]any); ok {
			if s := lyricaExtractLyricText(child); s != "" {
				return s
			}
		}
	}

	return ""
}

func lyricaValueAsLyric(v any) string {
	switch vv := v.(type) {
	case string:
		return strings.TrimSpace(vv)
	case []string:
		return strings.TrimSpace(strings.Join(vv, "\n"))
	case []any:
		lines := make([]string, 0, len(vv))
		for _, item := range vv {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				lines = append(lines, strings.TrimSpace(s))
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}
	}
	return ""
}
