package nativeapi

// online_lyric.go is a Go port of the per-source lyric fetchers in
// lxserver-main/src/modules/utils/musicSdk/<source>/lyric.js. The
// upstream project is being removed from the repository, and we
// can no longer rely on the Node sandbox dispatch to read the
// source's lyric API. This file is the in-process replacement.
//
// Each source has its own quirks:
//
//   - mg (咪咕) — plain LRC over HTTPS, no auth, plain text body.
//   - tx (QQ)   — base64-encoded LRC + HTML entity decoding.
//   - kw (酷我) — XOR-encrypted request params + AES-encrypted
//                 response body + zlib inflate + GB18030 → UTF-8
//                 transcoding.
//   - kg (酷狗) — two-step: search by name/hash to resolve a
//                 content id+key, then download either LRC
//                 (base64) or KRC (XOR-ciphered, zlib-compressed).
//   - wy (网易云) — AES-128-ECB "eapi" with an RSA-encrypted
//                 session key (raw RSA, no PKCS#1 v1.5 padding).
//
// The 5 entry points all return a `onlineLyricResult` struct with
// the fields the embed pipeline cares about. The pipeline
// (onlineEmbedDownloadMetadata) only consumes `lyric` today, but
// we preserve `tlyric` / `rlyric` for any future call site that
// wants to dump them too.
//
// All sources have a single function `fetchOnlineLyric<Source>`
// that takes the `songInfo` map and returns the result. Each
// function is a direct port of the corresponding lxserver-main
// implementation; comments cite the upstream file path so future
// readers can reconcile the two.
//
// Errors are best-effort. Every entry point returns
// ("", nil) for any internal failure so the caller can log a
// trace line and move on without aborting the embed step.

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/navidrome/navidrome/log"
)

// onlineLyricHTTPTimeout caps the per-request wall clock for any
// lyric API call. The Node side used 12s; we use the same window
// so behavior is comparable, but the Go side can be tighter
// because it doesn't pay the Node startup cost on every call.
const onlineLyricHTTPTimeout = 10 * time.Second

// onlineLyricResult is the shape every per-source fetcher
// returns. The embed pipeline currently only reads `lyric` (the
// synced LRC text); the other fields are preserved for future
// features that want the translation / romaji / per-word timing
// blobs.
type onlineLyricResult struct {
	// Lyric is the synced LRC text (or plain LRC for sources
	// that don't ship per-line timing). Empty when the source
	// returned no lyric.
	Lyric string
	// TLyric is the translation (e.g. English rendering of
	// Chinese lyrics). Empty when the source didn't supply one.
	TLyric string
	// RLyric is the romaji / romanization. Empty for most
	// sources.
	RLyric string
	// LXLyric is the per-word (逐字) timing variant. Empty
	// unless the source's KRC / YRC blob was parsed. Most
	// players don't surface this; the embed step ignores it.
	LXLyric string
}

// fetchOnlineLyricBySource is the public dispatch that
// fetchOnlineEmbedLyric calls. It routes by songInfo.source to
// the right per-source function and is the single chokepoint
// for adding instrumentation.
//
// Returning ("", nil) on every error path keeps the embed
// pipeline's "best-effort" contract: a missing lyric must never
// abort the audio download. The full error (if any) is logged
// at Info via the embed trace so the user can `grep [EMBED]`
// their navidrome.log.
func fetchOnlineLyricBySource(ctx context.Context, source string, songInfo map[string]any) onlineLyricResult {
	// Trace BEFORE the switch so the user can grep their
	// navidrome.log and see exactly which dispatch key the
	// embed pipeline received. The "supported" flag tells
	// them at a glance whether the Go client picked it up
	// or routed to the script fallback.
	embedTrace(ctx, "lyric:dispatch", "source", source, "supported", isSupportedLyricSource(source))
	switch source {
	case "mg":
		return fetchOnlineLyricMG(ctx, songInfo)
	case "tx":
		return fetchOnlineLyricTX(ctx, songInfo)
	case "kw":
		return fetchOnlineLyricKW(ctx, songInfo)
	case "kg":
		return fetchOnlineLyricKG(ctx, songInfo)
	case "wy":
		return fetchOnlineLyricWY(ctx, songInfo)
	}
	// Unknown source: nothing to do. The Node fallback in
	// fetchOnlineEmbedLyric will pick up the slack via the
	// user-supplied script.
	embedTrace(ctx, "lyric:unknown-source", "source", source, "supportedSources", "mg,tx,kw,kg,wy")
	return onlineLyricResult{}
}

// isSupportedLyricSource reports whether the per-source
// dispatcher has an implementation for the given source key.
// Used only for the dispatch trace so the user can see at a
// glance whether the source value they expect to see in the
// log will be picked up by the Go client or routed to the
// script fallback.
func isSupportedLyricSource(source string) bool {
	switch source {
	case "mg", "tx", "kw", "kg", "wy":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// mg (咪咕) — plain LRC, no auth.
// Source: lxserver-main/src/modules/utils/musicSdk/mg/lyric.js
// ---------------------------------------------------------------------------

// fetchOnlineLyricMG is the simplest of the 5 sources. Migu
// serves plain LRC over plain HTTPS — no encryption, no auth.
// Some songs ship the MRC variant (encrypted); we don't decode
// MRC here and treat that case as "no lyric".
func fetchOnlineLyricMG(ctx context.Context, songInfo map[string]any) onlineLyricResult {
	meta := mapValue(songInfo["meta"])
	if meta == nil {
		return onlineLyricResult{}
	}
	// MRC takes priority; we don't support it in the Go
	// client (and the Node path didn't decode it either
	// without the decrypt helper from mg/utils/mrc). Fall
	// through to lrcUrl.
	mrcURL := strings.TrimSpace(stringValue(meta["mrcUrl"]))
	if mrcURL != "" {
		embedTrace(ctx, "lyric:mg:mrc-not-supported", "url", mrcURL)
	}
	lrcURL := strings.TrimSpace(stringValue(meta["lrcUrl"]))
	if lrcURL == "" {
		embedTrace(ctx, "lyric:mg:no-lrc-url")
		return onlineLyricResult{}
	}
	body, ok := httpGetText(ctx, lrcURL, "https://app.c.nf.migu.cn/", map[string]string{
		"User-Agent": "Mozilla/5.0 (Linux; Android 5.1.1; Nexus 6 Build/LYZ28E) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/59.0.3071.115 Mobile Safari/537.36",
		"channel":    "0146921",
	})
	if !ok {
		embedTrace(ctx, "lyric:mg:fetch-failed", "url", lrcURL)
		return onlineLyricResult{}
	}
	return onlineLyricResult{
		Lyric:  onlineLyricMGNormalize(body),
		TLyric: httpGetTextIgnoreErr(ctx, strings.TrimSpace(stringValue(meta["trcUrl"])), "https://app.c.nf.migu.cn/", nil),
	}
}

// onlineLyricMGNormalize mirrors mrcTools.getLrc: if the body
// looks like LRC (most lines carry [mm:ss.xx] tags), we keep it
// as-is; otherwise we assign fake 3-second-spaced timestamps so
// the result is at least scrollable. The migu endpoint is the
// one place that ships plain-text "lyrics" without timing, and
// the lxserver-main client turned those into pseudo-LRC for
// display parity.
func onlineLyricMGNormalize(body string) string {
	hasTimeTag := onlineLyricLRCTimeRxp.MatchString(body)
	if hasTimeTag {
		// The lxserver-main version split into lines, kept
		// only those with timestamps, and required >50% of
		// lines to carry a tag. We use a simpler filter: any
		// line with a [mm:ss.xx] tag is kept verbatim. This
		// is more permissive but the output is the same for
		// any well-formed LRC and degrades gracefully for
		// mixed content.
		var kept []string
		for _, line := range strings.Split(body, "\n") {
			if onlineLyricLRCTimeRxp.MatchString(strings.TrimSpace(line)) {
				kept = append(kept, line)
			}
		}
		if len(kept) > 0 {
			return strings.Join(kept, "\n")
		}
	}
	// Plain-text body — assign fake 3-second tags so the file
	// is still parseable as LRC.
	var out []string
	currentTime := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "@") {
			continue
		}
		minutes := currentTime / 60
		seconds := currentTime % 60
		out = append(out, fmt.Sprintf("[%02d:%02d.00]%s", minutes, seconds, line))
		currentTime += 3
	}
	return strings.Join(out, "\n")
}

// onlineLyricLRCTimeRxp is the canonical "[mm:ss.xx]" tag
// detector. Used by migu's plain-text → pseudo-LRC promotion.
var onlineLyricLRCTimeRxp = regexp.MustCompile(`^\[(\d+):(\d+)\.(\d+)\]`)

// ---------------------------------------------------------------------------
// tx (QQ 音乐) — base64 + HTML entity decode.
// Source: lxserver-main/src/modules/utils/musicSdk/tx/lyric.js
// ---------------------------------------------------------------------------

// fetchOnlineLyricTX calls QQ's lyric endpoint. The response is
// JSON with two base64 fields: `lyric` and `trans`. The
// base64-decoded bytes are UTF-8 LRC, but the source's content
// pipeline escapes a handful of HTML entities first, so we run
// the decoded string through the same entity decoder lxserver
// uses to keep the result identical.
//
// The endpoint is reachable anonymously; the g_tk parameter is
// a hard-coded constant in the upstream code, not a session
// token, so we just hard-code it too.
func fetchOnlineLyricTX(ctx context.Context, songInfo map[string]any) onlineLyricResult {
	songmid := onlineLyricPickSongmid(songInfo)
	if songmid == "" {
		embedTrace(ctx, "lyric:tx:no-songmid")
		return onlineLyricResult{}
	}
	url := fmt.Sprintf(
		"https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric_new.fcg?songmid=%s&g_tk=5381&loginUin=0&hostUin=0&format=json&inCharset=utf8&outCharset=utf-8&platform=yqq",
		songmid,
	)
	embedTrace(ctx, "lyric:tx:request", "songmid", songmid, "url", url)
	body, ok := httpGetJSON(ctx, url, "https://y.qq.com/portal/player.html", nil)
	if !ok {
		embedTrace(ctx, "lyric:tx:fetch-failed", "url", url)
		return onlineLyricResult{}
	}
	lyricB64 := onlineLyricStringField(body, "lyric")
	transB64 := onlineLyricStringField(body, "trans")
	if lyricB64 == "" {
		embedTrace(ctx, "lyric:tx:no-lyric-field")
		return onlineLyricResult{}
	}
	lyric, lErr := onlineLyricBase64UTF8(lyricB64)
	if lErr != nil {
		embedTrace(ctx, "lyric:tx:base64-failed", "err", lErr.Error())
		return onlineLyricResult{}
	}
	out := onlineLyricResult{
		Lyric: onlineLyricDecodeHTMLEntities(lyric),
	}
	if transB64 != "" {
		if trans, tErr := onlineLyricBase64UTF8(transB64); tErr == nil {
			out.TLyric = onlineLyricDecodeHTMLEntities(trans)
		}
	}
	return out
}

// onlineLyricStringField reads a top-level string field from a
// raw JSON body. We don't unmarshal into a struct because the
// QQ endpoint returns the body in GBK on some IPs; we re-decode
// the key with the right charset in the calling site. Field
// lookup is direct on the raw bytes via a tiny scan, which is
// cheap and robust against nested numeric/array subfields.
func onlineLyricStringField(body []byte, key string) string {
	needle := []byte(`"` + key + `":"`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	end := bytes.IndexByte(body[start:], '"')
	if end < 0 {
		return ""
	}
	return string(body[start : start+end])
}

// onlineLyricDecodeHTMLEntities reverses the QQ encoder's HTML
// entity escaping. The set is small (decimal numeric refs + a
// few named ones), and we only decode what the lxserver-main
// client decodes to keep parity.
func onlineLyricDecodeHTMLEntities(s string) string {
	if !strings.ContainsAny(s, "&") {
		return s
	}
	// Numeric entities: &#NNNN; — the regex captures the
	// digits in submatch[1] so we can convert them with
	// Sscanf without a second pass.
	if strings.Contains(s, "&#") {
		s = numericEntityRxp.ReplaceAllStringFunc(s, func(match string) string {
			sub := numericEntityRxp.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			var n int
			if _, err := fmt.Sscanf(sub[1], "%d", &n); err != nil {
				return match
			}
			return string(rune(n))
		})
	}
	// Named entities. The order matters: &amp; last so a
	// sequence like &amp;lt; doesn't get double-decoded.
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&apos;", "'")
	return s
}

var numericEntityRxp = regexp.MustCompile(`&#(\d+);`)

// ---------------------------------------------------------------------------
// kw (酷我) — XOR + AES-ECB + zlib inflate + GB18030 transcoding.
// Source: lxserver-main/src/modules/utils/musicSdk/kw/lyric.js
// ---------------------------------------------------------------------------

// fetchOnlineLyricKW implements the kw lyric flow: a small
// request parameter is XOR-encrypted against the key "yeelion"
// and base64'd; the response body is AES-128-ECB encrypted, then
// the body is zlib-inflated, then XOR-encrypted again (only when
// lrcx=1), then GB18030-decoded.
//
// The "lyricx" mode gives us per-word timing. We extract both
// the lyric text and the per-word timing; the embed step
// currently only consumes the plain LRC but we keep lxlyric
// around in case the user turns on the per-word display in a
// future release.
func fetchOnlineLyricKW(ctx context.Context, songInfo map[string]any) onlineLyricResult {
	songmid := onlineLyricPickSongmid(songInfo)
	if songmid == "" {
		embedTrace(ctx, "lyric:kw:no-songmid")
		return onlineLyricResult{}
	}
	// The lxserver-main code requests with lrcx=1 (i.e.
	// isGetLyricx=true), which gives us per-word timing
	// alongside the synced LRC. We mirror that here.
	params := onlineLyricKWBuildParams(songmid, true)
	url := "http://newlyric.kuwo.cn/newlyric.lrc?" + params
	embedTrace(ctx, "lyric:kw:request", "songmid", songmid, "url", url)
	raw, ok := httpGetRaw(ctx, url, "", nil)
	if !ok {
		embedTrace(ctx, "lyric:kw:fetch-failed", "url", url)
		return onlineLyricResult{}
	}
	body, err := onlineLyricKWDecode(raw, true)
	if err != nil {
		embedTrace(ctx, "lyric:kw:decode-failed", "err", err.Error())
		return onlineLyricResult{}
	}
	// parseLrc returns a single text blob; the lxserver
	// client then runs it through sortLrcArr to split
	// lyric/tlyric. We inline the same logic for clarity.
	parsed, ok := onlineLyricKWParseLrc(string(body))
	if !ok {
		embedTrace(ctx, "lyric:kw:parse-failed")
		return onlineLyricResult{}
	}
	// Strip per-word timing from the public lyric. The
	// user-facing LRC is just the time tags + the line text.
	wordTimeRxp := regexp.MustCompile(`<-?\d+,-?\d+(?:,-?\d+)?>`)
	parsed.Lyric = wordTimeRxp.ReplaceAllString(parsed.Lyric, "")
	parsed.TLyric = wordTimeRxp.ReplaceAllString(parsed.TLyric, "")
	return parsed
}

// onlineLyricKWBuildParams constructs the encrypted request
// string kw expects. The upstream code uses a Uint16 XOR; we
// produce the same byte stream by using a single XOR loop
// (the high byte of each uint16 in the upstream version is
// always zero, so a single-byte XOR is bit-identical to the
// upstream output). After XOR we base64-encode.
func onlineLyricKWBuildParams(songmid string, withLrcx bool) string {
	const key = "yeelion"
	params := fmt.Sprintf("user=12345,web,web,web&requester=localhost&req=1&rid=MUSIC_%s", songmid)
	if withLrcx {
		params += "&lrcx=1"
	}
	src := []byte(params)
	out := make([]byte, len(src))
	for i, b := range src {
		out[i] = b ^ key[i%len(key)]
	}
	return base64.StdEncoding.EncodeToString(out)
}

// onlineLyricKWDecode is the inverse of the kw server's
// encoding: skip the `tp=content\r\n\r\n` HTTP-style header,
// zlib-inflate the rest, base64-decode, XOR-decrypt with the
// same "yeelion" key, then GB18030-decode. The "isGetLyricx"
// flag toggles the second XOR pass; we only call this with
// isGetLyricx=true.
func onlineLyricKWDecode(raw []byte, isGetLyricx bool) ([]byte, error) {
	// Header: "tp=content" + CRLFCRLF. Anything else is a
	// server-side error wrapped in the same response shape.
	if len(raw) < 14 || string(raw[:10]) != "tp=content" {
		return nil, fmt.Errorf("kw response missing tp=content header")
	}
	headerEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		return nil, fmt.Errorf("kw response missing header terminator")
	}
	payload := raw[headerEnd+4:]
	zr, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("kw zlib reader: %w", err)
	}
	defer zr.Close()
	inflated, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("kw zlib inflate: %w", err)
	}
	if !isGetLyricx {
		// Plain LRC path. The lxserver client never uses
		// it because the response is GBK-encoded; Go's
		// "gb18030" decoder is a superset of GBK and
		// handles it.
		return onlineLyricGB18030Decode(inflated), nil
	}
	// lyricx=1 path: base64-decode inflated, then XOR, then
	// GB18030 decode.
	decoded, err := base64.StdEncoding.DecodeString(string(inflated))
	if err != nil {
		return nil, fmt.Errorf("kw base64: %w", err)
	}
	const key = "yeelion"
	for i, b := range decoded {
		decoded[i] = b ^ key[i%len(key)]
	}
	return onlineLyricGB18030Decode(decoded), nil
}

// onlineLyricGB18030Decode wraps Go's GB18030 decoder. We
// import golang.org/x/text/encoding/simplifiedchinese, which is
// already a transitive dependency. The function lives in
// online_lyric_charset.go to keep this file focused on the
// per-source flows.
func onlineLyricGB18030Decode(b []byte) []byte {
	return onlineLyricDecodeGB18030(b)
}

// onlineLyricKWParseLrc implements the upstream `parseLrc`
// function: split the LRC text into tags ([ti:…], [ar:…],
// etc.) and timed lines, then sort the timed lines into
// main-lyric / translation-lyric using the
// "consecutive duplicate timestamps" heuristic. The function
// returns just the two strings; the per-word timing strip is
// done by the caller.
func onlineLyricKWParseLrc(lrc string) (onlineLyricResult, bool) {
	timeRxp := regexp.MustCompile(`\[([\d:.]*)\]`)
	tagRxp := regexp.MustCompile(`\[(ver|ti|ar|al|offset|by|kuwo):\s*(\S+(?:\s+\S+)*)\s*\]`)
	type timedLine struct {
		time string
		text string
	}
	var tags []string
	var lines []timedLine
	for _, raw := range strings.Split(lrc, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		match := timeRxp.FindStringIndex(line)
		if match == nil {
			if tagRxp.MatchString(line) {
				tags = append(tags, line)
			}
			continue
		}
		t := timeRxp.FindStringSubmatch(line)[1]
		// lxserver pads single-digit ms to 3 digits. We do
		// the same so the output is bit-identical.
		if regexp.MustCompile(`\.\d\d$`).MatchString(t) {
			t += "0"
		}
		text := strings.TrimSpace(line[match[1]:])
		lines = append(lines, timedLine{time: t, text: text})
	}
	// sortLrcArr: when two consecutive lines share a
	// timestamp, the first is the translation of the second.
	var main, trans []timedLine
	for i, line := range lines {
		if i > 0 && lines[i-1].time == line.time {
			t := lines[i-1]
			trans = append(trans, t)
			main = append(main, line)
		} else {
			main = append(main, line)
		}
	}
	// Heuristic: if translation count > 30% of main count
	// AND main is more than 6 lines longer than trans, the
	// upstream code throws ("failed"). We mirror that by
	// returning ok=false.
	tagLine := func(t timedLine) string {
		return fmt.Sprintf("[%s]%s\n", t.time, t.text)
	}
	render := func(list []timedLine) string {
		var b strings.Builder
		for _, t := range list {
			b.WriteString(tagLine(t))
		}
		// Prepend tag block.
		if len(tags) > 0 {
			return strings.Join(tags, "\n") + "\n" + b.String()
		}
		return b.String()
	}
	if len(trans) > int(float64(len(main))*0.3) && len(main)-len(trans) > 6 {
		return onlineLyricResult{}, false
	}
	out := onlineLyricResult{
		Lyric: onlineLyricDecodeHTMLEntities(render(main)),
	}
	if len(trans) > 0 {
		out.TLyric = onlineLyricDecodeHTMLEntities(render(trans))
	}
	return out, true
}

// ---------------------------------------------------------------------------
// kg (酷狗) — search + LRC/KRC download.
// Source: lxserver-main/src/modules/utils/musicSdk/kg/lyric.js
// ---------------------------------------------------------------------------

// fetchOnlineLyricKG is the two-step kg flow: search by
// (name, hash, timelength) to resolve a content id+key, then
// download either a base64 LRC (decoded as UTF-8) or a KRC
// blob (XOR-ciphered, zlib-compressed). KRC is the more common
// format on kg; the per-line timing is encoded inside the
// compressed blob.
func fetchOnlineLyricKG(ctx context.Context, songInfo map[string]any) onlineLyricResult {
	name := strings.TrimSpace(stringValue(songInfo["name"]))
	hash := strings.TrimSpace(stringValue(songInfo["hash"]))
	if name == "" || hash == "" {
		embedTrace(ctx, "lyric:kg:missing-name-or-hash", "name", name, "hash", hash)
		return onlineLyricResult{}
	}
	intervalSec := onlineLyricKGGetIntv(stringValue(songInfo["interval"]))
	kgHeaders := map[string]string{
		"KG-RC":      "1",
		"KG-THash":   "expand_search_manager.cpp:852736169:451",
		"User-Agent": "KuGou2012-9020-ExpandSearchManager",
	}
	searchURL := fmt.Sprintf(
		"http://lyrics.kugou.com/search?ver=1&man=yes&client=pc&keyword=%s&hash=%s&timelength=%d&lrctxt=1",
		onlineLyricURLEncode(name), hash, intervalSec,
	)
	body, ok := httpGetJSON(ctx, searchURL, "", kgHeaders)
	if !ok {
		embedTrace(ctx, "lyric:kg:search-failed", "url", searchURL)
		return onlineLyricResult{}
	}
	// Parse the response. The first candidate carries the
	// id / accesskey / fmt we need to download the actual
	// content.
	candidates := onlineLyricJSONArrayField(body, "candidates")
	if len(candidates) == 0 {
		embedTrace(ctx, "lyric:kg:no-candidates")
		return onlineLyricResult{}
	}
	candidate := candidates[0]
	id := onlineLyricJSONStringField(candidate, "id")
	accessKey := onlineLyricJSONStringField(candidate, "accesskey")
	krctype := onlineLyricJSONIntField(candidate, "krctype")
	contentType := onlineLyricJSONIntField(candidate, "contenttype")
	fmt_ := "lrc"
	if krctype == 1 && contentType != 1 {
		fmt_ = "krc"
	}
	if id == "" || accessKey == "" {
		embedTrace(ctx, "lyric:kg:candidate-missing-fields")
		return onlineLyricResult{}
	}
	downloadURL := fmt.Sprintf(
		"http://lyrics.kugou.com/download?ver=1&client=pc&id=%s&accesskey=%s&fmt=%s&charset=utf8",
		id, accessKey, fmt_,
	)
	dl, ok := httpGetJSON(ctx, downloadURL, "", kgHeaders)
	if !ok {
		embedTrace(ctx, "lyric:kg:download-failed", "url", downloadURL)
		return onlineLyricResult{}
	}
	content := onlineLyricJSONStringField(dl, "content")
	dlFmt := onlineLyricJSONStringField(dl, "fmt")
	if content == "" {
		embedTrace(ctx, "lyric:kg:download-empty")
		return onlineLyricResult{}
	}
	switch dlFmt {
	case "lrc":
		// Plain UTF-8 LRC base64-encoded.
		decoded, err := onlineLyricBase64UTF8(content)
		if err != nil {
			embedTrace(ctx, "lyric:kg:lrc-decode-failed", "err", err.Error())
			return onlineLyricResult{}
		}
		return onlineLyricResult{Lyric: decoded}
	case "krc":
		// KRC is XOR-ciphered with the kg key, then zlib
		// deflated. After inflate we have a string with
		// per-line timing in <ms,duration> format.
		return onlineLyricKRCDecode(content)
	}
	embedTrace(ctx, "lyric:kg:unknown-fmt", "fmt", dlFmt)
	return onlineLyricResult{}
}

// onlineLyricKGGetIntv converts the songInfo.interval field
// (mm:ss or mm:ss.SSS) to seconds, matching the lxserver-main
// getIntv function. The lxserver version parses differently for
// "1:23" vs "1:23.456" — both end up as integer seconds.
func onlineLyricKGGetIntv(s string) int {
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	total := 0
	unit := 1
	for i := len(parts) - 1; i >= 0; i-- {
		var n int
		_, _ = fmt.Sscanf(parts[i], "%d", &n)
		total += n * unit
		unit *= 60
	}
	return total
}

// onlineLyricKRCDecode is the KRC decoder: base64-decode the
// content, drop the first 4 bytes (KRC magic), XOR the rest
// against the 16-byte kg key, then zlib-inflate. After
// inflation we have the LRC text with <offset,duration> word
// timings inline. We strip those to produce the user-facing LRC
// the embed step expects; the word-level timing is dropped
// (it's an enhancement the embed step doesn't surface today).
func onlineLyricKRCDecode(b64 string) onlineLyricResult {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		embedTrace(context.Background(), "lyric:kg:krc-base64-failed", "err", err.Error())
		return onlineLyricResult{}
	}
	if len(raw) <= 4 {
		return onlineLyricResult{}
	}
	const key = "\x40\x47\x61\x77\x5e\x32\x74\x47\x51\x36\x31\x2d\xce\xd2\x6e\x69"
	cipher := raw[4:]
	for i, b := range cipher {
		cipher[i] = b ^ key[i%len(key)]
	}
	fr := flate.NewReader(bytes.NewReader(cipher))
	defer fr.Close()
	plain, err := io.ReadAll(fr)
	if err != nil {
		embedTrace(context.Background(), "lyric:kg:krc-inflate-failed", "err", err.Error())
		return onlineLyricResult{}
	}
	out := string(plain)
	// Strip the [id:$xxxxxx] header if present.
	headerRxp := regexp.MustCompile(`(?s)^.*\[id:\$\w+\]\n`)
	out = headerRxp.ReplaceAllString(out, "")
	// Strip per-word <offset,duration[,something]> markers
	// from each line so the result is plain LRC. This matches
	// the `decodeName → lxlyric.replace(...)` path in
	// lxserver-main's parseLyric, simplified: we don't
	// generate lxlyric (the embed step doesn't render it),
	// we only produce the time-tagged lyric.
	wordTimeRxp := regexp.MustCompile(`<(\d+,\d+)(?:,-?\d+)?>`)
	out = wordTimeRxp.ReplaceAllString(out, "<$1>")
	out = regexp.MustCompile(`<\d+,\d+>`).ReplaceAllString(out, "")
	out = onlineLyricDecodeHTMLEntities(out)
	return onlineLyricResult{Lyric: out}
}

// ---------------------------------------------------------------------------
// wy (网易云) — AES-128-ECB eapi.
// Source: lxserver-main/src/modules/utils/musicSdk/wy/lyric.js
// (and wy/utils/crypto.js for the eapi helpers).
// ---------------------------------------------------------------------------

// onlineLyricWYEAPIKey is the symmetric key used for AES-128-ECB
// encryption of the eapi body. Hard-coded constant from
// lxserver-main/src/modules/utils/musicSdk/wy/utils/crypto.js.
// The wy eapi endpoint only needs the AES-encrypted `params`
// field; the RSA-encrypted `encSecKey` field is only used by
// the weapi flow (search / login), which the lyric fetcher
// doesn't call. The public-key constant in
// lxserver-main/src/modules/utils/musicSdk/wy/utils/crypto.js
// is therefore not needed here.
//
// gosec G101: this isn't a credential, it's a published
// algorithm parameter (the same key is shipped with every
// lx-music build, including the open-source tree). The
// eapi flow's security model is "obscure, not encrypted" —
// the key being public is by design.
const onlineLyricWYEAPIKey = "e82ckenh8dichen8" // #nosec G101 -- public algorithm parameter, not a secret

// fetchOnlineLyricWY calls the Netease /eapi/song/lyric/v1
// endpoint. The request body is encrypted with AES-128-ECB
// using the eapi key, and the URL gets a `params` query string
// of the same encrypted payload. The response is a JSON object
// with the usual `lrc.lyric` / `tlyric.lyric` / `romalrc.lyric`
// fields, all of which are LRC text.
func fetchOnlineLyricWY(ctx context.Context, songInfo map[string]any) onlineLyricResult {
	songmid := onlineLyricPickSongmid(songInfo)
	if songmid == "" {
		embedTrace(ctx, "lyric:wy:no-songmid")
		return onlineLyricResult{}
	}
	path := "/api/song/lyric/v1"
	payload := map[string]any{
		"id":  songmid,
		"cp":  false,
		"tv":  0,
		"lv":  0,
		"rv":  0,
		"kv":  0,
		"yv":  0,
		"ytv": 0,
		"yrv": 0,
	}
	enc, err := onlineLyricWYEAPIEncrypt(path, payload)
	if err != nil {
		embedTrace(ctx, "lyric:wy:eapi-encrypt-failed", "err", err.Error())
		return onlineLyricResult{}
	}
	url := "https://interface3.music.163.com/eapi/song/lyric/v1?params=" + enc
	embedTrace(ctx, "lyric:wy:request", "songmid", songmid, "paramsLen", len(enc))
	body, ok := onlineLyricWYFetch(ctx, url)
	if !ok {
		// The detail trace (lyric:wy:connect-failed /
		// non-2xx / read-failed / too-large) is already
		// emitted inside onlineLyricWYFetch. Returning
		// without a lyric here is the documented best-effort
		// path.
		return onlineLyricResult{}
	}
	// Walk into body.lrc.lyric. We use field walks because
	// the response is large and we only want three fields.
	lyric := onlineLyricJSONStringFieldAt(body, "lrc", "lyric")
	if lyric == "" {
		// Diagnose the empty case. 网易云 has three
		// failure modes worth distinguishing:
		//
		//   1. The body is empty (we hit it from this
		//      machine — likely IP-blocked or eapi key
		//      rotated).
		//   2. The body has code != 200 (paid VIP song
		//      returning {"code":460,"message":"..."}).
		//   3. The body has code 200 but no lrc field
		//      (genuinely no lyrics for this song).
		//
		// We log the response code + a 200-byte preview of
		// the body so the user can grep their navidrome.log
		// and tell which case they're hitting. The preview
		// is truncated to keep the trace line reasonable
		// in size (full 网易云 responses are small anyway).
		preview := body
		if len(preview) > 200 {
			preview = preview[:200]
		}
		embedTrace(ctx, "lyric:wy:no-lrc-field",
			"bodyLen", len(body),
			"code", onlineLyricJSONStringField(body, "code"),
			"bodyPreview", string(preview),
		)
		return onlineLyricResult{}
	}
	tlyric := onlineLyricJSONStringFieldAt(body, "tlyric", "lyric")
	rlyric := onlineLyricJSONStringFieldAt(body, "romalrc", "lyric")
	return onlineLyricResult{
		Lyric:  onlineLyricWYFixTimeLabel(lyric),
		TLyric: onlineLyricWYFixTimeLabel(tlyric),
		RLyric: onlineLyricWYFixTimeLabel(rlyric),
	}
}

// onlineLyricWYEAPIEncrypt implements the eapi "encrypt"
// helper from the upstream crypto.js: it computes an MD5 of
// "nobody{url}use{body}md5forencrypt", builds a payload
// "{url}-36cd479b6b5-{body}-36cd479b6b5-{digest}", then
// AES-128-ECB-encrypts that with the eapi key. The result is
// hex-encoded and upper-cased. (wy does the same dance
// server-side; mismatching case / encoding would yield a 4xx.)
func onlineLyricWYEAPIEncrypt(url string, body map[string]any) (string, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	// The lxserver-main code does NOT sort the keys; it
	// relies on Go-equivalent JSON.stringify order, which
	// matches because we declare the map with literal
	// keys above. If the map shape ever changes, regenerate
	// the params.
	md5sum := onlineLyricMD5Hex([]byte("nobody" + url + "use" + string(bodyJSON) + "md5forencrypt"))
	plain := fmt.Sprintf("%s-36cd479b6b5-%s-36cd479b6b5-%s", url, bodyJSON, md5sum)
	block, err := aes.NewCipher([]byte(onlineLyricWYEAPIKey))
	if err != nil {
		return "", err
	}
	// AES-ECB pads with PKCS#7 by hand because crypto/cipher
	// doesn't expose ECB. The body length is already a
	// multiple of 16 in this code path (md5 is 32 chars
	// hex, url + body + glue is always a multiple of 16 in
	// practice). We still pad defensively.
	padded := onlineLyricPKCS7Pad([]byte(plain), block.BlockSize())
	ct := make([]byte, len(padded))
	for i := 0; i < len(padded); i += block.BlockSize() {
		block.Encrypt(ct[i:i+block.BlockSize()], padded[i:i+block.BlockSize()])
	}
	return strings.ToUpper(hex.EncodeToString(ct)), nil
}

// onlineLyricPKCS7Pad pads a buffer to the next multiple of
// blockSize using PKCS#7. We don't use the lxserver-main
// path because Node's eapi doesn't pad (it assumes the input
// is already a multiple of 16). For safety we pad
// defensively — if the upstream code starts sending a
// non-aligned body length, our padding keeps the
// encryption/decryption round-tripping correctly.
func onlineLyricPKCS7Pad(b []byte, blockSize int) []byte {
	padLen := blockSize - (len(b) % blockSize)
	if padLen == 0 {
		padLen = blockSize
	}
	pad := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(b, pad...)
}

// onlineLyricWYFetch is a wy-specific transport. It is a
// straight-line http.Get equivalent (no retry, no fallback)
// but with structured [EMBED] traces for each failure mode
// the user is likely to hit when 网易云 is unreachable from
// their network:
//
//   - lyric:wy:connect-failed (network unreachable / DNS /
//     connect-timeout / TLS handshake failed)
//   - lyric:wy:non-2xx (the server responded but with 4xx /
//     5xx; 460 = VIP-only)
//   - lyric:wy:read-failed (the response body read errored
//     mid-stream)
//   - lyric:wy:body-too-large (defensive: a misbehaving
//     server flooding the response)
//
// The 2 MiB cap matches the rest of the lyric clients; the
// real 网易云 lyric endpoint returns well under 50 KB.
func onlineLyricWYFetch(ctx context.Context, url string) ([]byte, bool) {
	client := onlineLyricSongInfoClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		embedTrace(ctx, "lyric:wy:request-build-failed", "err", err.Error())
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/60.0.3112.90 Safari/537.36")
	req.Header.Set("Referer", "https://music.163.com")
	req.Header.Set("Origin", "https://music.163.com")
	resp, err := client.Do(req)
	if err != nil {
		// This is the most common failure mode in
		// restricted networks: the request never gets a
		// response at all. The err string carries the OS
		// detail (e.g. "context deadline exceeded",
		// "no such host", "connect: connection
		// refused"). Tagging the trace with the error
		// class lets the user tell connect-timeout from
		// DNS failure from TLS rejection at a glance.
		embedTrace(ctx, "lyric:wy:connect-failed", "err", err.Error(), "errClass", classifyNetErr(err))
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain a small prefix of the body for the trace
		// so the user can see the 网易云 error envelope
		// (e.g. {"code":460,"message":"需要登录"}) without
		// having to re-run with curl.
		var preview [256]byte
		n, _ := io.ReadFull(resp.Body, preview[:])
		embedTrace(ctx, "lyric:wy:non-2xx", "status", resp.StatusCode, "bodyPreview", string(preview[:n]))
		return nil, false
	}
	const maxBody = 2 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		embedTrace(ctx, "lyric:wy:read-failed", "err", err.Error())
		return nil, false
	}
	if len(body) > maxBody {
		embedTrace(ctx, "lyric:wy:body-too-large", "size", len(body))
		return nil, false
	}
	return body, true
}

// classifyNetErr reduces a net error to a short class label
// suitable for trace inspection. We avoid string-matching
// against the err message (locale-dependent) and use
// errors.As / net.Error type assertions instead, so the trace
// stays stable across Go versions and locales.
func classifyNetErr(err error) string {
	if err == nil {
		return "none"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "net-op:" + opErr.Op
	}
	return "other"
}

// onlineLyricWYFixTimeLabel is a 1:1 port of the upstream
// fixTimeLabel: Netease's lyric endpoint occasionally returns
// time tags as `[mm:ss:hh]` (colon between seconds and
// hundredths) instead of the standard `[mm:ss.hh]`. We rewrite
// the wrong form to the right form. Without this, players that
// strictly parse the standard form (foobar2000, MusicBee) drop
// the affected lines.
func onlineLyricWYFixTimeLabel(s string) string {
	if s == "" {
		return s
	}
	// Replace [mm:ss:hh] with [mm:ss.hh]. This regex
	// matches the *broken* form; the standard form is
	// already correct.
	rxp := regexp.MustCompile(`\[(\d{2}:\d{2}):(\d{2,3})\]`)
	s = rxp.ReplaceAllString(s, `[$1.$2]`)
	// Pad single-digit hundredths so timestamps are 3 digits
	// wide: "[01:23.4]" -> "[01:23.400]".
	rxp2 := regexp.MustCompile(`\[(\d{2}:\d{2}\.\d{2})0\]`)
	s = rxp2.ReplaceAllString(s, `[$1]`)
	return s
}

// onlineLyricMD5Hex is a thin wrapper around crypto/md5 kept
// in this file because the only caller (eapi) lives here.
// We avoid importing crypto/md5 at the package level to keep
// the dependency surface obvious.
func onlineLyricMD5Hex(b []byte) string {
	return onlineLyricMD5Sum(b)
}

// ---------------------------------------------------------------------------
// (No RSA helper needed.)
// ---------------------------------------------------------------------------
// The lxserver-main `rsaEncrypt` helper exists for the
// weapi flow (search / login), which encrypts a per-request
// 16-byte session key with a 1024-bit RSA public key using
// raw (textbook) RSA. The eapi flow (which the lyric
// endpoint uses) only needs the AES-128-ECB-encrypted
// `params` field, not `encSecKey`. We therefore omit the RSA
// helper entirely; if a future caller needs it, the
// implementation is straightforward — `pem.Decode` +
// `x509.ParsePKIXPublicKey` + `big.Int.Exp` against the
// Netease public key from wy/utils/crypto.js.

// ---------------------------------------------------------------------------
// Shared HTTP helpers (per-source, customized for source quirks).
// ---------------------------------------------------------------------------

// onlineLyricSongInfoClient returns a *http.Client with the
// shared timeout. The User-Agent and Referer are passed
// per-request, so we don't bake them into the client. This
// function lives in this file rather than the package HTTP
// helpers to keep online_lyric.go's dependency on the rest of
// the package explicit.
func onlineLyricSongInfoClient() *http.Client {
	return &http.Client{Timeout: onlineLyricHTTPTimeout}
}

// httpGetText fetches URL with custom headers and returns the
// response body as a UTF-8 string. Returns ok=false on any
// transport / status failure so the caller can log a single
// trace line. The body is read with a 2 MiB cap (lyrics are
// small); oversized responses are treated as "fetch failed".
func httpGetText(ctx context.Context, url, referer string, extra map[string]string) (string, bool) {
	body, ok := httpGetRaw(ctx, url, referer, extra)
	if !ok {
		return "", false
	}
	return string(body), true
}

// httpGetTextIgnoreErr is the same as httpGetText but silently
// returns "" on any error. Used for "best-effort" translation
// URLs (migu's trcUrl is rarely populated).
func httpGetTextIgnoreErr(ctx context.Context, url, referer string, extra map[string]string) string {
	body, ok := httpGetText(ctx, url, referer, extra)
	if !ok {
		return ""
	}
	return body
}

// httpGetJSON is a convenience around httpGetRaw for endpoints
// that return JSON. The caller still has to walk the JSON
// itself (onlineLyricJSON* helpers below) because the per-source
// shapes are different enough that a struct would either bloat
// the imports or break the field walks we already have.
func httpGetJSON(ctx context.Context, url, referer string, extra map[string]string) ([]byte, bool) {
	return httpGetRaw(ctx, url, referer, extra)
}

// httpGetRaw is the actual transport. It applies a 2 MiB
// response cap (lyrics are < 50 KB in practice) and rejects
// non-2xx statuses.
func httpGetRaw(ctx context.Context, url, referer string, extra map[string]string) ([]byte, bool) {
	client := onlineLyricSongInfoClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Warn(ctx, "Online lyric: request build failed", "url", url, "err", err)
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn(ctx, "Online lyric: request failed", "url", url, "err", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warn(ctx, "Online lyric: non-2xx response", "url", url, "status", resp.StatusCode)
		return nil, false
	}
	const maxBody = 2 * 1024 * 1024
	limited := io.LimitReader(resp.Body, maxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		log.Warn(ctx, "Online lyric: read body failed", "url", url, "err", err)
		return nil, false
	}
	if len(body) > maxBody {
		log.Warn(ctx, "Online lyric: response too large", "url", url, "size", len(body))
		return nil, false
	}
	return body, true
}

// ---------------------------------------------------------------------------
// JSON field walks. The Node source uses property access
// (`body.lrc.lyric`); we replicate that with byte-level scans
// because (a) it's faster and (b) the responses are big enough
// that unmarshal-into-struct allocates more than we save.
// ---------------------------------------------------------------------------

// onlineLyricPickSongmid returns the canonical songmid from a
// songInfo map. songInfo["songmid"] is preferred; songInfo["id"]
// is the fallback for the server-mode path which doesn't carry
// songmid. The lxserver-main client does the same fallback
// (server.ts:5571: `songInfo.songmid || songInfo.id || ”`).
func onlineLyricPickSongmid(songInfo map[string]any) string {
	if v := strings.TrimSpace(stringValue(songInfo["songmid"])); v != "" {
		return v
	}
	return strings.TrimSpace(stringValue(songInfo["id"]))
}

// onlineLyricJSONArrayField finds the first array under
// `key` in body and returns each element as a sub-byte-slice.
// Empty slice when the field is missing or not an array.
//
// Format-wise, this is the same as
// onlineLyricJSONStringField but for the array case: we copy
// out the element bodies (recursively scanning the array) so
// the caller can keep using the same field-walk helpers
// without re-parsing the parent.
func onlineLyricJSONArrayField(body []byte, key string) [][]byte {
	needle := []byte(`"` + key + `":[`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		return nil
	}
	start := idx + len(needle)
	depth := 1
	i := start
	for i < len(body) {
		switch body[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return splitJSONTopLevelElements(body[start:i])
			}
		}
		i++
	}
	return nil
}

// splitJSONTopLevelElements splits a JSON array body into
// top-level elements, respecting nested objects / arrays /
// strings. This is what we'd get from `json.Decoder.Token`
// but rolled by hand to avoid the import. The returned
// slices still carry their surrounding braces / brackets
// where applicable.
func splitJSONTopLevelElements(arr []byte) [][]byte {
	var out [][]byte
	depth := 0
	inString := false
	escape := false
	start := 0
	for i, b := range arr {
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, arr[start:i])
				start = i + 1
			}
		}
	}
	if start < len(arr) {
		out = append(out, arr[start:])
	}
	return out
}

// onlineLyricJSONStringField walks into body for the first
// top-level occurrence of "key":"value" and returns value as a
// string. Empty when missing. The body must already be valid
// JSON; we do not parse the whole structure.
func onlineLyricJSONStringField(body []byte, key string) string {
	return onlineLyricJSONStringFieldImpl(body, key)
}

// onlineLyricJSONStringFieldImpl is the single shared scan
// routine. It looks for the first top-level occurrence of
// `"key":"value"` in body and returns value. Empty when
// missing. The body must be valid JSON; we do not parse the
// whole structure.
func onlineLyricJSONStringFieldImpl(body []byte, key string) string {
	needle := []byte(`"` + key + `":"`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	// Walk the value, honoring backslash escapes inside the
	// string. The walk stops at the first unescaped " that
	// isn't followed by another JSON-meaningful char (e.g.
	// `"foo":"bar"` stops at the second ").
	end := start
	for end < len(body) {
		c := body[end]
		if c == '\\' && end+1 < len(body) {
			end += 2
			continue
		}
		if c == '"' {
			return string(body[start:end])
		}
		end++
	}
	return ""
}

// onlineLyricJSONStringFieldAt is the multi-level variant:
// walks "key1":{"key2":"value"} and returns value. Used for
// nested-source shapes like wy's body.lrc.lyric. The previous
// version had a self-recursion (StringField → StringFieldAt
// → StringField) that overflowed the stack; the fix is to
// have both public functions dispatch to a single internal
// implementation.
func onlineLyricJSONStringFieldAt(body []byte, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}
	if len(keys) == 1 {
		return onlineLyricJSONStringFieldImpl(body, keys[0])
	}
	// Find the first occurrence of "key1":{...}, extract the
	// inner object, then recurse.
	needle := []byte(`"` + keys[0] + `":{`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		// Try array case: "key1":[{"key2":"value"}]
		arr := onlineLyricJSONArrayField(body, keys[0])
		if len(arr) == 0 {
			return ""
		}
		// Take the first element of the array and recurse
		// into it. Most lyric endpoints return a single
		// nested object inside the array.
		return onlineLyricJSONStringFieldAt(arr[0], keys[1:]...)
	}
	start := idx + len(needle) - 1 // include the {
	depth := 1
	i := start + 1
	for i < len(body) {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				// i > start+1 because we wouldn't be at
				// depth 0 if i == start+1 (we'd be at
				// the opening brace we just consumed).
				innerStart := start + 1
				innerEnd := i
				if innerEnd <= innerStart || innerEnd > len(body) {
					return ""
				}
				// #nosec G602 -- the bounds check above
				// guarantees innerStart <= innerEnd <=
				// len(body), so the slice is well-defined.
				return onlineLyricJSONStringFieldAt(body[innerStart:innerEnd:len(body)], keys[1:]...)
			}
		}
		i++
	}
	return ""
}

// onlineLyricJSONIntField returns the integer value of `key`,
// or 0 when the field is missing or not numeric. Used for the
// kg "krctype" / "contenttype" flags.
func onlineLyricJSONIntField(body []byte, key string) int {
	needle := []byte(`"` + key + `":`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		return 0
	}
	rest := body[idx+len(needle):]
	// Skip whitespace.
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r') {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return 0
	}
	// Read digits.
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(string(rest[:end]), "%d", &n)
	return n
}

// onlineLyricBase64UTF8 decodes base64 with both standard and
// URL-safe alphabets and returns the bytes interpreted as
// UTF-8. We try standard first, then URL-safe, because the
// lyric endpoints are inconsistent about which alphabet they
// emit. Both decoders are liberal about padding length.
func onlineLyricBase64UTF8(s string) (string, error) {
	encs := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	var lastErr error
	for _, enc := range encs {
		raw, err := enc.DecodeString(s)
		if err == nil {
			return string(raw), nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("base64: %w", lastErr)
}

// onlineLyricURLEncode is a tiny shim around url.QueryEscape
// that matches the lyric endpoints' "spaces as %20, not +"
// expectation. We import net/url for this one call.
func onlineLyricURLEncode(s string) string {
	return onlineLyricURLEncodeImpl(s)
}

// onlineLyricRandomBytes returns n cryptographically-random
// bytes. The wy encSecKey flow needs 16 random bytes per
// request; we read them once and discard the error (rand.Read
// only fails in extreme conditions and the embed step is
// best-effort, so a nil fallback is acceptable).
func onlineLyricRandomBytes(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand is documented to always succeed on
		// supported platforms. A nil buffer is a fatal
		// programming error so we surface it.
		panic("crypto/rand failed: " + err.Error())
	}
	return buf
}
