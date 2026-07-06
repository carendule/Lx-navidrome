package nativeapi

import (
	"regexp"
	"strings"
)

// Improved script metadata extraction: parse only from script content,
// without relying on filename.
// This is critical for integrity: filenames can be tampered with, while
// validated script content is authoritative.

// extractMetadataFromCode extracts metadata from script content.
// It only accepts metadata declared in code and does not infer
// metadata from filenames.
func extractMetadataFromCode(script string) scriptMetadata {
	meta := scriptMetadata{}

	// Priority 1: block comment format (/* ... @name ... */)
	// This is the format used by LxServer and standard JSDoc.
	blockCommentMatch := regexp.MustCompile(`(?s)/\*[*!]?([\s\S]*?)\*/`).FindStringSubmatch(script)
	if len(blockCommentMatch) > 1 {
		comment := blockCommentMatch[1]

		// @name - from first @ to line end or next @ marker.
		if nameMatch := regexp.MustCompile(`@name\s+([^\n@]+)`).FindStringSubmatch(comment); len(nameMatch) > 1 {
			if val := strings.TrimSpace(nameMatch[1]); val != "" {
				meta.Name = val
			}
		}

		// @version
		if versionMatch := regexp.MustCompile(`@version\s+([^\n@]+)`).FindStringSubmatch(comment); len(versionMatch) > 1 {
			if val := strings.TrimSpace(versionMatch[1]); val != "" {
				meta.Version = val
			}
		}

		// @author
		if authorMatch := regexp.MustCompile(`@author\s+([^\n@]+)`).FindStringSubmatch(comment); len(authorMatch) > 1 {
			if val := strings.TrimSpace(authorMatch[1]); val != "" {
				meta.Author = val
			}
		}

		// @description
		if descMatch := regexp.MustCompile(`@description\s+([^\n@]+)`).FindStringSubmatch(comment); len(descMatch) > 1 {
			if val := strings.TrimSpace(descMatch[1]); val != "" {
				meta.Description = val
			}
		}

		// @homepage or @repository
		if homeMatch := regexp.MustCompile(`@(?:homepage|repository)\s+([^\n@]+)`).FindStringSubmatch(comment); len(homeMatch) > 1 {
			if val := strings.TrimSpace(homeMatch[1]); val != "" {
				meta.Homepage = val
			}
		}

		// If any metadata is found in block comments, return immediately
		// without scanning line comments.
		if meta.Name != "" || meta.Version != "" || meta.Author != "" {
			return meta
		}
	}

	// Priority 2: multi-line single-line comment format (consecutive // lines).
	// 这是备用方案，如果脚本使用单行注释
	lines := strings.Split(script, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") {
			continue // 只处理注释行
		}

		content := strings.TrimPrefix(trimmed, "//")
		content = strings.TrimSpace(content)

		// @name
		if strings.HasPrefix(content, "@name") {
			if val := strings.TrimSpace(strings.TrimPrefix(content, "@name")); val != "" && meta.Name == "" {
				meta.Name = val
			}
		}

		// @version
		if strings.HasPrefix(content, "@version") {
			if val := strings.TrimSpace(strings.TrimPrefix(content, "@version")); val != "" && meta.Version == "" {
				meta.Version = val
			}
		}

		// @author
		if strings.HasPrefix(content, "@author") {
			if val := strings.TrimSpace(strings.TrimPrefix(content, "@author")); val != "" && meta.Author == "" {
				meta.Author = val
			}
		}
	}

	return meta
}

// getScriptDisplayName 生成脚本的显示名称
// 如果脚本中没有元数据，则使用 fallback（但这不是 "提取" 的元数据）
func getScriptDisplayName(meta scriptMetadata, fallbackFromFilename string) string {
	if meta.Name != "" {
		return meta.Name
	}

	// 如果脚本中没有名字，使用文件名作为显示名称
	// 但这只是用于显示和生成 ID，不是 "元数据"
	if fallbackFromFilename != "" {
		// 移除 .js 扩展名
		name := strings.TrimSuffix(fallbackFromFilename, ".js")
		// 只移除非常不安全的字符
		name = regexp.MustCompile(`[\\/:*?"<>|]+`).ReplaceAllString(name, "_")
		name = strings.TrimSpace(name)
		if name != "" {
			return name
		}
	}

	return "Unknown Source"
}

// isMetadataEmpty 检查元数据是否从脚本中提取（而不是生成）
func isMetadataEmpty(meta scriptMetadata) bool {
	return meta.Name == "" && meta.Version == "" && meta.Author == ""
}

// ValidateScriptStructure 验证脚本结构是否包含必要的元数据和音源定义
// 这是 Phase 1 的改进：基于静态分析而不是实际运行
// 未来 Phase 2 会集成真实的 JS 引擎执行
func ValidateScriptStructure(scriptContent string) (bool, []string, string) {
	// 检查 1: 是否有元数据
	meta := extractMetadataFromCode(scriptContent)
	if isMetadataEmpty(meta) {
		return false, nil, "脚本缺少元数据。请在脚本顶部添加注释: /* @name 脚本名称 @version 版本 */"
	}

	// 检查 2: 是否定义了 sources/apis/qualitys 对象
	sources := extractSupportedSources(scriptContent)
	if len(sources) == 0 {
		return false, nil, "脚本未定义音源。请定义 sources/apis/qualitys 对象，包含 kg/tx/wy/kw/mg 等音源代码。"
	}

	return true, sources, ""
}

// ✅ 迁移指南：
//
// OLD (不推荐):
//   meta := extractMetadataFromFilename("长青SVIP音源v1.2.0.js")
//   // 返回：name="长青SVIP音源", version="v1.2.0" (来自文件名，不可靠)
//
// NEW (推荐):
//   meta := extractMetadataFromCode(scriptContent)
//   // 返回：name="长青SVIP音源", version="1.2.0" (来自脚本代码，可靠)
//   // 如果代码中没有元数据，则 name="" 和 version=""
//
// UI 展示时:
//   displayName := getScriptDisplayName(meta, filename)
//   // 如果 meta.Name 为空，才使用文件名作为 fallback
