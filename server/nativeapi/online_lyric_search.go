package nativeapi

// online_lyric_search.go — search-based lyric fetchers.
//
// The original online_lyric.go file ships with five
// songmid-based fetchers (one per source). Each of those
// takes the songInfo.songmid and asks the source "give me
// the lyric for THIS song id". That works when the
// songInfo.source and the target source share a song-id
// namespace — i.e. when the song was downloaded from the
// target source. It does NOT work in the cross-source
// fallback case the user reported:
//
//   - Download from WY → songInfo.songmid = 108914
//     (网易云's id)
//   - Fall back to KW → KW receives 108914, treats it as
//     a KW id, returns the lyric for a completely
//     different song (王菀之 / 原来如此).
//   - Fall back to TX → TX receives 108914, has no song
//     with that id in its own namespace, returns empty.
//
// The fix is a second layer: when the songmid-based
// fetch returns empty, do a name+singer search on the
// target source to find the song's id in THAT source's
// namespace, then re-fetch the lyric. This file implements
// the search layer for the three sources whose search
// APIs are reachable without auth (wy, tx, kw) and which
// the user has been hitting most often.
//
// The search-based candidates are still subject to the
// same matcher (onlineLyricMatchScoreLyric in
// online_lyric_match.go) — the candidate's [ti:]/[ar:]
// tags and overall duration are checked against songInfo
// before we accept the lyric. The point of search is to
// REACH the right song; the matcher still gates against
// pulling the wrong one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// onlineLyricSearchResult is the in-memory shape returned
// by the per-source search functions. We separate the
// "search succeeded" outcome from the "search found a
// matching song" outcome: a search can succeed (HTTP 200,
// valid JSON, non-empty list) but not find a match (all
// candidates failed the matcher). The orchestrator
// distinguishes the two cases in the trace so the user
// can tell whether the search was the issue or the
// matching was.
type onlineLyricSearchResult struct {
	// Lyric is the lyric text from the first
	// matcher-acceptable candidate, or "" when no
	// candidate passed the matcher.
	Lyric string
	// Searched is true when the search HTTP call
	// succeeded and returned a non-empty result. The
	// distinction matters: a Searched=false result
	// means "the source's search API didn't return
	// anything", while Searched=true with Lyric=""
	// means "the source had results but none of them
	// were the right song".
	Searched bool
	// FirstCandidateTitle is the [ti:] tag (or its
	// search-equivalent) of the first result. Carried
	// in the trace so the user can grep borderline
	// cases without re-running with a debugger.
	FirstCandidateTitle string
	// FirstCandidateSinger is the [ar:] tag of the
	// first result.
	FirstCandidateSinger string
}

// onlineLyricSearchQuery is the trimmed name+singer
// combination we pass to the per-source search APIs.
// Returns ("", false) when there's no usable name
// (the search would be meaningless). The "haveName"
// bool is also exposed so the orchestrator can skip
// the search phase entirely on empty input.
func onlineLyricSearchQuery(songInfo map[string]any) (name, singer string, ok bool) {
	name = strings.TrimSpace(stringValue(songInfo["name"]))
	singer = strings.TrimSpace(stringValue(songInfo["singer"]))
	if name == "" {
		if meta := mapValue(songInfo["meta"]); meta != nil {
			name = strings.TrimSpace(stringValue(meta["songName"]))
			singer = strings.TrimSpace(stringValue(meta["singerName"]))
		}
	}
	if name == "" {
		return "", "", false
	}
	return name, singer, true
}

// onlineLyricSearchQueryString is the format each
// source's search API accepts. Most sources accept
// just the song name; some append the singer for
// disambiguation when the name is short or common.
// We always include the singer when present because
// the disambiguation win outweighs the slight query-
// length cost.
func onlineLyricSearchQueryString(name, singer string) string {
	if singer == "" {
		return name
	}
	return name + " " + singer
}

// ---------------------------------------------------------------------------
// wy (网易云) search → lyric
// ---------------------------------------------------------------------------

// fetchOnlineLyricWYBySearch searches 网易云 for the song
// by name+singer, then fetches the lyric for the first
// candidate whose title+duration passes the matcher.
//
// 网易云's search API is the eapi endpoint
// `/api/search/song/list/page` (encrypted with the same
// AES-128-ECB key the lyric endpoint uses). The response
// shape is body.data.resources[].baseInfo.simpleSongData —
// we extract id (the song's id in wy's namespace) and
// call the existing fetchOnlineLyricWY path with the
// resolved id.
//
// The id field is the canonical song id, NOT the
// "songmid" the original songInfo carried (which was
// the id from the download source, not wy). We
// reconstruct a synthetic songInfo for the resolved id
// and feed it to the existing per-song lyric fetcher.
func fetchOnlineLyricWYBySearch(ctx context.Context, songInfo map[string]any) onlineLyricSearchResult {
	name, singer, ok := onlineLyricSearchQuery(songInfo)
	if !ok {
		return onlineLyricSearchResult{}
	}
	enc, err := onlineLyricWYEAPIEncrypt("/api/search/song/list/page", map[string]any{
		"keyword":     onlineLyricSearchQueryString(name, singer),
		"limit":       5,
		"offset":      0,
		"scene":       "normal",
		"needCorrect": "1",
		"channel":     "typing",
		"total":       true,
	})
	if err != nil {
		embedTrace(ctx, "lyric:wy:search-encrypt-failed", "err", err.Error())
		return onlineLyricSearchResult{}
	}
	url := "https://interface3.music.163.com/eapi/search/song/list/page?params=" + enc
	embedTrace(ctx, "lyric:wy:search-request", "query", onlineLyricSearchQueryString(name, singer))
	body, ok := onlineLyricWYFetch(ctx, url)
	if !ok {
		return onlineLyricSearchResult{}
	}
	// Walk body.data.resources. We need each item's
	// baseInfo.simpleSongData.id and .name and .ar
	// (singers) and .dt (duration in ms). We do this
	// with a single byte scan to avoid a 200-byte
	// unmarshal + struct walk for the common case of
	// 5 candidates.
	candidates := onlineLyricSearchWYParseCandidates(body)
	if len(candidates) == 0 {
		embedTrace(ctx, "lyric:wy:search-no-candidates")
		return onlineLyricSearchResult{Searched: true}
	}
	out := onlineLyricSearchResult{Searched: true}
	if len(candidates) > 0 {
		out.FirstCandidateTitle = candidates[0].name
		out.FirstCandidateSinger = candidates[0].singer
	}
	// Walk the candidates in order. The matcher in
	// online_lyric_match.go gates on title/artist/
	// duration; we apply the same gate here so the
	// orchestrator doesn't see search candidates that
	// would have been rejected by the matcher.
	for _, c := range candidates {
		candInfo := map[string]any{
			"name":     c.name,
			"singer":   c.singer,
			"interval": onlineLyricSearchFormatDuration(c.durationMs / 1000),
			"songmid":  c.id,
		}
		// We don't have the lyric yet — first we
		// need the songmid-resolution check (the
		// candidate must be the right song). We
		// score against songInfo, not the lyric,
		// because the lyric requires another HTTP
		// round-trip we don't want to do for a wrong
		// song.
		if score, accept := onlineLyricSearchPreAccept(songInfo, c); accept {
			res := fetchOnlineLyricWY(ctx, candInfo)
			if strings.TrimSpace(res.Lyric) != "" {
				out.Lyric = res.Lyric
				embedTrace(ctx, "lyric:wy:search-accepted", "candidateId", c.id, "candidateName", c.name, "titleScore", score.TitleScore, "artistScore", score.ArtistScore, "durationDelta", score.DurationDelta)
				return out
			}
		}
	}
	embedTrace(ctx, "lyric:wy:search-no-match")
	return out
}

// onlineLyricSearchWYParseCandidates extracts the
// candidate list from a wy search response. The path
// is body.data.resources[].baseInfo.simpleSongData.
// Returns up to 5 candidates.
type onlineLyricSearchWYCandidate struct {
	id         string
	name       string
	singer     string
	durationMs int
}

func (c onlineLyricSearchWYCandidate) GetName() string     { return c.name }
func (c onlineLyricSearchWYCandidate) GetSinger() string   { return c.singer }
func (c onlineLyricSearchWYCandidate) GetDurationSec() int { return c.durationMs / 1000 }

func onlineLyricSearchWYParseCandidates(body []byte) []onlineLyricSearchWYCandidate {
	// Find the "resources" array. We don't trust the
	// outer response shape to be stable, so we scan
	// for the array and walk each element looking
	// for baseInfo.simpleSongData.
	needle := []byte(`"resources":[`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		return nil
	}
	arrStart := idx + len(needle) - 1 // include the [
	// Find matching ].
	depth := 1
	i := arrStart + 1
	for i < len(body) {
		switch body[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				elems := splitJSONTopLevelElements(body[arrStart+1 : i])
				out := make([]onlineLyricSearchWYCandidate, 0, len(elems))
				for _, el := range elems {
					if c := onlineLyricSearchWYExtractOne(el); c != nil {
						out = append(out, *c)
					}
				}
				return out
			}
		}
		i++
	}
	return nil
}

func onlineLyricSearchWYExtractOne(elem []byte) *onlineLyricSearchWYCandidate {
	// Look for "baseInfo":{"simpleSongData":{...}}
	baseNeedle := []byte(`"baseInfo":{`)
	idx := bytes.Index(elem, baseNeedle)
	if idx < 0 {
		return nil
	}
	// Find the matching } for baseInfo.
	depth := 1
	i := idx + len(baseNeedle)
	baseStart := i
	for i < len(elem) {
		switch elem[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				base := elem[baseStart:i]
				simpleNeedle := []byte(`"simpleSongData":{`)
				j := bytes.Index(base, simpleNeedle)
				if j < 0 {
					return nil
				}
				simpleStart := j + len(simpleNeedle) - 1
				// Find matching } in base.
				d2 := 1
				k := simpleStart + 1
				for k < len(base) {
					switch base[k] {
					case '{':
						d2++
					case '}':
						d2--
						if d2 == 0 {
							inner := base[simpleStart+1 : k]
							return onlineLyricSearchWYExtractFields(inner)
						}
					}
					k++
				}
				return nil
			}
		}
		i++
	}
	return nil
}

func onlineLyricSearchWYExtractFields(inner []byte) *onlineLyricSearchWYCandidate {
	id := onlineLyricJSONStringFieldImpl(inner, "id")
	name := onlineLyricJSONStringFieldImpl(inner, "name")
	// Singers live in an array of {"name": "..."}.
	// We pick the first one and join the rest with
	// "、" to mirror lxserver-main's formatSinger
	// behavior.
	singers := onlineLyricJSONStringFieldAt(inner, "ar", "name")
	// dt is the duration in milliseconds.
	dtStr := onlineLyricJSONStringFieldImpl(inner, "dt")
	dtMs, _ := strconv.Atoi(dtStr)
	if id == "" || name == "" {
		return nil
	}
	return &onlineLyricSearchWYCandidate{
		id:         id,
		name:       name,
		singer:     singers,
		durationMs: dtMs,
	}
}

// ---------------------------------------------------------------------------
// tx (QQ 音乐) search → lyric
// ---------------------------------------------------------------------------

// fetchOnlineLyricTXBySearch searches QQ 音乐 for the
// song by name+singer, then fetches the lyric for the
// first matcher-acceptable candidate.
//
// QQ's search endpoint is the musicu.fcg POST API.
// The response shape is body.req.data.body.item_song[].
// We pull songmid (the .mid field) and pass it to the
// existing fetchOnlineLyricTX.
func fetchOnlineLyricTXBySearch(ctx context.Context, songInfo map[string]any) onlineLyricSearchResult {
	name, singer, ok := onlineLyricSearchQuery(songInfo)
	if !ok {
		return onlineLyricSearchResult{}
	}
	payload := map[string]any{
		"comm": map[string]any{
			"ct":       "11",
			"cv":       "14090508",
			"v":        "14090508",
			"tmeAppID": "qqmusic",
		},
		"req": map[string]any{
			"module": "music.search.SearchCgiService",
			"method": "DoSearchForQQMusicMobile",
			"param": map[string]any{
				"search_type":  0,
				"query":        onlineLyricSearchQueryString(name, singer),
				"page_num":     1,
				"num_per_page": 5,
				"highlight":    0,
				"nqc_flag":     0,
				"cat":          2,
				"grp":          1,
			},
		},
	}
	embedTrace(ctx, "lyric:tx:search-request", "query", onlineLyricSearchQueryString(name, singer))
	body, ok := onlineLyricSearchTXPost(ctx, payload)
	if !ok {
		return onlineLyricSearchResult{}
	}
	candidates := onlineLyricSearchTXParseCandidates(body)
	if len(candidates) == 0 {
		embedTrace(ctx, "lyric:tx:search-no-candidates")
		return onlineLyricSearchResult{Searched: true}
	}
	out := onlineLyricSearchResult{Searched: true}
	out.FirstCandidateTitle = candidates[0].name
	out.FirstCandidateSinger = candidates[0].singer
	for _, c := range candidates {
		if score, accept := onlineLyricSearchPreAccept(songInfo, c); accept {
			candInfo := map[string]any{
				"name":     c.name,
				"singer":   c.singer,
				"interval": onlineLyricSearchFormatDuration(c.durationSec),
				"songmid":  c.mid,
			}
			res := fetchOnlineLyricTX(ctx, candInfo)
			if strings.TrimSpace(res.Lyric) != "" {
				out.Lyric = res.Lyric
				embedTrace(ctx, "lyric:tx:search-accepted", "candidateId", c.mid, "candidateName", c.name, "titleScore", score.TitleScore, "artistScore", score.ArtistScore, "durationDelta", score.DurationDelta)
				return out
			}
		}
	}
	embedTrace(ctx, "lyric:tx:search-no-match")
	return out
}

type onlineLyricSearchTXCandidate struct {
	mid         string
	name        string
	singer      string
	durationSec int
}

func (c onlineLyricSearchTXCandidate) GetName() string     { return c.name }
func (c onlineLyricSearchTXCandidate) GetSinger() string   { return c.singer }
func (c onlineLyricSearchTXCandidate) GetDurationSec() int { return c.durationSec }

// onlineLyricSearchTXPost is the musicu.fcg POST
// transport. We use a separate function so the trace
// labels stay distinct from the existing per-song
// lyric fetch.
func onlineLyricSearchTXPost(ctx context.Context, payload map[string]any) ([]byte, bool) {
	plJSON, err := json.Marshal(payload)
	if err != nil {
		embedTrace(ctx, "lyric:tx:search-build-failed", "err", err.Error())
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", strings.NewReader(string(plJSON)))
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "QQMusic 14090508(android 12)")
	req.Header.Set("Content-Type", "application/json")
	client := onlineLyricSongInfoClient()
	resp, err := client.Do(req)
	if err != nil {
		embedTrace(ctx, "lyric:tx:search-connect-failed", "err", err.Error())
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		embedTrace(ctx, "lyric:tx:search-non-2xx", "status", resp.StatusCode)
		return nil, false
	}
	const maxBody = 2 * 1024 * 1024
	body, err := readAllLimited(resp.Body, maxBody)
	if err != nil {
		embedTrace(ctx, "lyric:tx:search-read-failed", "err", err.Error())
		return nil, false
	}
	return body, true
}

// readAllLimited is a tiny shim around io.ReadAll +
// io.LimitReader that doesn't bloat the imports.
func readAllLimited(r interface {
	Read(p []byte) (n int, err error)
}, max int64) ([]byte, error) {
	limited := io.LimitReader(r, max+1)
	return io.ReadAll(limited)
}

func onlineLyricSearchTXParseCandidates(body []byte) []onlineLyricSearchTXCandidate {
	// Path: body.req.data.body.item_song[]
	// The qq search response is nested; we drill in
	// step by step.
	needle := []byte(`"item_song":[`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		return nil
	}
	arrStart := idx + len(needle) - 1
	depth := 1
	i := arrStart + 1
	for i < len(body) {
		switch body[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				elems := splitJSONTopLevelElements(body[arrStart+1 : i])
				out := make([]onlineLyricSearchTXCandidate, 0, len(elems))
				for _, el := range elems {
					if c := onlineLyricSearchTXExtractOne(el); c != nil {
						out = append(out, *c)
					}
				}
				return out
			}
		}
		i++
	}
	return nil
}

func onlineLyricSearchTXExtractOne(elem []byte) *onlineLyricSearchTXCandidate {
	mid := onlineLyricJSONStringFieldImpl(elem, "mid")
	name := onlineLyricJSONStringFieldImpl(elem, "name")
	// Singers in QQ search live as an array of
	// {"name": "...", "mid": "..."}. The
	// JSONStringFieldAt helper drills into the first
	// array element.
	singer := onlineLyricJSONStringFieldAt(elem, "singer", "name")
	interval := onlineLyricJSONIntField(elem, "interval")
	if mid == "" {
		return nil
	}
	return &onlineLyricSearchTXCandidate{
		mid:         mid,
		name:        name,
		singer:      singer,
		durationSec: interval,
	}
}

// ---------------------------------------------------------------------------
// kw (酷我) search → lyric
// ---------------------------------------------------------------------------

// fetchOnlineLyricKWBySearch searches 酷我 for the song
// by name+singer, then fetches the lyric for the first
// matcher-acceptable candidate.
//
// 酷我's search API is the simplest of the three: GET
// http://search.kuwo.cn/r.s?all=<query>. The response
// is a top-level JSON array. Each element has
// MUSICRID (with the "MUSIC_" prefix), SONGNAME,
// ARTIST, DURATION (in seconds), etc.
func fetchOnlineLyricKWBySearch(ctx context.Context, songInfo map[string]any) onlineLyricSearchResult {
	name, singer, ok := onlineLyricSearchQuery(songInfo)
	if !ok {
		return onlineLyricSearchResult{}
	}
	query := onlineLyricSearchQueryString(name, singer)
	url := fmt.Sprintf("http://search.kuwo.cn/r.s?client=kt&all=%s&pn=0&rn=5&uid=794762570&ver=kwplayer_ar_9.2.2.1&vipver=1&newver=1&ft=music&cluster=0&strategy=2012&encoding=utf8&rformat=json&vermerge=1&mobi=1&issubtitle=1",
		onlineLyricURLEncodeImpl(query))
	embedTrace(ctx, "lyric:kw:search-request", "query", query)
	raw, ok := httpGetRaw(ctx, url, "", nil)
	if !ok {
		return onlineLyricSearchResult{}
	}
	candidates := onlineLyricSearchKWParseCandidates(raw)
	if len(candidates) == 0 {
		embedTrace(ctx, "lyric:kw:search-no-candidates")
		return onlineLyricSearchResult{Searched: true}
	}
	out := onlineLyricSearchResult{Searched: true}
	out.FirstCandidateTitle = candidates[0].name
	out.FirstCandidateSinger = candidates[0].singer
	for _, c := range candidates {
		if score, accept := onlineLyricSearchPreAccept(songInfo, c); accept {
			candInfo := map[string]any{
				"name":     c.name,
				"singer":   c.singer,
				"interval": onlineLyricSearchFormatDuration(c.durationSec),
				"songmid":  c.id,
			}
			res := fetchOnlineLyricKW(ctx, candInfo)
			if strings.TrimSpace(res.Lyric) != "" {
				out.Lyric = res.Lyric
				embedTrace(ctx, "lyric:kw:search-accepted", "candidateId", c.id, "candidateName", c.name, "titleScore", score.TitleScore, "artistScore", score.ArtistScore, "durationDelta", score.DurationDelta)
				return out
			}
		}
	}
	embedTrace(ctx, "lyric:kw:search-no-match")
	return out
}

type onlineLyricSearchKWCandidate struct {
	id          string
	name        string
	singer      string
	durationSec int
}

func (c onlineLyricSearchKWCandidate) GetName() string     { return c.name }
func (c onlineLyricSearchKWCandidate) GetSinger() string   { return c.singer }
func (c onlineLyricSearchKWCandidate) GetDurationSec() int { return c.durationSec }

func onlineLyricSearchKWParseCandidates(body []byte) []onlineLyricSearchKWCandidate {
	// The kw response is a top-level array. Find
	// the opening [ and matching ].
	start := bytes.IndexByte(body, '[')
	if start < 0 {
		return nil
	}
	depth := 1
	i := start + 1
	for i < len(body) {
		switch body[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				elems := splitJSONTopLevelElements(body[start+1 : i])
				out := make([]onlineLyricSearchKWCandidate, 0, len(elems))
				for _, el := range elems {
					if c := onlineLyricSearchKWExtractOne(el); c != nil {
						out = append(out, *c)
					}
				}
				return out
			}
		}
		i++
	}
	return nil
}

func onlineLyricSearchKWExtractOne(elem []byte) *onlineLyricSearchKWCandidate {
	rid := onlineLyricJSONStringFieldImpl(elem, "MUSICRID")
	if strings.HasPrefix(rid, "MUSIC_") {
		rid = strings.TrimPrefix(rid, "MUSIC_")
	}
	if rid == "" {
		return nil
	}
	name := onlineLyricJSONStringFieldImpl(elem, "SONGNAME")
	artist := onlineLyricJSONStringFieldImpl(elem, "ARTIST")
	// DURATION is the kw search's duration in seconds.
	durationStr := onlineLyricJSONStringFieldImpl(elem, "DURATION")
	duration, _ := strconv.Atoi(durationStr)
	return &onlineLyricSearchKWCandidate{
		id:          rid,
		name:        name,
		singer:      artist,
		durationSec: duration,
	}
}

// ---------------------------------------------------------------------------
// Common helpers
// ---------------------------------------------------------------------------

// onlineLyricSearchPreAccept runs the matcher against a
// search candidate's name/singer/duration (without
// fetching the lyric yet). Returns (score, true) when
// the candidate passes the matcher, and (zero, false)
// when the candidate should be skipped. The orchestrator
// only fires the lyric-fetch HTTP request for candidates
// that pass this gate, which saves us from doing wasted
// HTTP round-trips on wrong songs.
//
// The score is returned alongside the accept flag so the
// caller can include the per-candidate numbers in the
// trace line.
func onlineLyricSearchPreAccept(songInfo map[string]any, c interface {
	GetName() string
	GetSinger() string
	GetDurationSec() int
}) (onlineLyricMatchScore, bool) {
	cand := onlineLyricCandidate{
		Source:      "search",
		Lyric:       "[ti:" + c.GetName() + "]\n[ar:" + c.GetSinger() + "]",
		SelfTitle:   c.GetName(),
		SelfArtist:  c.GetSinger(),
		LyricDurSec: c.GetDurationSec(),
	}
	// Synthesize a fake "lyric" so the matcher's
	// parser can extract the tags. The body is just
	// the [ti:]/[ar:] lines we put in; the matcher
	// will read them back out and score.
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	return score, score.OK
}

// onlineLyricSearchFormatDuration converts an integer
// second count to the "mm:ss" form lx-music uses
// (and the form songInfo.interval is in). The lx-music
// time format is "m:ss" with no zero-padding on the
// minutes (e.g. "5:25" for 5 minutes 25 seconds).
func onlineLyricSearchFormatDuration(sec int) string {
	if sec <= 0 {
		return "0:00"
	}
	m := sec / 60
	s := sec % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

// onlineLyricSearchTimeout caps the total wall-clock
// time for a single search-based fallback attempt. The
// search is best-effort; if it doesn't return in this
// window we move on to the next source rather than
// blocking the embed pipeline.
//
// The window is tight because the embed step has its
// own 30s budget and we run 3-4 search attempts in
// series (wy → tx → kw) — at 6s each the search layer
// consumes up to ~20s of the embed budget, which is
// already half. 6s is also enough for any of the
// per-source search APIs to return on a healthy CDN.
var onlineLyricSearchTimeout = 6 * time.Second
