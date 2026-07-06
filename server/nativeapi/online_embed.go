package nativeapi

// online_embed.go wires cover / metadata / lyrics embedding into the
// download flow. The strategy mirrors lxserver-main's
// fileCache.downloadAndCache: after a successful audio download we
// (1) fetch the cover art from songInfo.img / meta.picUrl, (2) ask
// the source script for lyrics (when supported), then (3) run a
// single ffmpeg pass to write all three into the file's native
// tag container. Failure on any of those steps is logged but does
// not fail the download — the user already has the audio on disk.
//
// We intentionally do NOT pull a CGO tag library into the Go
// process: ffmpeg is already a hard dependency of navidrome
// (core/ffmpeg), ships everywhere, and handles mp3/flac/m4a/ogg/opus
// tag containers with one invocation.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/core/ffmpeg"
	"github.com/navidrome/navidrome/log"
)

// onlineEmbedCoverTimeout caps how long we wait on a cover image
// HTTP fetch. Most covers are 20-200 KB and return in well under a
// second on a healthy CDN; 8s is the upper bound for the worst case
// we want to tolerate before giving up and proceeding without art.
const onlineEmbedCoverTimeout = 8 * time.Second

// onlineEmbedCoverMaxBytes caps the cover image size we accept
// from upstream. The previous value of 8 MiB was too aggressive:
// 网易云's HD album scans routinely serve 10-15 MiB JPEGs and
// 咪咕's "原始封面" / Apple Music art often tops 20 MiB. The
// downstream path transcodes the image down to
// onlineEmbedCoverEmbedMaxBytes (300 KB) before embedding, so
// the in-memory size only matters for (a) the per-task disk
// write and (b) ffmpeg's stdin on the transcode. 32 MiB is the
// new ceiling: large enough for any source we know about, small
// enough that a malicious or misconfigured upstream can't fill
// the user's disk.
const onlineEmbedCoverMaxBytes = 32 * 1024 * 1024

// onlineEmbedCoverEmbedMaxBytes is the hard cap on what we put
// into the audio file's native cover-art slot. We transcode the
// source cover down to this size before embedding so a single
// oversized source image (common with paid-CD-quality album
// scans served at 3000x3000 / 7 MB) doesn't bloat every
// downloaded file. 300 KB matches the order of magnitude other
// tools (mp3tag, kid3, MusicBee) embed by default and is well
// under the ID3v2 APIC frame size most players load lazily.
const onlineEmbedCoverEmbedMaxBytes = 300 * 1024

// onlineEmbedCoverMaxWidth is the upper bound on the long edge
// of the cover image we actually write into the audio file.
// 800px is the conventional album-art resolution: it's
// indistinguishable from the source on a phone screen, and
// down-scaling from typical 1000-3000px source images cuts
// the file size by an order of magnitude before JPEG's
// quality factor even comes into play.
const onlineEmbedCoverMaxWidth = 800

// onlineEmbedCoverJpegQuality maps to ffmpeg's -q:v scale (1 =
// best, 31 = worst). q=5 is the sweet spot for JPEG album
// art: visually indistinguishable from the source at typical
// viewing sizes while still cutting file size by ~5x
// compared to q=2.
const onlineEmbedCoverJpegQuality = 5

// onlineEmbedLyricTimeout caps how long we'll wait on a source
// script to return lyrics. Sourced from the same 12s budget as the
// resolve script — the script already proved it can run in that
// window, and the lyric endpoint is usually much faster.
const onlineEmbedLyricTimeout = 12 * time.Second

// onlineEmbedArtworkDir holds the per-task cover images we extract
// during download. The directory lives next to the final audio
// file's parent so cleanup is trivial (we can wipe the whole dir
// without affecting the user's other music). Files are written
// with 0600 because they originate from a user upload + untrusted
// upstream image fetch.
func onlineEmbedArtworkDir(downloadDir string) string {
	return filepath.Join(downloadDir, ".nd-embed-artwork")
}

// onlineEmbedArtworkPath returns a per-task cover file path. The
// `taskID` is what we use to avoid clashes when several
// downloads run in parallel against the same download directory.
func onlineEmbedArtworkPath(downloadDir, taskID string, mimeExt string) string {
	return filepath.Join(onlineEmbedArtworkDir(downloadDir), taskID+mimeExt)
}

// onlineEmbedLyricTagFrame is the metadata key ffmpeg routes to
// the lyrics tag frame on every container we target. lxserver-main
// writes the same value via `tagger2.lyrics = lyricText` in
// `fileCache.ts`; we use ffmpeg's `-metadata lyrics=…` flag
// instead because ffmpeg is already a hard dependency and handles
// the per-container frame selection for us:
//
//   - MP3  → ID3v2 USLT (and we force `-id3v2_version 3` so the
//     Windows stock player surfaces it).
//   - FLAC → Vorbis LYRICS field.
//   - OGG  → Vorbis LYRICS field (same as FLAC; same ffmpeg code
//     path in libavformat).
//   - M4A  → iTunes `©lyr` atom.
//
// The user explicitly opted out of a sidecar `.lrc` file — tag
// embed only. lxserver-main's `saveLyricCache` does both; we
// keep just the tag step to match the user's stated preference.
const onlineEmbedLyricTagFrame = "lyrics"

// onlineEmbedResult is what onlineEmbedDownloadMetadata returns to
// the caller. Errors on individual sub-steps are folded into a
// single error so the caller can log them once.
type onlineEmbedResult struct {
	HadCover bool
	HadLyric bool
}

type onlineEmbedFailure struct {
	Reason string
	Cause  error
}

func (e *onlineEmbedFailure) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Reason
	}
	return e.Reason + ": " + e.Cause.Error()
}

func (e *onlineEmbedFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newOnlineEmbedFailure(reason string, cause error) error {
	return &onlineEmbedFailure{Reason: reason, Cause: cause}
}

func onlineEmbedFailureReason(err error) string {
	var embedErr *onlineEmbedFailure
	if errors.As(err, &embedErr) {
		return embedErr.Reason
	}
	return ""
}

func strictOnlineEmbedDownloadedFile(
	ctx context.Context,
	downloadDir string,
	embedID string,
	songInfo map[string]any,
	candidate onlineSource,
	songSource string,
	quality string,
	audioPath string,
) (string, error) {
	if audioPath == "" {
		return audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("audio path is empty"))
	}

	embedMode := onlineEmbedMode()
	if embedMode == embedModeNone {
		return audioPath, nil
	}

	// Strict success criteria: metadata is considered valid only when
	// the three core tags and cover are all available. Any missing
	// prerequisite fails the task early.
	title := onlineEmbedFirstNonEmpty(songInfo, "name", "songName", "title")
	artist := onlineEmbedFirstNonEmpty(songInfo, "singer", "singerName", "artist")
	album := onlineEmbedFirstNonEmpty(songInfo, "albumName", "album", "albumname")
	if strings.TrimSpace(title) == "" || strings.TrimSpace(artist) == "" || strings.TrimSpace(album) == "" {
		return audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("required metadata missing: title/artist/album"))
	}

	coverURL := pickOnlineEmbedCoverURL(songInfo)
	if strings.TrimSpace(coverURL) == "" {
		return audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("cover url missing"))
	}
	coverRef := fetchAndPersistOnlineCover(ctx, downloadDir, embedID, songInfo)
	defer onlineEmbedCleanupArtwork(downloadDir)
	if coverRef == nil {
		return audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("cover fetch failed"))
	}

	lyric := ""
	if embedMode == embedModeAll {
		lyric, _ = fetchOnlineEmbedLyric(ctx, candidate, songSource, songInfo, quality)
		if strings.TrimSpace(lyric) == "" {
			return audioPath, newOnlineEmbedFailure("歌词嵌入失败", fmt.Errorf("lyric fetch failed"))
		}
	}

	_, finalPath, err := onlineEmbedDownloadMetadata(ctx, audioPath, songInfo, quality, coverRef, lyric)
	if err != nil {
		if finalPath == "" {
			finalPath = audioPath
		}
		return finalPath, err
	}
	if finalPath == "" {
		finalPath = audioPath
	}
	return finalPath, nil
}

// onlineEmbedArtworkRef is a small wrapper so the embed step can
// accept a cover that was either downloaded by the caller or read
// from disk. When a download task is retrying after a transient
// failure we already have the cover on disk, so we don't have to
// re-fetch it.
type onlineEmbedArtworkRef struct {
	// Path is a path to a cover file on disk. Empty when the caller
	// did not manage to download a cover, in which case FFmpeg is
	// invoked without the -i cover argument.
	Path string
	// Mime is the cover's MIME type (e.g. "image/jpeg"). The
	// extension chosen in onlineEmbedArtworkPath is derived from
	// this. We use it only for logging here.
	Mime string
}

// pickOnlineEmbedCoverURL returns the best cover URL available in
// the song info payload, or "" if none is set. The order matches
// lxserver-main's extractSongMetadata: songInfo.img first, then
// the legacy songInfo.meta.picUrl fallback that older lx-music
// scripts still emit.
func pickOnlineEmbedCoverURL(songInfo map[string]any) string {
	if v := strings.TrimSpace(stringValue(songInfo["img"])); v != "" {
		return v
	}
	if meta := mapValue(songInfo["meta"]); meta != nil {
		if v := strings.TrimSpace(stringValue(meta["picUrl"])); v != "" {
			return v
		}
	}
	return ""
}

// onlineEmbedCoverExtension maps a content-type / sniffed header
// to a file extension. We default to .jpg because the vast
// majority of upstream cover art is JPEG; downstream ffmpeg is
// happy to re-mux anything we hand it.
func onlineEmbedCoverExtension(contentType string, head []byte) string {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch ct {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	// Sniff a few common magic bytes as a last resort.
	if len(head) >= 4 && bytes.Equal(head[:4], []byte{0x89, 'P', 'N', 'G'}) {
		return ".png"
	}
	if len(head) >= 3 && bytes.Equal(head[:3], []byte{0xFF, 0xD8, 0xFF}) {
		return ".jpg"
	}
	if len(head) >= 6 && (bytes.Equal(head[:6], []byte("GIF87a")) || bytes.Equal(head[:6], []byte("GIF89a"))) {
		return ".gif"
	}
	if len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")) {
		return ".webp"
	}
	return ".jpg"
}

// persistOnlineEmbedCover writes the body to a path under the
// download directory. Returns the path. The caller is expected to
// invoke onlineEmbedCleanupArtwork on the directory at the end of
// the embed step so the cover doesn't pile up.
func persistOnlineEmbedCover(downloadDir, taskID, mime string, body []byte) (string, error) {
	if err := os.MkdirAll(onlineEmbedArtworkDir(downloadDir), 0o755); err != nil {
		return "", err
	}
	ext := onlineEmbedCoverExtension(mime, body)
	path := onlineEmbedArtworkPath(downloadDir, taskID, ext)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// fetchOnlineEmbedLyric asks the source script to fetch the lyric
// text for the given song. The dispatch is the same path that the
// resolve phase uses: we run the same Node sandbox, but with
// `action: 'lyric'` instead of `action: 'musicUrl'`. Scripts that
// don't handle that action are expected to either throw or return
// falsy — both are treated as "no lyric available" rather than an
// error.
//
// `source` is the onlineSource that successfully resolved the
// download (NOT songInfo.source — the two can differ for fallback
// flows where wy's song was resolved by an ikun custom script).
//
// fetchOnlineEmbedLyric is the orchestrator the embed entry
// points call. The lookup order is:
//
//  1. The user-supplied lx-music script FIRST, via Node.
//     Some lx-music scripts ship a built-in lyric provider
//     (proxies to a paid lyric API, decrypts the source's
//     VIP-locked lyrics, etc.) that's more reliable than
//     the public source API. We score the result with the
//     same matcher the Go-client fallback uses, so a wrong
//     song (e.g. a same-title remix) is rejected and we
//     fall through to the multi-source Go fallback.
//
//  2. songInfo.meta.lrcUrl fallback SECOND.
//     This keeps strict server-mode and browser-mode behavior
//     aligned: if the script has no lyric action, we still try
//     the source-provided lrcUrl before moving on. The same
//     matcher is applied, so we only accept it when title/artist/
//     duration evidence is good enough.
//
//  3. The Go client with multi-source fallback LAST.
//     We try wy / kg / kw / tx / mg in order (see
//     onlineLyricFallbackOrder), and accept the first
//     candidate whose [ti:]/[ar:] tags + duration match
//     the song we downloaded. A single source frequently
//     has no lyric for a song that another source
//     happily serves; the cross-source check (see
//     online_lyric_match.go) is what makes this safe — we
//     don't accept a tag-less lyric from a single source
//     without checking the others first.
//
// The matcher is the safety net: a lyric from the "wrong
// song with the same title" can quietly slip in if we
// don't compare against songInfo.name / songInfo.singer /
// songInfo.interval before embedding. The 70/70/3s
// thresholds (title-similarity, artist-similarity,
// duration tolerance) were picked to be permissive enough
// to absorb parenthetical-release differences and strict
// enough to reject remixes / live versions.
func fetchOnlineEmbedLyric(ctx context.Context, source onlineSource, songSource string, songInfo map[string]any, quality string) (string, error) {
	// Top-of-orchestrator trace. The orchestrator is the
	// most likely place for a silent failure (any error path
	// returns ("", nil)), so we log the dispatch key here
	// before any work. The user can grep this to confirm the
	// lyric fetch was attempted at all.
	embedTrace(ctx, "lyric:fetch-attempt", "songSource", songSource, "candidate", source.ID, "quality", quality, "hasName", stringValue(songInfo["name"]) != "", "hasSongmid", stringValue(songInfo["songmid"]) != "")

	// 1) User-supplied lx-music script. The script is
	// invoked with action='lyric' and may return a lyric
	// string. We then run the matcher: scripts are most
	// often the right answer (they know about VIP variants
	// the public APIs don't), but a same-title
	// mis-identification is still possible. If the
	// matcher's score is OK we take the result and
	// skip the rest of the fallback run.
	if out, ok := fetchOnlineEmbedLyricViaScriptOk(ctx, source, songSource, songInfo, quality); ok {
		if accepted, ok := onlineEmbedAcceptLyricCandidate(ctx, "script:"+source.ID, out, songInfo); ok {
			return accepted, nil
		}
	}

	// 2) songInfo.meta.lrcUrl fallback. This path is now shared by
	// all embed flows (server/browser/playlist sync), but still goes
	// through the same matcher before we accept it.
	if out := lookupOnlineBrowserLyricFromSongInfo(ctx, songInfo); strings.TrimSpace(out) != "" {
		if accepted, ok := onlineEmbedAcceptLyricCandidate(ctx, "meta.lrcUrl", out, songInfo); ok {
			return accepted, nil
		}
	} else {
		embedTrace(ctx, "lyric:meta-lrc-empty", "songName", stringValue(songInfo["name"]))
	}

	// 3) Multi-source Go client fallback. Iterates
	// onlineLyricFallbackOrder and accepts the first
	// candidate the matcher approves. The trace line at
	// the end of the run summarizes every attempt so
	// the user can see exactly which sources fired,
	// which produced a lyric, and which (if any) the
	// matcher accepted.
	lyric, attempts := onlineLyricFallback(ctx, songInfo)
	accepted := ""
	for _, a := range attempts {
		if a.Accepted {
			accepted = a.Source
		}
	}
	embedTrace(ctx, "lyric:fallback-result",
		"acceptedSource", accepted,
		"finalLen", len(lyric),
		"attemptCount", len(attempts),
		"attempts", onlineLyricMatchAttemptsTraceValue(attempts),
	)
	if lyric != "" {
		return lyric, nil
	}
	// Trace the empty-result case so the user can tell
	// whether the per-source fetchers ran but returned
	// nothing (the [EMBED] lyric:dispatch lines above
	// will have fired with each source key) or whether
	// the dispatcher never matched (in which case the
	// dispatch trace will show source=… and
	// supported=false). Either way the embed step
	// falls through to ffmpeg without a lyrics frame.
	embedTrace(ctx, "lyric:all-paths-empty", "songName", stringValue(songInfo["name"]), "songmid", stringValue(songInfo["songmid"]))
	return "", nil
}

func onlineEmbedAcceptLyricCandidate(ctx context.Context, source string, lyric string, songInfo map[string]any) (string, bool) {
	text := strings.TrimSpace(lyric)
	if text == "" {
		return "", false
	}
	cand := onlineLyricCandidate{Source: source, Lyric: text}
	cand.SelfTitle, cand.SelfArtist, cand.SelfAlbum = onlineLyricParseIDTags(text)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(text)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		embedTrace(ctx, "lyric:candidate-rejected", "source", source, "lyricLen", len(text), "reason", score.Reason, "titleScore", score.TitleScore, "artistScore", score.ArtistScore, "durationDelta", score.DurationDelta)
		return "", false
	}
	embedTrace(ctx, "lyric:candidate-accepted", "source", source, "lyricLen", len(text), "titleScore", score.TitleScore, "artistScore", score.ArtistScore, "durationDelta", score.DurationDelta)
	return text, true
}

// onlineLyricMatchAttemptsTraceValue formats the per-attempt
// summary as a compact key=value list suitable for the
// embedTrace helper. The helper is a separate function (not
// inlined) so we can keep the call site readable and add
// per-attempt formatting tweaks in one place.
//
// Format: "src:tScore/aScore/dDelta=reason" — the three
// numeric scores come FIRST so the user can grep for
// borderline cases ("t=50/a=100/d=0" = artist nailed it but
// the title has a version suffix). Without the numbers, a
// single "title tag did not match" reason was opaque —
// the user couldn't tell whether the lyric was the wrong
// song (rejection correct) or a same-song different
// source-tagger that just labeled it slightly differently
// (rejection wrong).
func onlineLyricMatchAttemptsTraceValue(attempts []onlineLyricMatchAttempt) string {
	if len(attempts) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(attempts))
	for _, a := range attempts {
		ts := "na"
		if a.TitleScore >= 0 {
			ts = strconv.Itoa(a.TitleScore)
		}
		as_ := "na"
		if a.ArtistScore >= 0 {
			as_ = strconv.Itoa(a.ArtistScore)
		}
		dd := "na"
		if a.DurationDelta >= 0 {
			dd = strconv.Itoa(a.DurationDelta)
		}
		parts = append(parts, a.Source+":t="+ts+"/a="+as_+"/d="+dd+"="+a.Reason)
	}
	return strings.Join(parts, ",")
}

// fetchOnlineEmbedLyricViaScriptOk is a thin wrapper over
// fetchOnlineEmbedLyricViaScript that returns (string, bool)
// so the orchestrator can tell whether the script actually
// produced a lyric. The bool is true when the script's
// success=true AND the lyric field is non-empty; false in
// every other case (success: false, no-node, no-script, node
// crashed, etc.). The wrapper exists so the orchestrator
// can fall back to the Go client without a separate
// "did we get a lyric" probe at the call site.
func fetchOnlineEmbedLyricViaScriptOk(ctx context.Context, source onlineSource, songSource string, songInfo map[string]any, quality string) (string, bool) {
	out, err := fetchOnlineEmbedLyricViaScript(ctx, source, songSource, songInfo, quality)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(out) == "" {
		return "", false
	}
	return out, true
}

func fetchOnlineEmbedLyricViaScript(ctx context.Context, source onlineSource, songSource string, songInfo map[string]any, quality string) (string, error) {
	if !commandExists("node") {
		embedTrace(ctx, "lyric:no-node", "source", source.ID)
		return "", nil
	}
	scriptPath := filepath.Join(onlineScriptsDir(), source.ID)
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		embedTrace(ctx, "lyric:no-script", "source", source.ID, "path", scriptPath, "err", err.Error())
		return "", nil
	}
	embedTrace(ctx, "lyric:dispatching", "source", source.ID, "songSource", songSource, "quality", quality)

	payload, err := json.Marshal(map[string]any{
		"script":      string(scriptContent),
		"allowUnsafe": source.AllowUnsafeVM,
		"source":      songSource,
		"musicInfo":   songInfo,
		"quality":     quality,
		"action":      "lyric",
	})
	if err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(ctx, onlineEmbedLyricTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "node", "-e", nodeOnlineDownloadScript)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(), fmt.Sprintf("ND_TIMEOUT_MS=%d", onlineEmbedLyricTimeout.Milliseconds()))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Lyric is best-effort; don't propagate script errors.
		embedTrace(ctx, "lyric:node-failed", "source", source.ID, "err", err.Error(), "stderr", strings.TrimSpace(stderr.String()))
		return "", nil
	}
	embedTrace(ctx, "lyric:node-ok", "source", source.ID, "stdoutLen", stdout.Len())

	// The script writes { success, error? } on success or { success:
	// false, error } on failure. We look for the lyrics field at the
	// top level. To avoid coupling to a specific shape, the script
	// returns lyrics as either { lyric, lrc } or a raw string — we
	// take whichever the script emitted.
	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Lyric   string `json:"lyric"`
		LRC     string `json:"lrc"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		embedTrace(ctx, "lyric:bad-json", "source", source.ID, "err", err.Error())
		return "", nil
	}
	if !resp.Success {
		// Distinguish two failure modes that previously
		// collapsed into a single trace line:
		//
		//   1. The script doesn't know how to fetch lyrics
		//      for this source (e.g. lx-music script author
		//      never wrote a `lyric:` action). The error
		//      string in that case is usually
		//      "lyric_unsupported:" (the sentinel lx-music
		//      scripts throw).
		//   2. The script supports lyrics but the fetch
		//      failed (network / VIP / parse error).
		//
		// The user has been asking why lyrics never appear
		// in the embedded tags; the answer often turns out
		// to be case (1) — the script is not at fault and
		// the issue is "no source has a lyric action for
		// this song". Tagging the failure mode in the
		// trace makes that visible at a glance.
		errTag := "unknown"
		errLower := strings.ToLower(strings.TrimSpace(resp.Error))
		switch {
		case errLower == "":
			errTag = "no-error-message"
		case strings.Contains(errLower, "lyric_unsupported"),
			strings.Contains(errLower, "unsupported"),
			strings.Contains(errLower, "not_supported"),
			strings.Contains(errLower, "not supported"):
			errTag = "script-has-no-lyric-action"
		case strings.Contains(errLower, "no lyric"),
			strings.Contains(errLower, "not found"),
			strings.Contains(errLower, "404"):
			errTag = "lyric-not-found"
		}
		embedTrace(ctx, "lyric:script-said-fail", "source", source.ID, "errTag", errTag, "err", resp.Error)
		return "", nil
	}
	out := strings.TrimSpace(resp.Lyric)
	if out == "" {
		out = strings.TrimSpace(resp.LRC)
	}
	embedTrace(ctx, "lyric:done", "source", source.ID, "lyricLen", len(out))
	return out, nil
}

// onlineEmbedSniffAudioFormat returns the ffmpeg -f muxer name and
// a short extension for the audio file at audioPath. We peek the
// first few bytes and match against the magic-byte sequences ffmpeg
// uses internally:
//
//	"ID3" header           -> MP3 with ID3v2 (most common)
//	0xFFFx + layer=00      -> AAC ADTS (raw AAC stream)
//	0xFF 0xFB/... frame    -> MP3 without ID3v2 (frame sync)
//	"fLaC"                 -> FLAC
//	"OggS"                 -> OGG container (Vorbis / Opus)
//	"RIFF" + "WAVE"        -> WAV
//	ftyp atom              -> MP4 / M4A (we accept any ftyp-based container)
//
// Returns ("", "") when the file can't be classified -- that signals
// the embed pipeline to skip silently rather than corrupt the
// file with a guessed format.
func onlineEmbedSniffAudioFormat(audioPath string) (format string, ext string) {
	f, err := os.Open(audioPath)
	if err != nil {
		return onlineEmbedFormatFromExt(audioPath)
	}
	defer f.Close()

	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if n < 4 {
		return onlineEmbedFormatFromExt(audioPath)
	}

	switch {
	case bytes.HasPrefix(head, []byte("ID3")):
		return "mp3", "mp3"
	case n >= 2 && head[0] == 0xFF && (head[1]&0xF0) == 0xF0 && (head[1]&0x06) == 0x00:
		// ADTS AAC frame sync. This was previously classified as MP3
		// because both formats start with 0xFFF sync bits. Treating ADTS
		// as MP3 makes ffmpeg fail early (`-f mp3 -i ...`) and skips the
		// embed pipeline entirely.
		return "aac", "m4a"
	case n >= 2 && head[0] == 0xFF && (head[1]&0xE0) == 0xE0:
		// 0xFF followed by 0xE? — frame sync marker for MP3.
		return "mp3", "mp3"
	case bytes.HasPrefix(head, []byte("fLaC")):
		return "flac", "flac"
	case bytes.HasPrefix(head, []byte("OggS")):
		return "ogg", "ogg"
	case n >= 12 && bytes.Equal(head[8:12], []byte("WAVE")):
		return "wav", "wav"
	case n >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		// MP4 / M4A / M4B all share the same ftyp atom; the
		// actual codec lives in head[8:12] (e.g. "M4A "). We
		// use the generic mp4 muxer; ffmpeg writes iTunes
		// metadata atoms (©nam / ©ART / ©alb / ©lyr) on its
		// own.
		return "mp4", "m4a"
	}

	// Some upstream files include non-audio bytes before sync headers,
	// which can make short magic-byte sniffing inconclusive. Fall back to
	// extension so common cases (e.g. .mp3) still go through embedding.
	return onlineEmbedFormatFromExt(audioPath)
}

func onlineEmbedFormatFromExt(audioPath string) (format string, ext string) {
	fileExt := strings.ToLower(strings.TrimSpace(filepath.Ext(audioPath)))
	switch fileExt {
	case ".mp3":
		return "mp3", "mp3"
	case ".flac":
		return "flac", "flac"
	case ".ogg", ".opus":
		return "ogg", "ogg"
	case ".wav":
		return "wav", "wav"
	case ".m4a", ".mp4", ".m4b":
		return "mp4", "m4a"
	case ".aac":
		return "aac", "m4a"
	default:
		return "", ""
	}
}

// onlineEmbedSongInfoHasAnyTag reports whether the songInfo map
// carries at least one of the baseline metadata fields the
// ffmpeg -metadata flags consume (title / artist / album, plus
// the meta.songName / meta.singerName / meta.albumName fallbacks
// the ffmpeg invocation uses). The "all" embed mode promise is
// to embed metadata, so when this returns true we always run
// ffmpeg even if cover and lyric both failed upstream — that
// way the user always sees their title/artist/album in the
// resulting file regardless of network conditions.
//
// Returns false for an empty / unknown songInfo, which is the
// only legitimate "skip" case.
func onlineEmbedSongInfoHasAnyTag(songInfo map[string]any) bool {
	if len(songInfo) == 0 {
		return false
	}
	for _, k := range []string{"name", "singer", "albumName", "songName", "singerName", "album", "albumname", "albumArtist"} {
		if strings.TrimSpace(stringValue(songInfo[k])) != "" {
			return true
		}
	}
	if meta := mapValue(songInfo["meta"]); meta != nil {
		for _, k := range []string{"songName", "singerName", "albumName", "album", "picUrl", "lrcUrl"} {
			if strings.TrimSpace(stringValue(meta[k])) != "" {
				return true
			}
		}
	}
	return false
}

// onlineEmbedDownloadMetadata runs the FFmpeg embed step on an
// already-downloaded audio file. Returns (result, nil) on partial
// success — when the file already plays, the embed step is purely
// an enhancement, so we never fail the download because the cover
// couldn't be fetched or ffmpeg ran out of memory.
//
// All errors here are best-effort and logged; callers should treat
// (nil, err) as "metadata embed did not run" rather than "download
// failed".
//
// The second return value is the (possibly renamed) audio path
// after the embed step finishes. For mp4/m4a content the file
// may have been renamed from `.aac` to `.m4a` to fix the
// extension mismatch (see onlineEmbedNormalizeM4AExtension);
// callers MUST update their task's FilePath field to the
// returned value, otherwise the task record will point to a
// non-existent file and the next access will fail.
func onlineEmbedDownloadMetadata(
	ctx context.Context,
	audioPath string,
	songInfo map[string]any,
	quality string,
	cover *onlineEmbedArtworkRef,
	lyric string,
) (*onlineEmbedResult, string, error) {
	result := &onlineEmbedResult{}
	if cover != nil && cover.Path != "" {
		result.HadCover = true
	}
	if strings.TrimSpace(lyric) != "" {
		result.HadLyric = true
	}

	// Trace breadcrumb #1: report what the caller actually handed
	// us. If this line never appears in navidrome.log, the caller
	// short-circuited earlier (most commonly because
	// onlineEmbedEnabled() returned false on a legacy settings
	// file). If the line shows cover=nil, the cover fetch failed
	// upstream; if it shows lyric="", the lyric dispatch didn't
	// yield a string.
	embedTrace(ctx, "download_metadata:enter", "audio", audioPath, "coverPath", coverPathOrEmpty(cover), "lyricLen", len(lyric))

	// Compute whether the songInfo map has any baseline metadata
	// worth writing. The "all" embed mode promise is to embed
	// metadata, so even when cover/lyric both failed (the common
	// case in restricted networks), the user's title/artist/album
	// must still land in the file. The skip-empty branch below
	// now only fires when cover, lyric, AND basic tags are all
	// unavailable — i.e. songInfo was empty, which is the only
	// case where there is genuinely nothing to write.
	hasBasicTags := onlineEmbedSongInfoHasAnyTag(songInfo)

	// If there's nothing to write, skip the ffmpeg invocation.
	// Pre-fix this branch also covered the "no cover and no lyric"
	// case, which silently dropped the title/artist/album embed
	// the user explicitly asked for. The trace now reports
	// `coverOk=false lyricOk=false hasBasicTags=true` so the user
	// can confirm the skip was intentional (no songInfo to write).
	if !result.HadCover && !result.HadLyric && !hasBasicTags {
		embedTrace(ctx, "download_metadata:skip-empty", "audio", audioPath, "reason", "no cover, no lyric, no basic tags")
		return result, audioPath, nil
	}

	ffmpegImpl := ffmpeg.New()
	cmdPath, err := ffmpegImpl.CmdPath()
	if err != nil {
		embedTrace(ctx, "download_metadata:ffmpeg-missing", "audio", audioPath, "err", err.Error())
		return result, audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("ffmpeg missing: %w", err))
	}
	embedTrace(ctx, "download_metadata:ffmpeg-found", "audio", audioPath, "cmd", cmdPath)

	// Sniff the audio container by magic bytes. fetchOnlineDownloadToTempFile
	// writes to os.CreateTemp without an extension, so ffmpeg's
	// format probing by extension would fail. We detect mp3 / flac /
	// mp4 / ogg / wav and pass the explicit -f to ffmpeg so the
	// input is recognized regardless of the on-disk filename.
	format, formatName := onlineEmbedSniffAudioFormat(audioPath)
	if format == "" {
		embedTrace(ctx, "download_metadata:format-unknown", "audio", audioPath, "hadCover", result.HadCover, "hadLyric", result.HadLyric)
		return result, audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("unknown audio format"))
	}
	embedTrace(ctx, "download_metadata:starting-ffmpeg", "audio", audioPath, "format", format, "hadCover", result.HadCover, "hadLyric", result.HadLyric)

	// We always write to a sibling .tmp file (with the right
	// extension so ffmpeg's output muxer picks the right container
	// automatically), then atomically rename over the original.
	// ffmpeg's `-c copy` is fast but it can still trip on a
	// malformed file or an unwritable destination, and we don't
	// want to leave the user with a half-written audio file in
	// that case.
	tmpPath := audioPath + ".ndembed." + formatName
	defer os.Remove(tmpPath)

	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-f", format,
		"-i", audioPath,
	}
	inputs := []string{audioPath}
	if result.HadCover {
		args = append(args, "-i", cover.Path)
		inputs = append(inputs, cover.Path)
	}
	args = append(args, "-map", "0:a")
	if result.HadCover {
		// Map the cover as video stream so it lands in the same
		// container. `-disposition:v:0 attached_pic` is what
		// marks the stream as cover art vs a regular video stream
		// (mp4 / mp3 id3 APIC both honor this).
		args = append(args, "-map", "1:v", "-c:v", "copy", "-disposition:v:0", "attached_pic")
	}
	args = append(args, "-c:a", "copy")
	// Copy input metadata first, then apply explicit metadata flags
	// below so our title/artist/album/comment overrides win.
	args = append(args, "-map_metadata", "0")

	// Tag metadata. ffmpeg understands -metadata for most common
	// tags; for MP3 it routes to ID3v2 and for FLAC it routes to
	// Vorbis Comments automatically.
	onlineEmbedAppendTagMetadataArgs(&args, songInfo, quality)

	if result.HadLyric && format != "mp3" {
		// For FLAC and M4A, ffmpeg writes the lyrics into
		// the standard container-native field:
		//
		//   - FLAC → Vorbis `lyrics` (lowercase) — the
		//     canonical key per the Xiph spec is `LYRICS`,
		//     and we re-canonicalize in the post-ffmpeg Go
		//     pass (see onlineEmbedWriteFLACLyric) because
		//     ffmpeg always writes lowercase.
		//   - M4A  → iTunes `©lyr` atom — no Go-side fix
		//     needed; the atom is correct on first write.
		//
		// For MP3 we deliberately skip this flag. ffmpeg
		// 8.x (and earlier, going back to 4.x) has a
		// long-standing behavior of writing
		// `-metadata lyrics=...` as a generic TXXX
		// (User-defined text) frame with the description
		// "USLT" rather than as a real USLT frame, even
		// with `-id3v2_version 3` or 4. mp3tag on Windows
		// does not auto-promote TXXX(USLT) to the standard
		// "Lyrics" column, so the user sees nothing. The
		// fix is to write a real USLT frame ourselves in a
		// post-ffmpeg Go pass (see onlineEmbedWriteID3USLT
		// below). Same story for the key name — ffmpeg
		// normalizes both `lyrics` and `LYRICS` to the
		// same TXXX wrapper, so changing the case is not a
		// fix on its own.
		args = append(args, "-metadata", onlineEmbedLyricTagFrame+"="+lyric)
	}
	if format == "mp3" {
		// Force ID3v2.3 on the rewrite so older MP3
		// players (incl. the Windows stock player) still
		// see the standard tag frames. The lyric itself is
		// written by our Go pass, not by ffmpeg.
		args = append(args, "-id3v2_version", "3")
	}
	if format == "aac" {
		// Raw ADTS AAC cannot carry rich tags/cover consistently.
		// Remux to MP4/M4A so title/artist/album/cover/lyrics persist.
		args = append(args, "-f", "mp4")
	}

	args = append(args, tmpPath)

	// Quiet any other metadata (e.g. encoder) that ffmpeg would
	// otherwise add. We deliberately leave the audio stream alone
	// (no -vn, no -c:a re-encode) so the user's original bitstream
	// is preserved bit-for-bit.

	_ = inputs // kept for future stream-mapping improvements

	_ = formatName // used above when constructing tmpPath

	runFFmpeg := func(ffArgs []string) (string, error) {
		cmd := exec.CommandContext(ctx, cmdPath, ffArgs...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		return strings.TrimSpace(stderr.String()), err
	}

	stderrText, err := runFFmpeg(args)
	if err != nil {
		embedTrace(ctx, "download_metadata:ffmpeg-failed", "audio", audioPath, "err", err.Error(), "stderr", stderrText)

		// Some files are saved with a misleading extension (e.g. .mp3
		// carrying AAC/MP4 content). If forced input format fails, retry
		// once with ffmpeg auto-detection by removing the input -f flag.
		retryArgs := make([]string, 0, len(args))
		removedInputFormat := false
		for i := 0; i < len(args); i++ {
			if !removedInputFormat && i+1 < len(args) && args[i] == "-f" && args[i+1] == format {
				removedInputFormat = true
				i++
				continue
			}
			retryArgs = append(retryArgs, args[i])
		}

		if removedInputFormat {
			embedTrace(ctx, "download_metadata:ffmpeg-retry-autodetect", "audio", audioPath, "format", format)
			retryStderr, retryErr := runFFmpeg(retryArgs)
			if retryErr != nil {
				embedTrace(ctx, "download_metadata:ffmpeg-retry-autodetect-failed", "audio", audioPath, "err", retryErr.Error(), "stderr", retryStderr)
				return result, audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("ffmpeg failed: %w", retryErr))
			}
			embedTrace(ctx, "download_metadata:ffmpeg-retry-autodetect-ok", "audio", audioPath)
		} else {
			return result, audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("ffmpeg failed: %w", err))
		}
	}
	embedTrace(ctx, "download_metadata:ffmpeg-ok", "audio", audioPath)
	if err := os.Rename(tmpPath, audioPath); err != nil {
		embedTrace(ctx, "download_metadata:rename-failed", "audio", audioPath, "err", err.Error())
		return result, audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("rename failed: %w", err))
	}
	// Normalize the file extension for mp4/m4a content. The
	// upstream script may have named the file with a .aac
	// extension (matching the URL's path or the source's
	// reported format), but the actual file content is the
	// MP4 container with iTunes atoms. Players that
	// associate ".aac" with raw AAC (most notably mp3tag on
	// Windows) will refuse to read the iTunes atoms in that
	// case and surface an empty tag list, even though the
	// file is perfectly tagged. Renaming to .m4a makes the
	// association unambiguous and matches what most
	// tools (mp3tag, MusicBee, kid3, foobar2000) use to
	// dispatch to the iTunes atom parser.
	if format == "mp4" || format == "aac" {
		newPath, renErr := onlineEmbedNormalizeM4AExtension(audioPath)
		if renErr != nil {
			embedTrace(ctx, "download_metadata:ext-rename-failed", "audio", audioPath, "err", renErr.Error())
			return result, audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("extension rename failed: %w", renErr))
		} else if newPath != "" {
			embedTrace(ctx, "download_metadata:ext-renamed", "from", audioPath, "to", newPath)
			audioPath = newPath
		}
	}
	// Post-ffmpeg Go-side tag pass. ffmpeg can't write
	// the lyrics in a way that mp3tag / MusicBee /
	// foobar2000 surfaces as the standard "Lyrics" field
	// (see the long comment above the ffmpeg `-metadata
	// lyrics=` block for the gory details), so for MP3
	// we open the freshly-written file and inject a real
	// USLT frame; for FLAC we re-emit the Vorbis comment
	// block with the canonical `LYRICS=` key. M4A is
	// already correct (ffmpeg writes the iTunes `©lyr`
	// atom, which the same players all read).
	if result.HadLyric {
		if err := onlineEmbedWriteLyricContainer(ctx, format, audioPath, lyric); err != nil {
			embedTrace(ctx, "download_metadata:lyric-rewrite-failed", "format", format, "audio", audioPath, "err", err.Error())
			return result, audioPath, newOnlineEmbedFailure("歌词嵌入失败", err)
		} else {
			embedTrace(ctx, "download_metadata:lyric-rewrite-ok", "format", format, "audio", audioPath, "lyricLen", len(lyric))
		}
	}
	embedTrace(ctx, "download_metadata:done", "audio", audioPath)
	return result, audioPath, nil
}

// embedTrace is a single funnel for embed-pipeline breadcrumbs so
// the user can tail navidrome.log and see exactly which step ran /
// short-circuited. Every entry is prefixed with [EMBED] so a
// single grep isolates the trace from other log lines. The format
// intentionally matches what navidrome.log already prints (key
// =value pairs) so it slots into whatever log shipper the user
// already has. The tag "step" is always present; the rest is
// caller-supplied. We log at Info even on skip / failure paths
// because the embed pipeline is fire-and-forget — the user is
// most likely to look here precisely when something is silently
// not happening.
//
// navidrome's log package treats the first argument as the
// message and every subsequent pair of args as a key / value
// field. An odd number of trailing args produces a "!!!!Invalid
// number of arguments!!!!" placeholder. We have to interleave
// the step label as a properly-shaped (key, value) pair so the
// parser can find the matching value; doing it in any other
// order means the step shows up as the value of a preceding
// key, which is what the user reported on their first trace
// run.
func embedTrace(_ context.Context, step string, kv ...any) {
	// Pad kv to an even length by appending a marker so the
	// parser never trips on an odd argument count. This is a
	// belt-and-braces measure — every caller in this file
	// already passes key/value pairs.
	if len(kv)%2 != 0 {
		kv = append(kv, "<odd-args>")
	}
	args := make([]any, 0, len(kv)+4)
	// message slot: the [EMBED] prefix is the visual hook the
	// user greps for; step=… in the structured fields carries
	// the canonical name for downstream log search.
	args = append(args, "[EMBED] "+step, "step", step)
	args = append(args, kv...)
	log.Info(args...)
}

// coverPathOrEmpty returns the cover's disk path or "" when the
// caller handed us no cover at all. Used by embedTrace so the log
// line shows "<empty>" instead of "<nil>" (which is the way
// fmt.Sprintf renders a typed-nil interface).
func coverPathOrEmpty(cover *onlineEmbedArtworkRef) string {
	if cover == nil {
		return "<empty>"
	}
	return cover.Path
}

// onlineEmbedCleanupArtwork removes the per-task cover directory
// for a download directory. Safe to call from any goroutine.
func onlineEmbedCleanupArtwork(downloadDir string) {
	if downloadDir == "" {
		return
	}
	_ = os.RemoveAll(onlineEmbedArtworkDir(downloadDir))
}

// onlineEmbedNormalizeM4AExtension renames a file from `.aac` to
// `.m4a` if its current extension is `.aac`. Several sources —
// kw most notably — return URLs ending in `.aac` even when the
// actual content is the MP4 container with iTunes atoms.
// Players that associate `.aac` with raw AAC (mp3tag on Windows
// is the canonical case) then refuse to read the iTunes atoms
// and show an empty tag list, despite the file being perfectly
// tagged.
//
// Renaming to `.m4a` makes the association unambiguous. The
// file content is unchanged (extension-only rename, no
// re-mux).
//
// The call site only invokes this for `format == "mp4"`, so by
// construction we only touch m4a-in-disguise files.
func onlineEmbedNormalizeM4AExtension(audioPath string) (string, error) {
	dir := filepath.Dir(audioPath)
	base := filepath.Base(audioPath)
	lower := strings.ToLower(base)
	targetExt := ".m4a"
	for _, badExt := range []string{".aac"} {
		suffix := strings.ToLower(badExt)
		if !strings.HasSuffix(lower, suffix) {
			continue
		}
		newBase := base[:len(base)-len(suffix)] + targetExt
		newPath := filepath.Join(dir, newBase)
		if _, err := os.Stat(newPath); err == nil {
			return "", fmt.Errorf("target %s already exists, leaving as-is", newPath)
		}
		if err := os.Rename(audioPath, newPath); err != nil {
			return "", err
		}
		return newPath, nil
	}
	return "", nil
}

// onlineEmbedMode consults the persisted online source settings
// and returns the embed mode the user has chosen ("none" /
// "metadata" / "all"). Falls back to the fresh-install default
// (embedModeMetadata) when settings can't be read so the user's
// first download after a clean install still gets embedded
// cover + tags.
//
// One startup banner is logged on the first call so the user
// running an old binary can tell at a glance that the trace
// code is missing. The banner appears only once per process; a
// settings file upgrade (legacy embedMetadata boolean ->
// 3-state EmbedMode string) is recorded in
// loadOnlineSourceSettings separately.
//
// Callers should branch on the returned string rather than
// recomputing it: every call round-trips through the
// settings.json read path, and the broker is sensitive to
// rapid repeated I/O on the same file.
var onlineEmbedBannerOnce sync.Once

// onlineEmbedAppendTagMetadataArgs reads the title/artist/
// album from songInfo (with the meta.* fallbacks) and
// appends the corresponding -metadata flags to args. Also
// appends a comment=Quality: <q> flag when quality is
// non-empty. This is the one chokepoint that decides what
// goes into the ffmpeg -metadata block, so the embed
// function can keep its cyclomatic complexity manageable.
//
// The function intentionally doesn't add the lyrics flag
// — that decision lives in onlineEmbedDownloadMetadata
// because the MP3 path skips ffmpeg entirely (the lyrics
// are written by onlineEmbedWriteID3USLT in a post-ffmpeg
// pass to avoid ffmpeg's TXXX(USLT) wrapper bug).
func onlineEmbedAppendTagMetadataArgs(args *[]string, songInfo map[string]any, quality string) {
	title := onlineEmbedFirstNonEmpty(songInfo, "name", "songName", "title")
	artist := onlineEmbedFirstNonEmpty(songInfo, "singer", "singerName", "artist")
	album := onlineEmbedFirstNonEmpty(songInfo, "albumName", "album", "albumname")
	albumArtist := onlineEmbedFirstNonEmpty(songInfo, "albumArtist", "album_artist", "albumartist")
	composer := onlineEmbedFirstNonEmpty(songInfo, "composer", "composerName")
	genre := onlineEmbedFirstNonEmpty(songInfo, "genre", "style")
	track := onlineEmbedNormalizeTrackOrDiscTag(onlineEmbedFirstNonEmpty(songInfo, "track", "trackNo", "trackNumber", "trackNum", "songNo", "no"))
	disc := onlineEmbedNormalizeTrackOrDiscTag(onlineEmbedFirstNonEmpty(songInfo, "disc", "discNo", "discNumber", "cdSerial", "discnum", "cdNum"))
	date := onlineEmbedNormalizeDateTag(onlineEmbedFirstNonEmpty(songInfo, "date", "publishDate", "publishTime", "pubTime", "pub_time", "releaseDate", "time_public", "year"))
	bpm := onlineEmbedNormalizeBPMTag(onlineEmbedFirstNonEmpty(songInfo, "bpm"))
	language := onlineEmbedFirstNonEmpty(songInfo, "language", "lang")
	isrc := onlineEmbedFirstNonEmpty(songInfo, "isrc")
	copyright := onlineEmbedFirstNonEmpty(songInfo, "copyright", "copyrightText", "cpName")
	comment := onlineEmbedFirstNonEmpty(songInfo, "comment", "description", "desc", "intro")

	if title != "" {
		*args = append(*args, "-metadata", "title="+title)
	}
	if artist != "" {
		*args = append(*args, "-metadata", "artist="+artist)
	}
	if album != "" {
		*args = append(*args, "-metadata", "album="+album)
	}
	if albumArtist != "" {
		*args = append(*args, "-metadata", "album_artist="+albumArtist)
	}
	if composer != "" {
		*args = append(*args, "-metadata", "composer="+composer)
	}
	if genre != "" {
		*args = append(*args, "-metadata", "genre="+genre)
	}
	if track != "" {
		*args = append(*args, "-metadata", "track="+track)
	}
	if disc != "" {
		*args = append(*args, "-metadata", "disc="+disc)
	}
	if date != "" {
		*args = append(*args, "-metadata", "date="+date)
	}
	if bpm != "" {
		*args = append(*args, "-metadata", "bpm="+bpm)
	}
	if language != "" {
		*args = append(*args, "-metadata", "language="+language)
	}
	if isrc != "" {
		*args = append(*args, "-metadata", "isrc="+isrc)
	}
	if copyright != "" {
		*args = append(*args, "-metadata", "copyright="+copyright)
	}

	// Keep upstream comment when present; append quality marker so the
	// UI-visible note does not overwrite source-provided metadata.
	if quality != "" {
		q := "Quality: " + quality
		if comment == "" {
			comment = q
		} else if !strings.Contains(comment, q) {
			comment = comment + " | " + q
		}
	}
	if comment != "" {
		*args = append(*args, "-metadata", "comment="+comment)
	}
}

func onlineEmbedFirstNonEmpty(songInfo map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(stringValue(songInfo[key])); v != "" {
			return v
		}
	}
	if meta := mapValue(songInfo["meta"]); meta != nil {
		for _, key := range keys {
			if v := strings.TrimSpace(stringValue(meta[key])); v != "" {
				return v
			}
		}
	}
	return ""
}

func onlineEmbedNormalizeDateTag(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	const minYear = 1900
	maxYear := time.Now().Year() + 1
	// Common case: YYYY-MM-DD / YYYY/MM/DD / YYYY.MM.DD.
	if len(v) >= 4 {
		prefix := v[:4]
		isYear := true
		for i := 0; i < 4; i++ {
			if prefix[i] < '0' || prefix[i] > '9' {
				isYear = false
				break
			}
		}
		if isYear {
			y, _ := strconv.Atoi(prefix)
			if y >= minYear && y <= maxYear {
				return prefix
			}
			return ""
		}
	}
	// Fallback: first 4 consecutive digits anywhere in the string.
	for i := 0; i+4 <= len(v); i++ {
		ok := true
		for j := 0; j < 4; j++ {
			c := v[i+j]
			if c < '0' || c > '9' {
				ok = false
				break
			}
		}
		if ok {
			yearText := v[i : i+4]
			y, _ := strconv.Atoi(yearText)
			if y >= minYear && y <= maxYear {
				return yearText
			}
			return ""
		}
	}
	return ""
}

func onlineEmbedNormalizeTrackOrDiscTag(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	parts := strings.Split(v, "/")
	if len(parts) == 0 || len(parts) > 2 {
		return ""
	}
	normalizePart := func(part string) (string, bool) {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", false
		}
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return "", false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 || n > 999 {
			return "", false
		}
		return strconv.Itoa(n), true
	}
	left, ok := normalizePart(parts[0])
	if !ok {
		return ""
	}
	if len(parts) == 1 {
		return left
	}
	right, ok := normalizePart(parts[1])
	if !ok {
		return left
	}
	return left + "/" + right
}

func onlineEmbedNormalizeBPMTag(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return ""
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 20 || n > 300 {
		return ""
	}
	return strconv.Itoa(n)
}

func onlineEmbedMode() string {
	settings, err := loadOnlineSourceSettings()
	if err != nil {
		embedTrace(context.Background(), "settings:load-failed-defaulting", "default", defaultOnlineEmbedMode, "err", err.Error())
		return defaultOnlineEmbedMode
	}
	mode := sanitizeEmbedMode(settings.EmbedMode)
	onlineEmbedBannerOnce.Do(func() {
		log.Info("[EMBED] pipeline v2 loaded — every download will emit [EMBED] breadcrumbs")
	})
	embedTrace(context.Background(), "settings:loaded", "embedMode", mode)
	return mode
}

// onlineEmbedEnabled remains as a thin wrapper so existing
// call sites that only need a yes/no answer (legacy tests, the
// download queue's "should we even call the embedder?" check)
// keep compiling. The wrapper intentionally collapses
// embedModeAll and embedModeMetadata into "true" — anything
// beyond embedModeNone still triggers the embed pipeline.
func onlineEmbedEnabled() bool {
	return onlineEmbedMode() != embedModeNone
}

// embedOnlineServerDownloadMetadata runs the full embed pipeline
// for a server-mode task that just finished writing the audio
// file at audioPath. The function is fire-and-forget: it always
// returns; sub-step errors are logged and the file is left in a
// playable state (i.e. without the cover / lyric / extra tags).
//
// We call onlineEmbedCleanupArtwork at the end so the per-task
// cover file (if any) is removed regardless of success — the
// embedder has already merged the cover into the audio file's
// native container.
func embedOnlineServerDownloadMetadata(
	ctx context.Context,
	task *onlineDownloadTask,
	candidate onlineSource,
	quality string,
	audioPath string,
) (string, error) {
	// Top-of-function trace: one [EMBED] line per server
	// download attempt. If the user reports "no LYRICS tag
	// and no [EMBED] log", this line being absent means
	// the embed step isn't being called at all.
	embedTrace(context.Background(), "embed:server-entry-reached", "task", task.ID, "audio", audioPath, "quality", quality, "source", candidate.ID)
	if audioPath == "" {
		embedTrace(ctx, "server:no-audio-path", "task", task.ID)
		return audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("audio path is empty"))
	}
	embedTrace(ctx, "server:enter", "task", task.ID, "audio", audioPath, "quality", quality, "source", candidate.ID)
	// Use a detached context (not the per-attempt ctx) so a
	// stall-detector or user cancel racing the embed doesn't
	// abort the ffmpeg run mid-rewrite. The embed step is
	// best-effort by design.
	embedCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx // referenced for log lines / future cancellation hooks

	songInfo := task.SongInfo
	if songInfo == nil {
		songInfo = buildOnlineDownloadTaskSongInfo(task)
	}

	// 3-state embed mode: "none" short-circuits the entire
	// pipeline (no cover fetch, no lyric round-trip, no ffmpeg
	// rewrite). This is the cheap opt-out for users who want the
	// audio bitstream as-is.
	embedMode := onlineEmbedMode()
	if embedMode == embedModeNone {
		embedTrace(embedCtx, "server:skip-none-mode", "task", task.ID)
		return audioPath, nil
	}
	finalPath, err := strictOnlineEmbedDownloadedFile(embedCtx, task.DownloadDir, task.ID, songInfo, candidate, task.Source, quality, audioPath)
	if err != nil {
		log.Error(embedCtx, "Online embed: server-mode embed failed", "task", task.ID, "err", err)
		return finalPath, err
	}
	if finalPath != "" && finalPath != audioPath {
		newPath := finalPath
		updateOnlineDownloadTask(task.ID, func(t *onlineDownloadTask) {
			t.FilePath = newPath
			t.FileName = filepath.Base(newPath)
		})
	}
	return finalPath, nil
}

// embedOnlineBrowserTaskWithScript is the variant used by the
// browser async pipeline (runOnlineDownloadTask) where the
// resolving candidate script is known. It calls the script for
// lyrics when one is available, and falls back to the
// songInfo.meta.lrcUrl fetch otherwise.
//
// The task pointer is looked up by id so callers that only have
// a taskID (which is the common case in the async pipeline) can
// use this entry point without an extra round-trip. Pass an
// already-loaded pointer when convenient — both forms end up at
// the same code path.
func embedOnlineBrowserTaskWithScript(
	ctx context.Context,
	taskID string,
	task *onlineDownloadTask,
	candidate onlineSource,
	songInfo map[string]any,
	songSource string,
	quality string,
	audioPath string,
) (string, error) {
	// Top-of-function trace. We log here BEFORE any
	// conditionals so the user can grep their navidrome.log
	// for [EMBED] and see whether this function was even
	// called for a given browser download. The trace is the
	// first thing the function does on entry — no
	// short-circuit above us, no caller-side gate (we get
	// called from a go-routine spawned by the browser
	// download code path). If the user reports "no LYRICS
	// tag and no [EMBED] log", this line being absent means
	// the embed step isn't being called at all (likely a
	// caller-side bug); this line being present means the
	// function ran and we can trace further.
	embedTrace(context.Background(), "embed:browser-entry-reached", "task", taskID, "songSource", songSource, "candidate", candidate.ID, "audio", audioPath, "quality", quality)
	if audioPath == "" {
		embedTrace(ctx, "browser:no-audio-path", "task", taskID)
		return audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("audio path is empty"))
	}
	embedTrace(ctx, "browser:enter", "task", taskID, "audio", audioPath, "source", candidate.ID)
	if task == nil {
		task = lookupOnlineDownloadTaskForEmbed(taskID)
		if task == nil {
			return audioPath, newOnlineEmbedFailure("元数据嵌入失败", fmt.Errorf("download task not found"))
		}
	}
	embedCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx

	// 3-state embed mode — see the matching block in
	// embedOnlineServerDownloadMetadata. "none" skips the entire
	// pipeline so the audio file is left untouched.
	embedMode := onlineEmbedMode()
	if embedMode == embedModeNone {
		embedTrace(embedCtx, "browser:skip-none-mode", "task", taskID)
		return audioPath, nil
	}
	finalPath, err := strictOnlineEmbedDownloadedFile(embedCtx, filepath.Dir(audioPath), task.ID, songInfo, candidate, songSource, quality, audioPath)
	if err != nil {
		log.Error(embedCtx, "Online embed: browser-task embed failed", "task", task.ID, "err", err)
		return finalPath, err
	}
	if finalPath != "" && finalPath != audioPath {
		task.FilePath = finalPath
		task.FileName = filepath.Base(finalPath)
	}
	return finalPath, nil
}

// lookupOnlineDownloadTaskForEmbed is a small helper that fetches
// the task pointer by id and tolerates the task having been
// removed (which can happen if the user deletes it from the
// download list mid-flight). It returns nil when the lookup
// fails, matching the "best effort" semantics of the embed
// pipeline.
func lookupOnlineDownloadTaskForEmbed(taskID string) *onlineDownloadTask {
	task, ok := getOnlineDownloadTaskPointer(taskID)
	if !ok {
		return nil
	}
	return task
}

// fetchAndPersistOnlineCover is the convenience that the two
// embed entry points share: fetch the cover URL from the song
// info, write the body to disk under the download directory, and
// return an onlineEmbedArtworkRef ffmpeg can consume. Returns
// (nil, nil) when no cover URL is available — that's a normal
// outcome for songs whose source scripts don't expose art.
func fetchAndPersistOnlineCover(ctx context.Context, downloadDir, taskID string, songInfo map[string]any) *onlineEmbedArtworkRef {
	coverURL := pickOnlineEmbedCoverURL(songInfo)
	if coverURL == "" {
		embedTrace(ctx, "cover:no-url", "task", taskID, "songName", stringValue(songInfo["name"]))
		return nil
	}
	embedTrace(ctx, "cover:fetching", "task", taskID, "url", coverURL)

	// We need the body bytes to write to disk, so do the fetch
	// here rather than going through downloadOnlineEmbedCover
	// (which only returns metadata).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		log.Warn(ctx, "Online embed: cover request build failed", "task", taskID, "err", err)
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if parsed, parseErr := url.Parse(coverURL); parseErr == nil {
		req.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host)
	}
	client := &http.Client{Timeout: onlineEmbedCoverTimeout}
	resp, err := client.Do(req)
	if err != nil {
		embedTrace(ctx, "cover:fetch-failed", "task", taskID, "err", err.Error())
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		embedTrace(ctx, "cover:bad-status", "task", taskID, "status", resp.StatusCode)
		return nil
	}
	limited := io.LimitReader(resp.Body, onlineEmbedCoverMaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		embedTrace(ctx, "cover:read-failed", "task", taskID, "err", err.Error())
		return nil
	}
	if int64(len(body)) > onlineEmbedCoverMaxBytes {
		embedTrace(ctx, "cover:too-large", "task", taskID, "size", len(body))
		return nil
	}
	if len(body) == 0 {
		embedTrace(ctx, "cover:empty-body", "task", taskID)
		return nil
	}

	// Quick sniff — reject JSON / HTML error stubs masquerading
	// as 200 OK.
	head := body
	if len(head) > 16 {
		head = head[:16]
	}
	trimmed := strings.TrimSpace(string(head))
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "<") {
		embedTrace(ctx, "cover:payload-looked-like-json-or-html", "task", taskID, "head", trimmed)
		return nil
	}

	mime := strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	if mime == "" {
		mime = http.DetectContentType(body)
	}

	path, err := persistOnlineEmbedCover(downloadDir, taskID, mime, body)
	if err != nil {
		embedTrace(ctx, "cover:persist-failed", "task", taskID, "err", err.Error())
		return nil
	}
	// Down-scale the source cover before the embed step. We
	// write the resized JPEG to a sibling path and atomically
	// rename it over the source — ffmpeg's behavior on
	// in-place (-y) writes is to truncate-then-write, which
	// can leave a 0-byte file if the process is killed mid-run
	// and which a few ffmpeg builds outright refuse. Failure
	// here is non-fatal: a too-big cover still plays, it just
	// bloats the audio file.
	resizedPath := path + ".resized.jpg"
	if _, resizeErr := onlineEmbedResizeCover(ctx, path, resizedPath); resizeErr != nil {
		embedTrace(ctx, "cover:resize-failed-keeping-source", "task", taskID, "path", path, "err", resizeErr.Error())
		_ = os.Remove(resizedPath)
	} else {
		if renameErr := os.Rename(resizedPath, path); renameErr != nil {
			embedTrace(ctx, "cover:resize-rename-failed", "task", taskID, "path", path, "err", renameErr.Error())
		} else {
			// Update mime to image/jpeg so the embed step
			// writes a jpeg-tagged APIC frame instead of
			// declaring a png (or worse, an exotic webp)
			// frame.
			mime = "image/jpeg"
		}
	}
	embedTrace(ctx, "cover:done", "task", taskID, "path", path, "mime", mime)
	return &onlineEmbedArtworkRef{Path: path, Mime: mime}
}

// lookupOnlineBrowserLyricFromSongInfo reads songInfo.meta.lrcUrl,
// fetches the lyric body, and returns it as text. It is used as the
// second-priority fallback in the unified lyric strategy after script
// lyric and before multi-source fallback. Returns "" when not set or
// on any error — same convention as the other lyric fetchers.
func lookupOnlineBrowserLyricFromSongInfo(ctx context.Context, songInfo map[string]any) string {
	meta := mapValue(songInfo["meta"])
	if meta == nil {
		return ""
	}
	lrcURL := strings.TrimSpace(stringValue(meta["lrcUrl"]))
	if lrcURL == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lrcURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if parsed, parseErr := url.Parse(lrcURL); parseErr == nil {
		req.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host)
	}
	client := &http.Client{Timeout: onlineEmbedCoverTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// onlineEmbedResizeCover runs ffmpeg to scale `srcPath` to
// `dstPath` as a JPEG. We pick a max long edge of
// onlineEmbedCoverMaxWidth (800) and the JPEG quality on
// onlineEmbedCoverJpegQuality (5) so the result lands at
// 100-200 KB regardless of source resolution. If the encoded
// output is still over onlineEmbedCoverEmbedMaxBytes (300 KB)
// — typical of a high-detail scan at any quality — we
// re-encode with a slightly higher q value. We never go past
// q=10 because anything beyond starts to look visibly soft on
// busy covers.
//
// ffmpeg is used here (and only here for cover resizing)
// because it's already a hard dependency of navidrome for
// transcoding; pulling in a Go image library just for this
// would add 30 MB of binary size for a feature the user can
// already disable with a one-line setting change.
//
// Returns the size of the encoded file on success. Failure
// is non-fatal: the caller falls back to embedding the source
// image as-is.
func onlineEmbedResizeCover(ctx context.Context, srcPath, dstPath string) (int64, error) {
	ffmpegImpl := ffmpeg.New()
	cmdPath, err := ffmpegImpl.CmdPath()
	if err != nil {
		return 0, err
	}

	// We try a 3-step quality ramp. Each pass is independent —
	// we always read from srcPath and write to dstPath — so a
	// bad quality choice simply produces a too-big file that the
	// next pass overwrites. Stop as soon as we land under the
	// cap; if even q=10 is too big, we still ship the q=10
	// version because the alternative is "no cover at all",
	// which is strictly worse for the user.
	attempts := []int{onlineEmbedCoverJpegQuality, 7, 10}
	for _, q := range attempts {
		args := []string{
			"-y", "-hide_banner", "-loglevel", "error",
			"-i", srcPath,
			"-vf", fmt.Sprintf("scale='if(gt(iw,%d),%d,iw)':-1", onlineEmbedCoverMaxWidth, onlineEmbedCoverMaxWidth),
			"-q:v", strconv.Itoa(q),
			"-f", "mjpeg",
			dstPath,
		}
		cmd := exec.CommandContext(ctx, cmdPath, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("ffmpeg cover resize q=%d failed: %w (stderr=%s)", q, err, strings.TrimSpace(stderr.String()))
		}
		st, statErr := os.Stat(dstPath)
		if statErr != nil {
			return 0, statErr
		}
		embedTrace(ctx, "cover:resized", "src", srcPath, "dst", dstPath, "q", q, "size", st.Size())
		if st.Size() <= int64(onlineEmbedCoverEmbedMaxBytes) {
			return st.Size(), nil
		}
	}
	// Last attempt wrote a file that's still over the cap; ship
	// it anyway rather than dropping the cover.
	st, statErr := os.Stat(dstPath)
	if statErr != nil {
		return 0, statErr
	}
	return st.Size(), nil
}

// commandExists is a tiny helper so we don't have to take a
// dependency on an os/exec util elsewhere in this file.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
