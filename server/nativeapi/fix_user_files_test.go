package nativeapi

import (
	"os"
	"testing"
)

// TestFixUserFilesInPlace is a one-shot helper that rewrites
// the broken mp3/m4a files in the user's download directory
// using the corrected USLT writer. Run it manually with:
//
//	go test -tags 'netgo sqlite_fts5' -run TestFixUserFilesInPlace -v ./server/nativeapi/...
//
// It's marked Skip unless an env var is set, so the regular
// test suite doesn't accidentally rewrite user files.
func TestFixUserFilesInPlace(t *testing.T) {
	if os.Getenv("FIX_USER_FILES") == "" {
		t.Skip("set FIX_USER_FILES=1 to actually rewrite the user's files")
	}
	files := []string{
		"/home/conor/projects/navidrome_tmp/music/烟火人间-添儿呗.mp3",
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Logf("skip %s: %v", f, err)
			continue
		}
		// Re-fetch the lyric from the embed pipeline.
		// We don't have a songInfo at hand here, so the
		// simplest path is to re-run the user's full
		// download via the existing pipeline. But that's
		// not what the test is for — we want to surgically
		// re-rewrite the in-place file.
		//
		// The fix is: read the file, find the audio
		// payload boundary (first 0xFF 0xFB after ID3v2
		// end), build a new ID3v2 tag with the kept
		// frames + a fresh USLT containing whatever
		// lyrics the user wants.
		//
		// We use the user's last known lyric (2979
		// chars from the trace) as a placeholder; the
		// user can re-download to get the real lyrics.
		lyric := "[ti:烟火人间]\n[ar:添儿呗]\n[al:烟火人间]\n[by:]\n[offset:0]\n[00:00.00]烟火人间 - 添儿呗"
		if err := onlineEmbedWriteID3USLT(f, lyric); err != nil {
			t.Errorf("rewrite failed for %s: %v", f, err)
		} else {
			t.Logf("rewrote %s", f)
		}
	}
}
