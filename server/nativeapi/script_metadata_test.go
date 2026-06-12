package nativeapi

import (
	"testing"
)

// TestExtractMetadataFromCode 测试从脚本代码中提取元数据
func TestExtractMetadataFromCode(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		expected scriptMetadata
		wantName string
		wantVer  string
	}{
		{
			name: "JSDoc 块注释格式（标准格式）",
			script: `/*!
 * @name 长青SVIP音源
 * @version 1.2.0
 * @author SVIP
 * @description 高质量音乐源
 * @homepage https://example.com
 */
const sources = {kg: {}, tx: {}};
lx.send('inited', {sources});`,
			wantName: "长青SVIP音源",
			wantVer:  "1.2.0",
		},
		{
			name: "JSDoc 块注释 (** 格式)",
			script: `/**
 * @name 测试音源
 * @version 2.0.0
 * @author TestAuthor
 */
const sources = {kg: {}};
lx.send('inited', {sources});`,
			wantName: "测试音源",
			wantVer:  "2.0.0",
		},
		{
			name: "混淆代码但保留元数据",
			script: `/*! @name 混淆音源 @version 1.0.0 @author Obfuscated */
var _0x1234=['sources','kg','tx'];
var sources={kg:{url:'...'},tx:{url:'...'}};
lx.send('inited',{sources:sources});`,
			wantName: "混淆音源",
			wantVer:  "1.0.0",
		},
		{
			name: "无元数据（应返回空值）",
			script: `const sources = {kg: {}, tx: {}};
lx.send('inited', {sources});`,
			wantName: "",
			wantVer:  "",
		},
		{
			name: "只有部分元数据",
			script: `/*
 * @name 部分元数据
 */
const sources = {kg: {}};
lx.send('inited', {sources});`,
			wantName: "部分元数据",
			wantVer:  "",
		},
		{
			name: "单行注释格式（备用）",
			script: `// @name 单行音源
// @version 1.5.0
const sources = {kg: {}};`,
			wantName: "单行音源",
			wantVer:  "1.5.0",
		},
		{
			name: "块注释优先级高于单行注释",
			script: `/*!
 * @name 块注释音源
 * @version 2.0.0
 */
// @name 单行音源 (这行会被忽略)
const sources = {kg: {}};`,
			wantName: "块注释音源",
			wantVer:  "2.0.0",
		},
		{
			name: "中文元数据正确处理",
			script: `/*!
 * @name 网易官方接口 
 * @author 开发者
 * @version v3.1.0
 */
const sources = {kg: {}};`,
			wantName: "网易官方接口",
			wantVer:  "v3.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMetadataFromCode(tt.script)
			if result.Name != tt.wantName {
				t.Errorf("Name: got %q, want %q", result.Name, tt.wantName)
			}
			if result.Version != tt.wantVer {
				t.Errorf("Version: got %q, want %q", result.Version, tt.wantVer)
			}
		})
	}
}

// TestGetScriptDisplayName 测试脚本显示名称生成
func TestGetScriptDisplayName(t *testing.T) {
	tests := []struct {
		name             string
		meta             scriptMetadata
		fallbackFilename string
		expected         string
	}{
		{
			name: "有脚本元数据时使用元数据",
			meta: scriptMetadata{
				Name: "长青SVIP音源",
			},
			fallbackFilename: "anything.js",
			expected:         "长青SVIP音源",
		},
		{
			name: "无脚本元数据时使用文件名",
			meta: scriptMetadata{
				Name: "",
			},
			fallbackFilename: "test_source.js",
			expected:         "test_source",
		},
		{
			name: "文件名中移除特殊字符",
			meta: scriptMetadata{
				Name: "",
			},
			fallbackFilename: "Test-Source_v1.2.0.js",
			expected:         "Test-Source_v1.2.0",
		},
		{
			name: "中文文件名保留",
			meta: scriptMetadata{
				Name: "",
			},
			fallbackFilename: "长青SVIP音源v1.2.0.js",
			expected:         "长青SVIP音源v1.2.0",
		},
		{
			name: "两者都为空时使用默认",
			meta: scriptMetadata{
				Name: "",
			},
			fallbackFilename: "",
			expected:         "Unknown Source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getScriptDisplayName(tt.meta, tt.fallbackFilename)
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestValidateScriptStructure 测试脚本结构验证
func TestValidateScriptStructure(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		shouldValid bool
		errorMsg    string
	}{
		{
			name: "完整有效的脚本",
			script: `/*!
 * @name 测试音源
 * @version 1.0.0
 */
const sources = {kg: {}, tx: {}};
lx.send('inited', {sources});`,
			shouldValid: true,
		},
		{
			name: "缺少元数据",
			script: `const sources = {kg: {}};
lx.send('inited', {sources});`,
			shouldValid: false,
			errorMsg:    "缺少元数据",
		},
		{
			name: "没有定义任何音源",
			script: `/*!
 * @name 空脚本
 * @version 1.0.0
 */
// 没有 sources 定义`,
			shouldValid: false,
			errorMsg:    "未定义音源",
		},
		{
			name: "只有名字，没有音源",
			script: `/*! @name 不完整 */
// 空脚本`,
			shouldValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, sources, errMsg := ValidateScriptStructure(tt.script)
			if valid != tt.shouldValid {
				t.Errorf("Valid: got %v, want %v", valid, tt.shouldValid)
			}
			if !tt.shouldValid && errMsg == "" {
				t.Errorf("Error message should not be empty for invalid script")
			}
			if tt.shouldValid && len(sources) == 0 {
				t.Errorf("Valid script should have sources")
			}
		})
	}
}

// TestIsMetadataEmpty 测试元数据是否为空
func TestIsMetadataEmpty(t *testing.T) {
	tests := []struct {
		name     string
		meta     scriptMetadata
		expected bool
	}{
		{
			name:     "完全空的元数据",
			meta:     scriptMetadata{},
			expected: true,
		},
		{
			name: "只有描述不算非空",
			meta: scriptMetadata{
				Description: "有描述",
			},
			expected: true,
		},
		{
			name: "有名字",
			meta: scriptMetadata{
				Name: "脚本",
			},
			expected: false,
		},
		{
			name: "有版本",
			meta: scriptMetadata{
				Version: "1.0.0",
			},
			expected: false,
		},
		{
			name: "有作者",
			meta: scriptMetadata{
				Author: "张三",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isMetadataEmpty(tt.meta)
			if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestRealWorldScripts 实际脚本示例
func TestRealWorldScripts(t *testing.T) {
	// 模拟真实的 lxmusic 脚本格式
	lxmusicScript := `/*!
 * @name LX Music Source
 * @version 3.0.0
 * @author LX Team
 * @description A real music source plugin
 * @homepage https://github.com/lyswhut/lx-music-desktop
 */

const sources = {
  kg: { search: true, quality: true },
  tx: { search: true, quality: true },
  wy: { search: true, quality: true },
  kw: { search: true, quality: true },
  mg: { search: true, quality: true }
};

lx.send('inited', { sources });

lx.on('request', async (data) => {
  const { action, source, info } = data;
  // ... search implementation
});`

	meta := extractMetadataFromCode(lxmusicScript)
	if meta.Name != "LX Music Source" {
		t.Errorf("Expected 'LX Music Source', got %q", meta.Name)
	}
	if meta.Version != "3.0.0" {
		t.Errorf("Expected '3.0.0', got %q", meta.Version)
	}

	valid, sources, _ := ValidateScriptStructure(lxmusicScript)
	if !valid {
		t.Error("Real script should be valid")
	}
	if len(sources) != 5 {
		t.Errorf("Expected 5 sources, got %d", len(sources))
	}
}
