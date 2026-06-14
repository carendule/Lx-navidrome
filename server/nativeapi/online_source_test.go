package nativeapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadOnlineSourceSettingsDefaultsEmbedModeToMetadata(t *testing.T) {
	// Fresh install: no settings file on disk. The default
	// constructor must set EmbedMode=embedModeMetadata so the
	// user's first download after install still gets cover /
	// tags (no lyric until the user opts into "all" via the
	// settings panel).
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
		t.Fatalf("loadOnlineSourceSettings: %v", err)
	}
	if settings.EmbedMode != defaultOnlineEmbedMode {
		t.Fatalf("expected EmbedMode=%q on fresh install, got %q", defaultOnlineEmbedMode, settings.EmbedMode)
	}
}

// TestSaveOnlineSourceSettingsPreservesEmbedModeWhenOmitted is
// the regression test for the production bug the user reported
// via navidrome.log: every time the user clicked "保存下载路径" the
// settings file was rewritten with embedMetadata=false (Go's zero
// value) because the handler constructed a fresh struct without
// reading the existing on-disk value. After the 3-state refactor
// the same defense protects EmbedMode: the save handler must
// preserve whatever mode the user previously chose when the
// frontend form payload omits the field.
func TestSaveOnlineSourceSettingsPreservesEmbedModeWhenOmitted(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	// Pre-seed settings with EmbedMode=embedModeNone via the
	// low-level helper (not the HTTP handler). The exact scenario
	// we want to defend: user picked "不嵌入", then clicks
	// 保存下载路径, and we want their next download to still
	// skip embed.
	if err := saveOnlineSourceSettings(onlineSourceSettings{
		DownloadPath: filepath.Join(tmpDir, "downloads"),
		NameTemplate: []string{"歌名", "歌手"},
		EmbedMode:    embedModeNone,
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate the frontend POST by going through the HTTP
	// handler. The frontend only sends downloadPath +
	// nameTemplate, never embedMode.
	api := &Router{}
	handler := api.saveOnlineSourceSettings
	body := `{"downloadPath":"` + filepath.Join(tmpDir, "downloads-new") + `","nameTemplate":["歌名","歌手","专辑"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/online/source/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save handler returned %d: %s", rec.Code, rec.Body.String())
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.EmbedMode != embedModeNone {
		t.Fatalf("saveOnlineSourceSettings with omitted embedMode clobbered the on-disk value: got %q want %q", settings.EmbedMode, embedModeNone)
	}
}

// TestSaveOnlineSourceSettingsFreshInstallDefaultsEmbedMode covers
// the fresh-install case: a user with no settings.json clicks save
// for the first time, the request omits EmbedMode, and we should
// default to embedModeMetadata so the embed pipeline runs on their
// first download.
func TestSaveOnlineSourceSettingsFreshInstallDefaultsEmbedMode(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	// First save — no prior settings on disk.
	if err := saveOnlineSourceSettings(onlineSourceSettings{
		DownloadPath: filepath.Join(tmpDir, "downloads"),
		NameTemplate: []string{"歌名", "歌手"},
	}); err != nil {
		t.Fatal(err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.EmbedMode != defaultOnlineEmbedMode {
		t.Fatalf("first save should default EmbedMode=%q (fresh install), got %q", defaultOnlineEmbedMode, settings.EmbedMode)
	}
}

// TestLoadOnlineSourceSettingsOldFileUpgradesEmbedMetadataToEmbedMode
// is the regression test for the schema migration. Settings files
// written by older versions of the server carry a boolean
// "embedMetadata" field (true / false) but no "embedMode" string.
// The load path must translate true -> "all" and false -> "none"
// so the user's explicit choice survives the upgrade, and the
// file is rewritten with the new field so the next load doesn't
// have to migrate again.
func TestLoadOnlineSourceSettingsOldFileUpgradesEmbedMetadataToEmbedMode(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	// Write a settings.json that mirrors the legacy schema:
	// embedMetadata=true, no embedMode field.
	legacySettings := `{
  "downloadPath": "` + filepath.Join(tmpDir, "downloads") + `",
  "nameTemplate": ["歌名", "歌手"],
  "embedMetadata": true
}`
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onlineSettingsPath(), []byte(legacySettings), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings: %v", err)
	}
	if settings.EmbedMode != embedModeAll {
		t.Fatalf("legacy embedMetadata=true should upgrade to EmbedMode=%q, got %q", embedModeAll, settings.EmbedMode)
	}

	// Confirm the file got rewritten with the new schema so
	// future loads skip the legacy branch.
	b, rErr := os.ReadFile(onlineSettingsPath())
	if rErr != nil {
		t.Fatalf("read upgraded settings: %v", rErr)
	}
	if !bytes.Contains(b, []byte("embedMode")) {
		t.Fatalf("expected upgraded settings.json to carry the embedMode field, got: %s", string(b))
	}
}

// TestLoadOnlineSourceSettingsLegacyEmbedMetadataFalseUpgradesToNone
// is the inverse of the previous test: the user explicitly
// disabled embedding in the legacy UI, and that opt-out must
// survive the schema migration as EmbedMode="none". We don't
// rewrite the on-disk file in this branch — the load path only
// persists the migration when the legacy field was the only
// signal we had.
func TestLoadOnlineSourceSettingsLegacyEmbedMetadataFalseUpgradesToNone(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	legacySettings := `{
  "downloadPath": "` + filepath.Join(tmpDir, "downloads") + `",
  "nameTemplate": ["歌名", "歌手"],
  "embedMetadata": false
}`
	if err := os.MkdirAll(onlineSourcesRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onlineSettingsPath(), []byte(legacySettings), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		t.Fatalf("loadOnlineSourceSettings: %v", err)
	}
	if settings.EmbedMode != embedModeNone {
		t.Fatalf("legacy embedMetadata=false should upgrade to EmbedMode=%q, got %q", embedModeNone, settings.EmbedMode)
	}
}

// TestSaveOnlineSourceSettingsRoundTripsEmbedMode walks each
// legal EmbedMode through save + load and asserts it survives
// unmodified. This is the round-trip contract the UI relies on:
// when the user picks "不嵌入" / "仅嵌入元数据" / "嵌入元数据和歌词"
// on the settings panel, reloads, the same value should come
// back. It also guards against a save handler that re-defaults
// the field on every write.
func TestSaveOnlineSourceSettingsRoundTripsEmbedMode(t *testing.T) {
	oldDataFolder := conf.Server.DataFolder
	oldMusicFolder := conf.Server.MusicFolder
	tmpDir := t.TempDir()
	conf.Server.DataFolder = conf.NewDir(tmpDir)
	conf.Server.MusicFolder = "/music/default"
	defer func() {
		conf.Server.DataFolder = oldDataFolder
		conf.Server.MusicFolder = oldMusicFolder
	}()

	cases := []struct {
		name string
		mode string
	}{
		{"none", embedModeNone},
		{"metadata", embedModeMetadata},
		{"all", embedModeAll},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := saveOnlineSourceSettings(onlineSourceSettings{
				DownloadPath: filepath.Join(tmpDir, "downloads"),
				NameTemplate: []string{"歌名", "歌手"},
				EmbedMode:    c.mode,
			}); err != nil {
				t.Fatalf("save: %v", err)
			}
			settings, err := loadOnlineSourceSettings()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if settings.EmbedMode != c.mode {
				t.Fatalf("EmbedMode round-trip mismatch: wrote %q, read %q", c.mode, settings.EmbedMode)
			}
		})
	}
}

// TestSanitizeEmbedMode is a focused unit test for the helper
// that gates the EmbedMode field. We expect:
//
//   - "none" / "metadata" / "all" pass through (case-insensitive).
//   - "" (zero value from json.Unmarshal of a missing field) and
//     typos / unknown values fall back to the default
//     embedModeMetadata so a hand-edited settings.json can't
//     disable embedding silently.
func TestSanitizeEmbedMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{embedModeNone, embedModeNone},
		{embedModeMetadata, embedModeMetadata},
		{embedModeAll, embedModeAll},
		{"NONE", embedModeNone},
		{" Metadata ", embedModeMetadata},
		{"aLL", embedModeAll},
		{"", defaultOnlineEmbedMode},
		{"bogus", defaultOnlineEmbedMode},
		{"true", defaultOnlineEmbedMode}, // legacy bool-as-string rejected
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := sanitizeEmbedMode(c.in); got != c.want {
				t.Fatalf("sanitizeEmbedMode(%q) = %q, want %q", c.in, got, c.want)
			}
		})
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
