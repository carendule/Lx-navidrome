package nativeapi

import "testing"

// TestOnlineDownloadFileNameUserExample is the exact example from the
// bug report: "那些花儿" / 320k / 朴树 / template [歌名, 音质, 歌手] →
// "那些花儿-320k-朴树.mp3".
func TestOnlineDownloadFileNameUserExample(t *testing.T) {
	song := map[string]any{
		"name":      "那些花儿",
		"singer":    "朴树",
		"album":     "我去2000年",
		"albumName": "我去2000年",
		"source":    "wy",
	}
	got := onlineDownloadFileName(song, "320k", []string{"歌名", "音质", "歌手"}, "https://example.com/track.mp3")
	want := "那些花儿-320k-朴树.mp3"
	if got != want {
		t.Fatalf("filename mismatch: got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameFallbackToDefault: empty / unknown-only
// templates should fall back to the persisted default [歌名, 歌手].
func TestOnlineDownloadFileNameFallbackToDefault(t *testing.T) {
	song := map[string]any{"name": "海屿你", "singer": "马也_Crabbit"}
	cases := []struct {
		name     string
		template []string
		want     string
	}{
		{"nil template", nil, "海屿你-马也_Crabbit.mp3"},
		{"empty slice", []string{}, "海屿你-马也_Crabbit.mp3"},
		{"only unknown tokens", []string{"foo", "bar"}, "海屿你-马也_Crabbit.mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := onlineDownloadFileName(song, "320k", tc.template, "https://example.com/x.mp3")
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestOnlineDownloadFileNameAlbumAndSource: album + source tokens
// resolve to songInfo["albumName"] and a short label.
func TestOnlineDownloadFileNameAlbumAndSource(t *testing.T) {
	song := map[string]any{
		"name":      "夜曲",
		"singer":    "周杰伦",
		"albumName": "十一月的萧邦",
		"source":    "tx",
	}
	got := onlineDownloadFileName(song, "flac", []string{"歌名", "专辑", "来源"}, "https://example.com/x.flac")
	want := "夜曲-十一月的萧邦-QQ.flac"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameSkipsEmptyValues: tokens whose lookup
// returns "" should be dropped so the filename has no dangling "-".
func TestOnlineDownloadFileNameSkipsEmptyValues(t *testing.T) {
	song := map[string]any{"name": "song", "singer": "artist"}
	got := onlineDownloadFileName(song, "128k", []string{"歌名", "专辑", "来源", "歌手"}, "https://x/y.mp3")
	want := "song-artist.mp3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameUnknownExtension: if the URL doesn't end
// in a known audio container, we leave it extension-less (the
// downstream downloader will fill it in from Content-Type).
func TestOnlineDownloadFileNameUnknownExtension(t *testing.T) {
	song := map[string]any{"name": "song"}
	got := onlineDownloadFileName(song, "320k", []string{"歌名", "歌手"}, "https://x/y.bin")
	want := "song"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestOnlineDownloadFileNameStripsPathSegment: ensure the extension is
// taken from the *path* (not the query string) and lowercased.
func TestOnlineDownloadFileNameStripsPathSegment(t *testing.T) {
	song := map[string]any{"name": "song", "singer": "a"}
	got := onlineDownloadFileName(song, "320k", []string{"歌名", "歌手"}, "https://x/y.MP3?token=abc")
	if got != "song-a.mp3" {
		t.Fatalf("got %q", got)
	}
}

// TestSanitizeNameTemplateDropsUnknownTokensButPreservesOrder.
func TestSanitizeNameTemplateDropsUnknownTokensButPreservesOrder(t *testing.T) {
	got := sanitizeNameTemplate([]string{"歌名", "garbage", "音质", "歌名", "歌手"})
	want := []string{"歌名", "音质", "歌手"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestSourceLabel maps known source codes to short labels.
func TestSourceLabel(t *testing.T) {
	cases := map[string]string{
		"wy":      "网易",
		"tx":      "QQ",
		"kg":      "酷狗",
		"kw":      "酷我",
		"mg":      "咪咕",
		"unknown": "unknown",
	}
	for in, want := range cases {
		if got := sourceLabel(in); got != want {
			t.Errorf("sourceLabel(%q)=%q want %q", in, got, want)
		}
	}
}
