package nativeapi

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestID3v24USLTFrameFormat pins the wire format of the
// USLT frame we emit. The frame header is 10 bytes
// (4-byte ID + 4-byte synchsafe size + 2-byte flags),
// the body is encoding(1) + language(3) + descriptor(NUL
// terminator) + lyrics. The first 4 bytes MUST be
// "USLT" so mp3tag's frame parser picks it up. The
// encoding byte MUST be 3 (UTF-8) so Chinese / Korean /
// Japanese characters render correctly. The 4-byte
// size field MUST be synchsafe-encoded (each byte's
// high bit is 0) so 2.4 readers can scan past frames
// without interpreting the data.
func TestID3v24USLTFrameFormat(t *testing.T) {
	frame := buildID3v24USLTFrame("[00:00.00]hello 一二三", "zho")
	if !bytes.Equal(frame[:4], []byte("USLT")) {
		t.Fatalf("frame ID should be USLT, got %q", frame[:4])
	}
	// Decode synchsafe size.
	v := uint32(frame[4])<<21 | uint32(frame[5])<<14 | uint32(frame[6])<<7 | uint32(frame[7])
	if v == 0 || int(v) != len(frame)-10 {
		t.Fatalf("synchsafe size %d != body length %d", v, len(frame)-10)
	}
	// Encoding byte = 3 (UTF-8).
	if frame[10] != 0x03 {
		t.Fatalf("encoding byte should be 0x03 (UTF-8), got 0x%02x", frame[10])
	}
	// Language = "zho".
	if !bytes.Equal(frame[11:14], []byte("zho")) {
		t.Fatalf("language should be zho, got %q", frame[11:14])
	}
	// Descriptor terminator.
	if frame[14] != 0x00 {
		t.Fatalf("descriptor terminator should be 0x00, got 0x%02x", frame[14])
	}
	// Lyric text — should be the UTF-8 bytes verbatim.
	want := "[00:00.00]hello 一二三"
	if string(frame[15:]) != want {
		t.Fatalf("lyric text mismatch: got %q, want %q", frame[15:], want)
	}
	// Sanity: every byte of the synchsafe size must have
	// the high bit clear (this is what makes the
	// encoding "synchsafe" — the 0x80 marker is reserved
	// for ID3v2 frame boundaries).
	for i := 4; i < 8; i++ {
		if frame[i]&0x80 != 0 {
			t.Fatalf("synchsafe byte at offset %d has high bit set: 0x%02x", i, frame[i])
		}
	}
}

func TestID3v23USLTFrameFormat(t *testing.T) {
	frame := buildID3v23USLTFrame("[00:00.00]hello 一二三", "zho")
	if !bytes.Equal(frame[:4], []byte("USLT")) {
		t.Fatalf("frame ID should be USLT, got %q", frame[:4])
	}
	// ID3v2.3 frame size is 32-bit big-endian.
	v := uint32(frame[4])<<24 | uint32(frame[5])<<16 | uint32(frame[6])<<8 | uint32(frame[7])
	if v == 0 || int(v) != len(frame)-10 {
		t.Fatalf("big-endian size %d != body length %d", v, len(frame)-10)
	}
	if frame[10] != 0x01 {
		t.Fatalf("encoding byte should be 0x01 (UTF-16 BOM), got 0x%02x", frame[10])
	}
	if !bytes.Equal(frame[11:14], []byte("zho")) {
		t.Fatalf("language should be zho, got %q", frame[11:14])
	}
	// Empty descriptor: UTF-16LE BOM + null terminator.
	if !bytes.Equal(frame[14:18], []byte{0xFF, 0xFE, 0x00, 0x00}) {
		t.Fatalf("descriptor bytes mismatch: % x", frame[14:18])
	}
	// Lyric text should start with UTF-16LE BOM.
	if !bytes.Equal(frame[18:20], []byte{0xFF, 0xFE}) {
		t.Fatalf("lyric text should start with UTF-16 BOM, got % x", frame[18:20])
	}
}

// TestWriteID3USLTIntoExistingFile simulates the full
// pipeline: an MP3 file already has an ID3v2 header
// (built by ffmpeg with the broken TXXX wrapper) plus a
// music payload. We call onlineEmbedWriteID3USLT to
// replace the ID3 tag with one that contains a real USLT
// frame, and verify the music payload is unchanged.
func TestWriteID3USLTIntoExistingFile(t *testing.T) {
	dir := t.TempDir()
	// Construct a fake MP3: ID3v2 header (10) + ID3v2
	// body (1 TXXX frame, 1 TIT2 frame) + audio payload
	// (10 bytes of zeros).
	id3Body := []byte{
		// TXXX frame: "TXXX" + size(4 BE) + flags(2) + payload
		'T', 'X', 'X', 'X',
		0x00, 0x00, 0x00, 0x08, // size = 8 (descriptor + 2 bytes lyrics)
		0x00, 0x00,
		0x00, 'U', 'S', 'L', 'T', 0x00, // descriptor "USLT\0"
		'h', 'i', // 2 bytes of lyrics in TXXX
		// TIT2 frame: title
		'T', 'I', 'T', '2',
		0x00, 0x00, 0x00, 0x05,
		0x00, 0x00,
		0x00, 'T', 'i', 't', 'l',
	}
	id3Header := []byte{'I', 'D', '3', 0x04, 0x00, 0x00}
	v := uint32(len(id3Body))
	id3Header = append(id3Header,
		byte((v>>21)&0x7f), byte((v>>14)&0x7f),
		byte((v>>7)&0x7f), byte(v&0x7f))
	audio := bytes.Repeat([]byte{0x55}, 200) // fake audio frames
	fullFile := append(append([]byte{}, id3Header...), id3Body...)
	fullFile = append(fullFile, audio...)
	path := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(path, fullFile, 0o600); err != nil {
		t.Fatal(err)
	}
	// Call the rewrite.
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]real lyrics 一二三"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	// Read back and verify.
	out, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:3], []byte("ID3")) {
		t.Fatalf("output should start with ID3, got %q", out[:3])
	}
	if out[3] != 4 {
		t.Fatalf("ID3v2 version should be 4, got %d", out[3])
	}
	// Synchsafe size should match body length.
	bodySize := int(out[6])<<21 | int(out[7])<<14 | int(out[8])<<7 | int(out[9])
	if bodySize == 0 || bodySize+10 > len(out) {
		t.Fatalf("invalid ID3v2 size: %d (file %d bytes)", bodySize, len(out))
	}
	// TIT2 was preserved (it was the second frame in the
	// source, but in the rewritten tag it comes first
	// because we prepend the surviving TIT2 to our
	// newly-built USLT frame). USLT is the second frame.
	pos := 10
	frameID := out[pos : pos+4]
	if !bytes.Equal(frameID, []byte("TIT2")) {
		t.Fatalf("first frame should be preserved TIT2, got %q", frameID)
	}
	// Walk all frames in the rewritten tag and confirm
	// both USLT and TIT2 are present, but TXXX is gone.
	var foundUSLT, foundTIT2, foundTXXX bool
	for pos+10 <= 10+bodySize {
		fid := string(out[pos : pos+4])
		switch fid {
		case "USLT":
			foundUSLT = true
		case "TIT2":
			foundTIT2 = true
		case "TXXX":
			foundTXXX = true
		}
		// ID3v2.4 synchsafe size decode (28 bits, 4 bytes).
		fsize := int(uint32(out[pos+4])<<21 | uint32(out[pos+5])<<14 | uint32(out[pos+6])<<7 | uint32(out[pos+7]))
		pos += 10 + fsize
	}
	if !foundUSLT {
		t.Fatal("rewritten tag is missing USLT frame")
	}
	if !foundTIT2 {
		t.Fatal("rewritten tag is missing the TIT2 (title) frame — the USLT writer must preserve other frames")
	}
	if foundTXXX {
		t.Fatal("rewritten tag still has the ffmpeg-emitted TXXX wrapper; the writer must drop it before injecting the real USLT")
	}
	// Audio payload should be preserved at the end.
	audioStart := 10 + bodySize
	if !bytes.Equal(out[audioStart:], audio) {
		t.Fatalf("audio payload was not preserved: got %d bytes, want %d", len(out)-audioStart, len(audio))
	}
}

// TestWriteID3USLTIntoFileWithoutID3 ensures the writer
// handles MP3s that don't have an ID3v2 header at all
// (some pre-tag era files). It must build a fresh
// header from scratch.
func TestWriteID3USLTIntoFileWithoutID3(t *testing.T) {
	dir := t.TempDir()
	audio := bytes.Repeat([]byte{0xab}, 100)
	path := filepath.Join(dir, "raw.mp3")
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]orphan lyrics"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:3], []byte("ID3")) {
		t.Fatalf("output should start with ID3 after rewrite, got %q", out[:3])
	}
	bodySize := int(out[6])<<21 | int(out[7])<<14 | int(out[8])<<7 | int(out[9])
	if bodySize == 0 || bodySize+10 > len(out) {
		t.Fatalf("invalid ID3v2 size: %d", bodySize)
	}
	if !bytes.Equal(out[10:14], []byte("USLT")) {
		t.Fatalf("first frame should be USLT, got %q", out[10:14])
	}
}

// TestWriteID3USLTPreservesV23HeaderAndFrameEncoding verifies that
// rewriting an ID3v2.3 file keeps v2.3 semantics (header version and
// frame size encoding), avoiding mixed v2.4/v2.3 tags that strict
// parsers reject.
func TestWriteID3USLTPreservesV23HeaderAndFrameEncoding(t *testing.T) {
	dir := t.TempDir()
	// Build a minimal v2.3 tag body with TXXX(USLT) + TIT2.
	id3Body := []byte{
		'T', 'X', 'X', 'X',
		0x00, 0x00, 0x00, 0x08,
		0x00, 0x00,
		0x00, 'U', 'S', 'L', 'T', 0x00,
		'h', 'i',
		'T', 'I', 'T', '2',
		0x00, 0x00, 0x00, 0x05,
		0x00, 0x00,
		0x00, 'T', 'i', 't', 'l',
	}
	id3Header := []byte{'I', 'D', '3', 0x03, 0x00, 0x00}
	v := uint32(len(id3Body))
	id3Header = append(id3Header,
		byte((v>>21)&0x7f), byte((v>>14)&0x7f),
		byte((v>>7)&0x7f), byte(v&0x7f))
	audio := bytes.Repeat([]byte{0x55}, 200)
	full := append(append([]byte{}, id3Header...), id3Body...)
	full = append(full, audio...)

	path := filepath.Join(dir, "song-v23.mp3")
	if err := os.WriteFile(path, full, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]real lyrics 一二三"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:3], []byte("ID3")) {
		t.Fatalf("output should start with ID3, got %q", out[:3])
	}
	if out[3] != 3 {
		t.Fatalf("ID3 major version should stay 3, got %d", out[3])
	}
	bodySize := int(out[6])<<21 | int(out[7])<<14 | int(out[8])<<7 | int(out[9])
	if bodySize <= 0 || bodySize+10 > len(out) {
		t.Fatalf("invalid ID3v2 size: %d (file %d bytes)", bodySize, len(out))
	}

	var foundUSLT, foundTIT2, foundTXXX bool
	pos := 10
	for pos+10 <= 10+bodySize {
		if out[pos] == 0 {
			break
		}
		fid := string(out[pos : pos+4])
		switch fid {
		case "USLT":
			foundUSLT = true
			if out[pos+10] != 0x01 {
				t.Fatalf("v2.3 USLT should use UTF-16 BOM encoding byte 0x01, got 0x%02x", out[pos+10])
			}
		case "TIT2":
			foundTIT2 = true
		case "TXXX":
			foundTXXX = true
		}
		// v2.3 frame size decoding: big-endian uint32.
		fsize := int(uint32(out[pos+4])<<24 | uint32(out[pos+5])<<16 | uint32(out[pos+6])<<8 | uint32(out[pos+7]))
		remaining := 10 + bodySize - pos - 10
		if fsize > remaining {
			fsize = remaining
		}
		pos += 10 + fsize
	}
	if pos != 10+bodySize {
		t.Fatalf("v2.3 frame walker ended at %d, expected %d", pos, 10+bodySize)
	}
	if !foundUSLT {
		t.Fatal("rewritten tag is missing USLT")
	}
	if !foundTIT2 {
		t.Fatal("rewritten tag is missing TIT2")
	}
	if foundTXXX {
		t.Fatal("rewritten tag still has TXXX")
	}
	audioStart := 10 + bodySize
	if !bytes.Equal(out[audioStart:], audio) {
		t.Fatalf("audio payload was not preserved: got %d bytes, want %d", len(out)-audioStart, len(audio))
	}
}

// TestWriteID3USLTPreservesID3v1Footer ensures the
// writer doesn't strip the legacy 128-byte ID3v1 footer
// (the "TAG" block at the end of the file) when the
// source has one. Some old tools and Windows File
// Explorer's Properties dialog still read the ID3v1
// fields, so we keep them around.
func TestWriteID3USLTPreservesID3v1Footer(t *testing.T) {
	dir := t.TempDir()
	id3v1 := append([]byte("TAG"), bytes.Repeat([]byte{0}, 125)...)
	audio := append(bytes.Repeat([]byte{0x55}, 200), id3v1...)
	path := filepath.Join(dir, "withtag.mp3")
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlineEmbedWriteID3USLT(path, "[00:00.00]with id3v1"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	// Last 128 bytes should still be the ID3v1 footer.
	if !bytes.Equal(out[len(out)-128:len(out)-125], []byte("TAG")) {
		t.Fatalf("ID3v1 footer was lost: tail=%q", out[len(out)-10:])
	}
}

// TestRewriteVorbisCommentRoundTrip constructs a small
// Vorbis comment block, runs rewriteVorbisComment on it,
// and verifies the LYRICS key is added with the new
// content and any old lyrics=… entry is removed.
func TestRewriteVorbisCommentRoundTrip(t *testing.T) {
	// Build a Vorbis comment block with vendor + 3
	// entries: TITLE, LYRICS=old, ARTIST.
	// Layout: vendor_len(4 LE) + vendor + count(4 LE) + entries
	buf := []byte{
		// vendor: "ffmpeg" (6 bytes)
		0x06, 0x00, 0x00, 0x00,
		'f', 'f', 'm', 'p', 'e', 'g',
		// count: 3
		0x03, 0x00, 0x00, 0x00,
		// entry 1: TITLE=hello
		0x0b, 0x00, 0x00, 0x00,
		'T', 'I', 'T', 'L', 'E', '=', 'h', 'e', 'l', 'l', 'o',
		// entry 2: LYRICS=old lyrics (17 bytes)
		0x11, 0x00, 0x00, 0x00,
		'L', 'Y', 'R', 'I', 'C', 'S', '=', 'o', 'l', 'd', ' ', 'l', 'y', 'r', 'i', 'c', 's',
		// entry 3: ARTIST=bob
		0x0a, 0x00, 0x00, 0x00,
		'A', 'R', 'T', 'I', 'S', 'T', '=', 'b', 'o', 'b',
	}
	out, err := rewriteVorbisComment(buf, "fresh lyrics 一二三")
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	// Parse the result.
	if len(out) < 8 {
		t.Fatal("output too short")
	}
	vlen := int(out[0]) | int(out[1])<<8 | int(out[2])<<16 | int(out[3])<<24
	if vlen != 6 || string(out[4:4+vlen]) != "ffmpeg" {
		t.Fatalf("vendor mismatch: len=%d body=%q", vlen, out[4:4+vlen])
	}
	rest := out[4+vlen:]
	count := int(rest[0]) | int(rest[1])<<8 | int(rest[2])<<16 | int(rest[3])<<24
	rest = rest[4:]
	if count != 3 {
		t.Fatalf("expected 3 comments (TITLE, ARTIST, new LYRICS), got %d", count)
	}
	var entries []string
	for i := 0; i < count; i++ {
		clen := int(rest[0]) | int(rest[1])<<8 | int(rest[2])<<16 | int(rest[3])<<24
		rest = rest[4:]
		entries = append(entries, string(rest[:clen]))
		rest = rest[clen:]
	}
	// Check entries — TITLE and ARTIST preserved, LYRICS
	// replaced.
	wantEntries := map[string]bool{
		"TITLE=hello":             true,
		"ARTIST=bob":              true,
		"LYRICS=fresh lyrics 一二三": true,
	}
	for _, e := range entries {
		if !wantEntries[e] {
			t.Fatalf("unexpected entry: %q", e)
		}
	}
	// Make sure no "lyrics=old" or "lyrics=old lyrics" survived.
	for _, e := range entries {
		if bytes.HasPrefix([]byte(e), []byte("lyrics=")) ||
			bytes.HasPrefix([]byte(e), []byte("LYRICS=old")) {
			t.Fatalf("old lyrics entry survived: %q", e)
		}
	}
}
