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

// onlineEmbedCoverMaxBytes caps the cover image size. 8 MiB is more
// than enough for any reasonable cover art; rejecting larger
// responses keeps a malicious or misconfigured upstream from
// filling the user's disk or stalling the downloader.
const onlineEmbedCoverMaxBytes = 8 * 1024 * 1024

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

// onlineEmbedResult is what onlineEmbedDownloadMetadata returns to
// the caller. Errors on individual sub-steps are folded into a
// single error so the caller can log them once.
type onlineEmbedResult struct {
	HadCover bool
	HadLyric bool
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
func fetchOnlineEmbedLyric(ctx context.Context, source onlineSource, songSource string, songInfo map[string]any, quality string) (string, error) {
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
		Lyric   string `json:"lyric"`
		LRC     string `json:"lrc"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		embedTrace(ctx, "lyric:bad-json", "source", source.ID, "err", err.Error())
		return "", nil
	}
	if !resp.Success {
		embedTrace(ctx, "lyric:script-said-fail", "source", source.ID, "scriptError", resp.Success)
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
		return "", ""
	}
	defer f.Close()

	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if n < 4 {
		return "", ""
	}

	switch {
	case bytes.HasPrefix(head, []byte("ID3")):
		return "mp3", "mp3"
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
	return "", ""
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
func onlineEmbedDownloadMetadata(
	ctx context.Context,
	audioPath string,
	songInfo map[string]any,
	quality string,
	cover *onlineEmbedArtworkRef,
	lyric string,
) (*onlineEmbedResult, error) {
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

	// If there's nothing to write, skip the ffmpeg invocation. This
	// is the common path when the source script didn't return a
	// cover URL and lyric lookup failed — there is no value in
	// paying the ffmpeg startup cost.
	if !result.HadCover && !result.HadLyric {
		embedTrace(ctx, "download_metadata:skip-empty", "audio", audioPath, "reason", "no cover and no lyric")
		return result, nil
	}

	ffmpegImpl := ffmpeg.New()
	cmdPath, err := ffmpegImpl.CmdPath()
	if err != nil {
		embedTrace(ctx, "download_metadata:ffmpeg-missing", "audio", audioPath, "err", err.Error())
		return result, nil
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
		return result, nil
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

	// Tag metadata. ffmpeg understands -metadata for most common
	// tags; for MP3 it routes to ID3v2 and for FLAC it routes to
	// Vorbis Comments automatically.
	title := strings.TrimSpace(stringValue(songInfo["name"]))
	artist := strings.TrimSpace(stringValue(songInfo["singer"]))
	album := strings.TrimSpace(stringValue(songInfo["albumName"]))
	if title == "" {
		if meta := mapValue(songInfo["meta"]); meta != nil {
			title = strings.TrimSpace(stringValue(meta["songName"]))
		}
	}
	if artist == "" {
		if meta := mapValue(songInfo["meta"]); meta != nil {
			artist = strings.TrimSpace(stringValue(meta["singerName"]))
		}
	}
	if album == "" {
		if meta := mapValue(songInfo["meta"]); meta != nil {
			album = strings.TrimSpace(stringValue(meta["albumName"]))
		}
	}
	if title != "" {
		args = append(args, "-metadata", "title="+title)
	}
	if artist != "" {
		args = append(args, "-metadata", "artist="+artist)
	}
	if album != "" {
		args = append(args, "-metadata", "album="+album)
	}
	if quality != "" {
		args = append(args, "-metadata", "comment=Quality: "+quality)
	}

	if result.HadLyric {
		// ffmpeg routes -metadata lyrics=… to the Vorbis LYRICS
		// field on FLAC and to the ID3 USLT frame on MP3 (when
		// -id3v2_version is the default 3/4). For M4A it lands in
		// the ©lyr atom, which most players surface correctly.
		args = append(args, "-metadata", "lyrics="+lyric)
		// Force ID3v2.3 on the rewrite so older MP3 players (incl.
		// the Windows stock player) still see the lyrics frame.
		args = append(args, "-id3v2_version", "3")
	}

	args = append(args, "-map_metadata", "0", tmpPath)

	// Quiet any other metadata (e.g. encoder) that ffmpeg would
	// otherwise add. We deliberately leave the audio stream alone
	// (no -vn, no -c:a re-encode) so the user's original bitstream
	// is preserved bit-for-bit.

	_ = inputs // kept for future stream-mapping improvements

	_ = formatName // used above when constructing tmpPath

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		embedTrace(ctx, "download_metadata:ffmpeg-failed", "audio", audioPath, "err", err.Error(), "stderr", strings.TrimSpace(stderr.String()))
		return result, nil
	}
	embedTrace(ctx, "download_metadata:ffmpeg-ok", "audio", audioPath)
	if err := os.Rename(tmpPath, audioPath); err != nil {
		embedTrace(ctx, "download_metadata:rename-failed", "audio", audioPath, "err", err.Error())
		return result, nil
	}
	embedTrace(ctx, "download_metadata:done", "audio", audioPath)
	return result, nil
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
) {
	if audioPath == "" {
		embedTrace(ctx, "server:no-audio-path", "task", task.ID)
		return
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
		return
	}

	// 1) Cover
	coverRef := fetchAndPersistOnlineCover(embedCtx, task.DownloadDir, task.ID, songInfo)

	// 2) Lyric — only fetched in "all" mode. We deliberately
	// skip the round-trip in "metadata" mode because most
	// popular lx-music scripts don't expose a lyric action and
	// the dispatch would block ffmpeg for the full timeout
	// window. Wiring the actual merge for "all" lands in a
	// follow-up; for now both modes pass an empty lyric string.
	lyric := ""
	if embedMode == embedModeAll {
		lyric, _ = fetchOnlineEmbedLyric(embedCtx, candidate, task.Source, songInfo, quality)
	}
	if lyric != "" {
		log.Info(embedCtx, "Online embed: lyric fetched for task", "task", task.ID, "len", len(lyric))
	} else {
		log.Debug(embedCtx, "Online embed: no lyric available for task", "task", task.ID)
	}
	if coverRef != nil {
		log.Info(embedCtx, "Online embed: cover fetched for task", "task", task.ID, "path", coverRef.Path, "mime", coverRef.Mime)
	}

	// 3) ffmpeg merge
	if _, err := onlineEmbedDownloadMetadata(embedCtx, audioPath, songInfo, quality, coverRef, lyric); err != nil {
		log.Error(embedCtx, "Online embed: server-mode embed failed", "task", task.ID, "err", err)
	}

	// 4) Cover artifact cleanup
	onlineEmbedCleanupArtwork(task.DownloadDir)
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
) {
	if audioPath == "" {
		embedTrace(ctx, "browser:no-audio-path", "task", taskID)
		return
	}
	embedTrace(ctx, "browser:enter", "task", taskID, "audio", audioPath, "source", candidate.ID)
	if task == nil {
		task = lookupOnlineDownloadTaskForEmbed(taskID)
		if task == nil {
			return
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
		return
	}

	downloadDir := filepath.Dir(audioPath)
	coverRef := fetchAndPersistOnlineCover(embedCtx, downloadDir, task.ID, songInfo)

	// Lyric is only fetched in "all" mode. We also keep the
	// browser-collected meta.lrcUrl fallback (populated by the
	// lx-music script's lyric action) because the browser
	// pipeline often already has the lyrics on hand.
	lyric := ""
	if embedMode == embedModeAll {
		lyric, _ = fetchOnlineEmbedLyric(embedCtx, candidate, songSource, songInfo, quality)
		if lyric == "" {
			lyric = lookupOnlineBrowserLyricFromSongInfo(embedCtx, songInfo)
		}
	}

	if _, err := onlineEmbedDownloadMetadata(embedCtx, audioPath, songInfo, quality, coverRef, lyric); err != nil {
		log.Error(embedCtx, "Online embed: browser-task embed failed", "task", task.ID, "err", err)
	}

	onlineEmbedCleanupArtwork(downloadDir)
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

// lookupOnlineBrowserLyricFromSongInfo is a fallback lyric source
// for browser-mode downloads where we don't have the resolving
// script on hand. It looks at the canonical songInfo.meta.lrcUrl
// field that some lx-music source scripts populate, fetches it,
// and returns the body as a string. Returns "" when not set or on
// any error — same convention as the other lyric fetchers.
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
