package nativeapi

import (
	"path/filepath"
	"testing"

	"github.com/navidrome/navidrome/conf"
)

func TestExtractMetadata(t *testing.T) {
	// Note: This test has been migrated to script_metadata_test.go
	// The new extractMetadataFromCode() function now handles all metadata extraction
	// These tests are kept for backward compatibility reference only
	t.Skip("Metadata extraction tests moved to script_metadata_test.go")
}

func TestExtractSupportedSources(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		expected []string
	}{
		{
			name:     "sources object with colon",
			script:   `var sources = { kg: {}, tx: {}, wy: {} }`,
			expected: []string{"kg", "tx", "wy"},
		},
		{
			name:     "apis object with equals",
			script:   `var apis = { kw: {}, mg: {} }`,
			expected: []string{"kw", "mg"},
		},
		{
			name:     "qualitys object with equals",
			script:   `var qualitys = { wy: {}, mg: {} }`,
			expected: []string{"mg", "wy"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractSupportedSources(tt.script)
			t.Logf("Got sources: %v", result)
			if len(result) != len(tt.expected) {
				t.Errorf("Got %d sources, want %d: %v", len(result), len(tt.expected), result)
				return
			}
			resultMap := make(map[string]bool)
			for _, s := range result {
				resultMap[s] = true
			}
			for _, exp := range tt.expected {
				if !resultMap[exp] {
					t.Errorf("Missing expected source: %q", exp)
				}
			}
		})
	}
}

func TestCreateOnlineSourceUsesRuntimeSources(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	defer func() {
		conf.Server.DataFolder = oldDataFolder
	}()

	script := `
/* @name Runtime Source @version 1.0.0 @author Test */
const sources = {
  tx: { name: "QQ音乐" },
  wy: { name: "网易云音乐" },
  kw: { name: "酷狗音乐" }
};

lx.send('inited', { sources: sources });

lx.on('request', function (data) {
  return data;
});
`

	source, err := createOnlineSource("runtime.js", script, "", false)
	if err != nil {
		t.Fatalf("createOnlineSource returned error: %v", err)
	}

	if source.Name != "Runtime Source" {
		t.Fatalf("unexpected source name: %q", source.Name)
	}

	if len(source.SupportedSources) != 3 {
		t.Fatalf("expected 3 supported sources, got %d: %v", len(source.SupportedSources), source.SupportedSources)
	}

	got := make(map[string]bool, len(source.SupportedSources))
	for _, key := range source.SupportedSources {
		got[key] = true
	}
	for _, key := range []string{"tx", "wy", "kw"} {
		if !got[key] {
			t.Fatalf("missing supported source %q in %v", key, source.SupportedSources)
		}
	}
}

func TestLoadOnlineSourceSettingsDefaultsToMusicFolder(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}

	if settings.DownloadPath != "/music/default" {
		t.Fatalf("unexpected default download path: %q", settings.DownloadPath)
	}
}

func TestSaveOnlineSourceSettingsPersistsDownloadPath(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	expected := filepath.Join(tmpDir, "downloads")
	if err := saveOnlineSourceSettings(onlineSourceSettings{DownloadPath: expected}); err != nil {
		t.Fatalf("saveOnlineSourceSettings returned error: %v", err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}

	if settings.DownloadPath != expected {
		t.Fatalf("unexpected persisted download path: got %q want %q", settings.DownloadPath, expected)
	}
}

func TestExtractMetadataChineseName(t *testing.T) {
	// Note: This test has been migrated to script_metadata_test.go
	// The new extractMetadataFromCode() function now handles all metadata extraction
	t.Skip("Metadata extraction tests moved to script_metadata_test.go")
}

func TestMinifiedSourceExtraction(t *testing.T) {
	// Simulating a minified script with sources definitions
	script := `var _0x1a2b=["kg","tx","wy","kw","mg"];var sources={kg:{url:"..."},tx:{url:"..."},wy:{url:"..."},kw:{url:"..."},mg:{url:"..."}};`

	result := extractSupportedSources(script)
	expected := []string{"kg", "kw", "mg", "tx", "wy"}

	if len(result) != len(expected) {
		t.Logf("Got %d sources: %v, want %d: %v", len(result), result, len(expected), expected)
	}

	resultMap := make(map[string]bool)
	for _, s := range result {
		resultMap[s] = true
	}

	for _, exp := range expected {
		if !resultMap[exp] {
			t.Errorf("Missing expected source: %q", exp)
		}
	}
}

func TestExtractMetadataFromFilename(t *testing.T) {
	// DEPRECATED: This function has been removed in favor of extractMetadataFromCode()
	// which only extracts metadata from script code, not filenames.
	// This is more secure and follows LxServer best practices.
	t.Skip("Filename-based metadata extraction is deprecated")
}

func TestValidateOnlineSourceScript(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		content   string
		shouldErr bool
	}{
		{
			name:      "valid js file",
			filename:  "test.js",
			content:   "console.log('test');",
			shouldErr: false,
		},
		{
			name:      "invalid extension",
			filename:  "test.txt",
			content:   "console.log('test');",
			shouldErr: true,
		},
		{
			name:      "empty content",
			filename:  "test.js",
			content:   "",
			shouldErr: true,
		},
		{
			name:      "whitespace only",
			filename:  "test.js",
			content:   "   \n\t  ",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOnlineSourceScript(tt.filename, tt.content)
			if (err != nil) != tt.shouldErr {
				t.Errorf("Expected error: %v, got: %v", tt.shouldErr, err)
			}
		})
	}
}

func TestLoadOnlineSourceSettingsDefaultsNameTemplate(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}
	want := []string{"歌名", "歌手"}
	if len(settings.NameTemplate) != len(want) {
		t.Fatalf("unexpected default NameTemplate length: got %d want %d", len(settings.NameTemplate), len(want))
	}
	for i, v := range want {
		if settings.NameTemplate[i] != v {
			t.Fatalf("unexpected default NameTemplate[%d]: got %q want %q", i, settings.NameTemplate[i], v)
		}
	}
}

func TestSaveOnlineSourceSettingsPersistsNameTemplate(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	want := []string{"歌名", "歌手", "专辑"}
	if err := saveOnlineSourceSettings(onlineSourceSettings{
		DownloadPath: filepath.Join(tmpDir, "downloads"),
		NameTemplate: want,
	}); err != nil {
		t.Fatalf("saveOnlineSourceSettings returned error: %v", err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings returned error: %v", err)
	}
	if len(settings.NameTemplate) != len(want) {
		t.Fatalf("unexpected persisted NameTemplate length: got %d want %d", len(settings.NameTemplate), len(want))
	}
	for i, v := range want {
		if settings.NameTemplate[i] != v {
			t.Fatalf("unexpected persisted NameTemplate[%d]: got %q want %q", i, settings.NameTemplate[i], v)
		}
	}
}

func TestSanitizeOnlineNameTemplate(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{"歌名", "歌手"}},
		{"empty", []string{}, []string{"歌名", "歌手"}},
		{"only-invalid", []string{"foo", "bar"}, []string{"歌名", "歌手"}},
		{"dedupe", []string{"歌名", "歌名", "歌手"}, []string{"歌名", "歌手"}},
		{"reorder-not-allowed", []string{"歌手", "歌名"}, []string{"歌手", "歌名"}},
		{"trim", []string{" 歌名 ", "  歌手"}, []string{"歌名", "歌手"}},
		{"all-five", []string{"歌名", "歌手", "专辑", "来源", "音质"}, []string{"歌名", "歌手", "专辑", "来源", "音质"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeOnlineNameTemplate(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("length mismatch: got %v want %v", got, c.want)
			}
			for i, v := range c.want {
				if got[i] != v {
					t.Fatalf("index %d: got %q want %q", i, got[i], v)
				}
			}
		})
	}
}
