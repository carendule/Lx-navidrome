package nativeapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBrowserDownloadProgressResponseIncludesSourceName: 验证 progress
// 响应结构序列化时携带 sourceName 字段（前端依赖此字段显示当前解析源）。
func TestBrowserDownloadProgressResponseIncludesSourceName(t *testing.T) {
	resp := onlineBrowserDownloadProgressResponse{
		TaskID:     "odl_test",
		Status:     "downloading",
		Progress:   25,
		SourceName: "ikun[赞助][永久]",
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"sourceName":"ikun[赞助][永久]"`) {
		t.Fatalf("expected sourceName in response, got %s", out)
	}
	// Decode back to ensure the field name matches the JSON tag.
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["sourceName"] != "ikun[赞助][永久]" {
		t.Errorf("expected sourceName=ikun[赞助][永久], got %v", decoded["sourceName"])
	}
	if decoded["taskId"] != "odl_test" {
		t.Errorf("expected taskId=odl_test, got %v", decoded["taskId"])
	}
	if decoded["status"] != "downloading" {
		t.Errorf("expected status=downloading, got %v", decoded["status"])
	}
}

// TestBrowserDownloadProgressResponseEmptySourceName: 内置源（wy/tx/kg/kw/mg）
// 不会触发自定义脚本，SourceName 留空，序列化为省略（omitempty）。
func TestBrowserDownloadProgressResponseEmptySourceName(t *testing.T) {
	resp := onlineBrowserDownloadProgressResponse{
		TaskID: "odl_test",
		Status: "resolving",
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "sourceName") {
		t.Errorf("expected sourceName to be omitted when empty, got %s", out)
	}
}
