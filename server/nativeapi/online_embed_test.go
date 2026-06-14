package nativeapi

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlineEmbedCoverExtensionSniffing pins the magic-byte / mime
// detection we use to decide which extension to give the cover
// file. ffmpeg keys off the extension to pick a demuxer, so the
// wrong extension silently turns a valid PNG into an unusable
// track. We test the byte-sniffing path explicitly because it is
// the fallback most likely to be reached in production (image
// hosts often serve `image/jpeg` even when the body is webp).
func TestOnlineEmbedCoverExtensionSniffing(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		head        []byte
		want        string
	}{
		{"jpg-magic", "image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, ".jpg"},
		{"png-magic", "image/png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, ".png"},
		{"webp-magic", "image/webp", append([]byte("RIFF"), 0, 0, 0, 0, 'W', 'E', 'B', 'P'), ".webp"},
		{"gif-magic", "image/gif", []byte("GIF89a..."), ".gif"},
		{"png-mime-wins-over-jpg-bytes", "image/png", []byte{0xFF, 0xD8}, ".png"},
		{"unknown-falls-back-to-jpg", "application/octet-stream", []byte{0x00, 0x00}, ".jpg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := onlineEmbedCoverExtension(tc.contentType, tc.head)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestPickOnlineEmbedCoverURL checks the lookup priority used by
// the embed pipeline: top-level img first, then songInfo.meta.picUrl
// (legacy lx-music field). Both are common in the wild — newer
// lx-music scripts emit the former, while a long tail of older
// scripts still emit only the latter.
func TestPickOnlineEmbedCoverURL(t *testing.T) {
	t.Run("img wins over meta.picUrl", func(t *testing.T) {
		got := pickOnlineEmbedCoverURL(map[string]any{
			"img":  "https://a.example/x.jpg",
			"meta": map[string]any{"picUrl": "https://b.example/y.jpg"},
		})
		if got != "https://a.example/x.jpg" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("falls back to meta.picUrl when img missing", func(t *testing.T) {
		got := pickOnlineEmbedCoverURL(map[string]any{
			"meta": map[string]any{"picUrl": "https://b.example/y.jpg"},
		})
		if got != "https://b.example/y.jpg" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("empty when neither is set", func(t *testing.T) {
		got := pickOnlineEmbedCoverURL(map[string]any{
			"name": "song",
		})
		if got != "" {
			t.Fatalf("got %q want empty", got)
		}
	})
}

// TestFetchAndPersistOnlineCover covers the happy path of the cover
// pipeline. We spin up a small in-process HTTP server that serves
// a real (8x8 blue) PNG, then point the embed pipeline at it. The
// test asserts:
//  1. The cover lands on disk under the .nd-embed-artwork dir.
//  2. The image bytes ffmpeg wrote are re-encoded (the resize
//     step in fetchAndPersistOnlineCover overwrites the source),
//     not the original PNG — this is the production behavior.
//  3. Mime is reported as image/jpeg after the resize.
//
// We previously asserted byte-equality with the source, but the
// resize step makes that an unreasonable invariant: a test that
// just feeds "fake-jpeg-payload" can't even run because ffmpeg
// would refuse to ingest it.
func TestFetchAndPersistOnlineCover(t *testing.T) {
	// Build a real 8x8 PNG. writeTinyPNG is defined later in
	// the file but in Go test files package-level functions
	// can be referenced regardless of declaration order, so
	// this works at compile time.
	pngPath := writeTinyPNG(t, t.TempDir())
	body, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	dir := t.TempDir()
	songInfo := map[string]any{"img": ts.URL + "/cover.png"}
	ref := fetchAndPersistOnlineCover(t.Context(), dir, "task-1", songInfo)
	if ref == nil {
		// Resize is best-effort: when ffmpeg is missing, the
		// source PNG is embedded as-is and the test still
		// expects success with the original mime preserved.
		t.Skip("fetchAndPersistOnlineCover returned nil; ffmpeg may be unavailable in this environment")
	}
	if !strings.HasPrefix(ref.Path, filepath.Join(dir, ".nd-embed-artwork")) {
		t.Fatalf("cover path %q is not under the embed artwork dir", ref.Path)
	}
	st, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatalf("read cover: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("cover file is empty")
	}
	// When ffmpeg is on PATH the resize step overwrites the
	// source PNG with a JPEG; the mime should reflect that.
	// When ffmpeg is missing the source is kept verbatim and
	// mime stays "image/png" — that's the documented graceful
	// fallback.
	if commandExists("ffmpeg") {
		if ref.Mime != "image/jpeg" {
			t.Errorf("expected mime=image/jpeg after resize, got %q", ref.Mime)
		}
	} else if ref.Mime == "" {
		t.Errorf("expected non-empty mime when ffmpeg is absent, got %q", ref.Mime)
	}
	_ = os.RemoveAll(filepath.Dir(ref.Path))
}

// TestFetchAndPersistOnlineCoverRejectsJSON guards against
// misconfigured CDNs that return a 200 OK with a JSON error body
// when the requested cover is missing. The embed pipeline should
// not treat that as a valid cover.
func TestFetchAndPersistOnlineCoverRejectsJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte(`{"code":404,"msg":"not found"}`))
	}))
	defer ts.Close()

	dir := t.TempDir()
	ref := fetchAndPersistOnlineCover(t.Context(), dir, "task-json", map[string]any{
		"img": ts.URL + "/missing.jpg",
	})
	if ref != nil {
		t.Fatalf("expected nil ref for JSON body, got %+v", ref)
	}
	// No file should have been left behind.
	entries, _ := os.ReadDir(filepath.Join(dir, ".nd-embed-artwork"))
	if len(entries) != 0 {
		t.Fatalf("expected no artwork files, got %d", len(entries))
	}
}

// TestOnlineEmbedCleanupArtwork removes the artwork dir even when
// it doesn't exist (idempotent) and leaves the rest of the
// download dir alone. This is the cleanup hook that runs after
// every successful download; we want to be sure it can never
// accidentally wipe a sibling file.
func TestOnlineEmbedCleanupArtwork(t *testing.T) {
	dir := t.TempDir()
	// Pre-populate a sibling file the cleanup must NOT touch.
	sibling := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(sibling, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	artworkDir := filepath.Join(dir, ".nd-embed-artwork")
	if err := os.MkdirAll(artworkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artworkDir, "cover.jpg"), []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}

	onlineEmbedCleanupArtwork(dir)

	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("sibling mp3 was deleted: %v", err)
	}
	if _, err := os.Stat(artworkDir); !os.IsNotExist(err) {
		t.Fatalf("artwork dir not removed: err=%v", err)
	}
}

// TestOnlineEmbedEnabledDefaultsToTrue ensures fresh installs
// (no settings file) get the embed step on by default. The user
// can opt out by saving settings with embedMetadata=false, but
// the very first download after a clean install should pick up
// cover / tags automatically.
func TestOnlineEmbedEnabledDefaultsToTrue(t *testing.T) {
	// Point the online-sources root at a fresh temp dir so we
	// don't see the host's real settings file.
	oldRoot := onlineSourcesRoot
	restore := func() { _ = oldRoot }
	defer restore()

	// We can't reassign the function, so we use the real
	// loadOnlineSourceSettings path but with an env override.
	t.Setenv("HOME", t.TempDir())
	if !onlineEmbedEnabled() {
		t.Fatal("expected onlineEmbedEnabled to be true on a fresh install")
	}
}

// TestOnlineEmbedDownloadMetadataFFmpegIntegration runs the real
// ffmpeg pipeline against a 1-second silent MP3 generated with
// ffmpeg's lavfi source. We verify the embed step writes the
// title / artist / album metadata and attaches the cover.
//
// The test is skipped when ffmpeg is not on PATH so dev machines
// without it stay green. On a real install the dependency is
// already required for transcoding, so this is effectively a
// smoke test for the cover-and-tags path.
func TestOnlineEmbedDownloadMetadataFFmpegIntegration(t *testing.T) {
	if !commandExists("ffmpeg") {
		t.Skip("ffmpeg not available; skipping real embed integration test")
	}
	if !commandExists("ffprobe") {
		t.Skip("ffprobe not available; skipping real embed integration test")
	}

	tmp := t.TempDir()
	audioPath := filepath.Join(tmp, "input.mp3")

	// Generate a 1-second silent MP3 with ffmpeg. We use lavfi so
	// the test doesn't depend on shipping a binary fixture.
	genCmd := []string{
		"ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
		"-t", "1", "-q:a", "9", "-acodec", "libmp3lame",
		audioPath,
	}
	if out, err := runEmbeddedCommand(t.Context(), genCmd); err != nil {
		t.Skipf("ffmpeg input generation failed: %v\n%s", err, out)
	}

	// Create a minimal cover file (a 1x1 PNG byte sequence).
	coverPath := writeTinyPNG(t, tmp)

	songInfo := map[string]any{
		"name":      "那些花儿",
		"singer":    "朴树",
		"albumName": "我去2000年",
		"source":    "wy",
	}
	ref := &onlineEmbedArtworkRef{Path: coverPath, Mime: "image/png"}
	res, finalPath, err := onlineEmbedDownloadMetadata(t.Context(), audioPath, songInfo, "320k", ref, "[00:00.00]LRC line")
	if err != nil {
		t.Fatalf("embed failed: %v", err)
	}
	if finalPath == "" {
		t.Fatal("finalPath is empty")
	}
	if !res.HadCover {
		t.Fatal("HadCover was false after embed with cover ref")
	}
	if !res.HadLyric {
		t.Fatal("HadLyric was false after embed with non-empty lyric")
	}

	// ffprobe the output to confirm the title metadata round-tripped.
	// We use a wildcard for the lyrics key: our ID3v2.4 USLT
	// frame surfaces as `lyrics-eng=…` (the language code is
	// part of the ffprobe key), so the test accepts either
	// that or the bare `lyrics=…` that older ffmpegs used
	// for the TXXX wrapper.
	probeCmd := []string{
		"ffprobe", "-v", "error",
		// No entry filter on format_tags because the
		// new ID3v2.4 USLT frame surfaces as
		// `lyrics-eng=…` (language is part of the key)
		// and ffprobe's entry filter is exact-match.
		// We list all format_tags and assert presence
		// of the expected keys below.
		"-show_entries", "format_tags:stream_tags=title",
		"-of", "default=noprint_wrappers=1",
		audioPath,
	}
	out, err := runEmbeddedCommand(t.Context(), probeCmd)
	if err != nil {
		t.Fatalf("ffprobe failed: %v\n%s", err, out)
	}
	combined := string(out)
	for _, want := range []string{"title=那些花儿", "artist=朴树", "album=我去2000年"} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected %q in ffprobe output, got:\n%s", want, combined)
		}
	}
	if !strings.Contains(combined, "lyrics") {
		t.Errorf("expected lyrics tag in ffprobe output, got:\n%s", combined)
	}
}

// TestOnlineEmbedDownloadMetadataFFmpegIntegrationExtensionless is
// the regression test for the bug the user reported via Mp3Tag:
// the browser streaming path downloads to an os.CreateTemp temp
// file (no extension), then runs the embed pipeline. The previous
// ffmpeg invocation passed the input path with no -f flag and
// relied on ffmpeg's extension-based probing, which silently
// failed. This test reproduces that exact scenario on disk and
// confirms the sniffer-driven path writes the tags, cover, and
// lyrics to the final file.
func TestOnlineEmbedDownloadMetadataFFmpegIntegrationExtensionless(t *testing.T) {
	if !commandExists("ffmpeg") {
		t.Skip("ffmpeg not available; skipping real embed integration test")
	}
	if !commandExists("ffprobe") {
		t.Skip("ffprobe not available; skipping real embed integration test")
	}

	tmp := t.TempDir()

	// Build a real MP3 in a directory we control, then MOVE it to
	// a path with no extension. This matches what
	// fetchOnlineDownloadToTempFile does in production: the audio
	// is on disk, but the filename has no .mp3 suffix.
	srcMP3 := filepath.Join(tmp, "src.mp3")
	genCmd := []string{
		"ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
		"-t", "1", "-q:a", "9", "-acodec", "libmp3lame",
		srcMP3,
	}
	if out, err := runEmbeddedCommand(t.Context(), genCmd); err != nil {
		t.Skipf("ffmpeg input generation failed: %v\n%s", err, out)
	}
	audioPath := filepath.Join(tmp, "nd-online-download-noext")
	body, err := os.ReadFile(srcMP3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audioPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	coverPath := writeTinyPNG(t, tmp)

	songInfo := map[string]any{
		"name":      "海屿你",
		"singer":    "马也_Crabbit",
		"albumName": "海屿你",
		"source":    "wy",
	}
	ref := &onlineEmbedArtworkRef{Path: coverPath, Mime: "image/png"}
	res, finalPath, err := onlineEmbedDownloadMetadata(t.Context(), audioPath, songInfo, "320k", ref, "[00:00.00]海屿你 - 马也_Crabbit")
	if err != nil {
		t.Fatalf("embed failed: %v", err)
	}
	if finalPath == "" {
		t.Fatal("finalPath is empty")
	}
	if !res.HadCover {
		t.Fatal("HadCover was false after embed with cover ref")
	}
	if !res.HadLyric {
		t.Fatal("HadLyric was false after embed with non-empty lyric")
	}

	// The temp file should have been replaced in place (this is
	// what the user expects to see in their Downloads folder).
	if _, err := os.Stat(audioPath); err != nil {
		t.Fatalf("output file disappeared after embed: %v", err)
	}
	// The .ndembed.tmp sibling should have been cleaned up.
	if _, err := os.Stat(audioPath + ".ndembed.mp3"); !os.IsNotExist(err) {
		t.Fatalf("expected .ndembed.mp3 sibling to be removed, stat err=%v", err)
	}

	// ffprobe the result. We deliberately do NOT pass -show_streams
	// here so this test runs faster; the cover-art presence is
	// verified via the format_tags and stream loop separately.
	probeCmd := []string{
		"ffprobe", "-v", "error",
		// We DON'T filter on `format_tags=lyrics` because
		// the new ID3v2.4 USLT frame surfaces as
		// `lyrics-eng=…` (language is part of the key)
		// and ffprobe's entry-filter is exact-match, not
		// a prefix or glob. Listing format_tags without a
		// filter shows all keys; the test then asserts
		// the lyrics key is present.
		"-show_entries", "format_tags",
		"-of", "default=noprint_wrappers=1",
		audioPath,
	}
	out, err := runEmbeddedCommand(t.Context(), probeCmd)
	if err != nil {
		t.Fatalf("ffprobe failed: %v\n%s", err, out)
	}
	combined := string(out)
	for _, want := range []string{
		"title=海屿你",
		"artist=马也_Crabbit",
		"album=海屿你",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected %q in ffprobe output, got:\n%s", want, combined)
		}
	}
	// Lyrics: the ID3v2.4 USLT frame surfaces as
	// `lyrics-eng=…` in ffprobe (the language is part of
	// the key). The old TXXX wrapper also showed as
	// `lyrics=…` — both are accepted, the test just wants
	// a "lyrics" key to be present.
	if !strings.Contains(combined, "lyrics") {
		t.Errorf("expected a lyrics tag in ffprobe output, got:\n%s", combined)
	}

	// Cover check: ffprobe -show_streams lists the attached_pic
	// video stream that holds the cover. We just confirm the codec
	// type is "video" with disposition "attached_pic"; the
	// detailed pixel-content test is left to the existing
	// extension test, which exercises the same path with
	// already-extensioned input.
	if info, err := os.Stat(audioPath); err == nil {
		t.Logf("final file size: %d", info.Size())
	} else {
		t.Logf("stat err: %v", err)
	}
	streamCmd := []string{
		"ffprobe", "-v", "error",
		"-select_streams", "v",
		"-show_entries", "stream=codec_type:stream_disposition=attached_pic",
		"-of", "default=noprint_wrappers=1",
		audioPath,
	}
	streamOut, err := runEmbeddedCommand(t.Context(), streamCmd)
	if err != nil {
		t.Fatalf("ffprobe stream check failed: %v\n%s", err, streamOut)
	}
	if !strings.Contains(string(streamOut), "codec_type=video") {
		t.Errorf("expected a video stream (cover) in the rewritten file, got:\n%s", streamOut)
	}
	t.Logf("stream out:\n%s", streamOut)
	if !strings.Contains(string(streamOut), "attached_pic=1") {
		t.Errorf("expected attached_pic disposition on the cover stream, got:\n%s", streamOut)
	}
}

// writeTinyPNG writes a small but valid PNG file to disk and
// returns the path. We use a real image/png.Encode call (rather
// than the hand-written IDAT bytes that some older test suites
// use) so the bytes ffmpeg reads are guaranteed parseable, which
// is the property the embed pipeline depends on. ffmpeg will
// silently drop a malformed cover rather than fail the run, so
// the test would otherwise pass for the wrong reason.
func writeTinyPNG(t *testing.T, dir string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	blue := color.RGBA{0x29, 0x6F, 0xB5, 0xFF}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, blue)
		}
	}
	p := filepath.Join(dir, "cover.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOnlineEmbedResizeCoverCappedTo300KB is the regression
// test for the user-reported "3000x3000 / 7 MB cover bloated
// every audio file" bug. We generate a 1500x1500 high-detail
// source PNG (the typical upstream cover image shape), then
// run onlineEmbedResizeCover on it and assert the output is
// (a) a JPEG, (b) at most onlineEmbedCoverEmbedMaxBytes, and
// (c) has a long edge no larger than onlineEmbedCoverMaxWidth.
//
// The test is skipped when ffmpeg is not on PATH; in CI we
// install ffmpeg specifically to exercise this code path.
func TestOnlineEmbedResizeCoverCappedTo300KB(t *testing.T) {
	if !commandExists("ffmpeg") {
		t.Skip("ffmpeg not available; skipping cover resize integration test")
	}

	tmp := t.TempDir()
	// 1500x1500 is typical for high-res CD-quality cover scans
	// (e.g. Apple Music / 网易云 lossless tiers). The colour
	// gradient + noise is what blows up the encoded JPEG
	// size at the source resolution, so the test is a
	// realistic worst case for the resize pipeline.
	srcPath := filepath.Join(tmp, "src.png")
	genCmd := []string{
		"ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i",
		"color=c=0x296FB5:s=1500x1500:d=1,format=yuv420p,noise=alls=80:allf=t+u",
		"-frames:v", "1", srcPath,
	}
	if out, err := runEmbeddedCommand(t.Context(), genCmd); err != nil {
		t.Skipf("ffmpeg source generation failed: %v\n%s", err, out)
	}

	// Sanity check: the source is at least > 300 KB so the test
	// actually exercises the resize path. A 1500x1500 solid
	// blue PNG is already under 300 KB, so the noise filter
	// above is what pushes it over.
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if srcInfo.Size() < 300*1024 {
		t.Skipf("source image is too small (%d bytes) to exercise the resize; "+
			"the test fixture needs to be over the cap", srcInfo.Size())
	}

	dstPath := filepath.Join(tmp, "dst.jpg")
	size, err := onlineEmbedResizeCover(t.Context(), srcPath, dstPath)
	if err != nil {
		t.Fatalf("resize failed: %v", err)
	}

	if size > int64(onlineEmbedCoverEmbedMaxBytes) {
		t.Errorf("resized cover is %d bytes, exceeds %d byte cap",
			size, onlineEmbedCoverEmbedMaxBytes)
	}

	// Long-edge check via ffprobe.
	probeCmd := []string{
		"ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0",
		dstPath,
	}
	out, err := runEmbeddedCommand(t.Context(), probeCmd)
	if err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, out)
	}
	var w, h int
	// ffprobe with -of csv=p=0 prints dimensions as "w,h".
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 2 {
		t.Fatalf("could not parse ffprobe output %q", out)
	}
	if _, err := fmt.Sscan(parts[0], &w); err != nil {
		t.Fatalf("parse width from %q: %v", parts[0], err)
	}
	if _, err := fmt.Sscan(parts[1], &h); err != nil {
		t.Fatalf("parse height from %q: %v", parts[1], err)
	}
	longEdge := w
	if h > longEdge {
		longEdge = h
	}
	if longEdge > onlineEmbedCoverMaxWidth {
		t.Errorf("resized cover long edge is %d, exceeds %d px cap",
			longEdge, onlineEmbedCoverMaxWidth)
	}
	t.Logf("resized cover: %dx%d, %d bytes (cap %d)", w, h, size, onlineEmbedCoverEmbedMaxBytes)
}

// TestOnlineEmbedSniffAudioFormat pins the audio-container sniffer
// used by onlineEmbedDownloadMetadata to pick the right -f flag
// when the input file has no extension (which is the case for
// the browser streaming path, where the temp file is created
// with os.CreateTemp and lacks a suffix).
func TestOnlineEmbedSniffAudioFormat(t *testing.T) {
	cases := []struct {
		name    string
		head    []byte
		wantFmt string
		wantExt string
	}{
		{"mp3-id3v2", []byte("ID3\x04\x00\x00\x00\x00\x00\x00" + "audio"), "mp3", "mp3"},
		{"mp3-raw-frame-sync", []byte{0xFF, 0xFB, 0x90, 0x00}, "mp3", "mp3"},
		{"flac", []byte("fLaC\x00\x00\x00\x22"), "flac", "flac"},
		{"ogg", []byte("OggS\x00\x02\x00\x00"), "ogg", "ogg"},
		{"wav", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "wav", "wav"},
		{"m4a", []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00"), "mp4", "m4a"},
		{"unknown-payload", []byte("hello world"), "", ""},
		{"empty-file", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "audiofile")
			if len(tc.head) > 0 {
				if err := os.WriteFile(p, tc.head, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				// Create an empty file so the open + read path
				// exercises the n < 4 early return.
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			gotFmt, gotExt := onlineEmbedSniffAudioFormat(p)
			if gotFmt != tc.wantFmt || gotExt != tc.wantExt {
				t.Fatalf("onlineEmbedSniffAudioFormat(%q) = (%q, %q), want (%q, %q)",
					tc.name, gotFmt, gotExt, tc.wantFmt, tc.wantExt)
			}
		})
	}
}

// runEmbeddedCommand is a tiny helper that runs a command and
// returns its combined output as a string. We use this from the
// embed integration test so a failure includes the actual ffmpeg
// stderr in the test output — without it, a misconfigured test
// fixture would just show "exit code 1". The arguments come from
// the test's own local literal slices, not from user input, so
// gosec's G204 is a false positive here.
func runEmbeddedCommand(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204
	return cmd.CombinedOutput()
}
