package nativeapi

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFixUserYHRJFile exercises the USLT writer against the
// actual file the user reported broken (烟火人间-添儿呗.mp3).
// We copy the file to a temp dir, call onlineEmbedWriteID3USLT,
// then assert the rewritten file's ID3v2 tag body size
// matches the sum of the actual frame bytes (i.e. no padding
// from the original tag end leaks into the audio slice).
func TestFixUserYHRJFile(t *testing.T) {
	src := "/home/conor/projects/navidrome_tmp/music/烟火人间-添儿呗.mp3"
	if _, err := os.Stat(src); err != nil {
		t.Skipf("user's file not present: %v", err)
	}
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "fix.mp3")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlineEmbedWriteID3USLT(dst, "[ti:烟火人间]\n[ar:添儿呗]\n[00:00.00]test"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	// Read the rewritten file
	out, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Parse the ID3v2 header
	if len(out) < 10 || string(out[:3]) != "ID3" {
		t.Fatal("rewritten file has no ID3v2 header")
	}
	declaredSize := int(out[6])<<21 | int(out[7])<<14 | int(out[8])<<7 | int(out[9])
	t.Logf("rewritten file: %d bytes, ID3v2 header size: %d, header end: %d", len(out), declaredSize, 10+declaredSize)
	// Walk the frames and check the parser ends exactly at the header end.
	// Mirrors the production walker in id3v2FilterFrames:
	// crucially, a malformed size is clamped to "rest of the tag body"
	// so a bad size can't make us walk off the end. We also assume
	// ID3v2.4 synchsafe (the same assumption the writer uses).
	pos := 10
	for pos+10 <= 10+declaredSize {
		if out[pos] == 0 {
			break
		}
		fsz := int(out[pos+4])<<21 | int(out[pos+5])<<14 | int(out[pos+6])<<7 | int(out[pos+7])
		// Clamp to remaining tag body
		remaining := 10 + declaredSize - pos - 10
		if fsz > remaining {
			fsz = remaining
		}
		pos += 10 + fsz
	}
	t.Logf("frame walker ended at pos %d", pos)
	if pos != 10+declaredSize {
		t.Fatalf("ID3v2 frame walker ended at %d, expected %d — file is malformed", pos, 10+declaredSize)
	}
	// Audio starts right after the ID3v2 header
	if len(out) > 10+declaredSize+4 {
		firstBytes := out[10+declaredSize : 10+declaredSize+4]
		t.Logf("first 4 audio bytes after ID3v2 end: %x", firstBytes)
		// First byte of MP3 frame should be 0xFF (frame sync)
		if firstBytes[0] != 0xFF {
			t.Errorf("expected 0xFF (MP3 frame sync) right after ID3v2 end, got 0x%02x — leftover padding in audio slice", firstBytes[0])
		}
	}
}
