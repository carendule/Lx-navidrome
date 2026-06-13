package nativeapi

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestConsoleStubSurvivesShadowing: 验证即使混淆脚本执行
// "this.console = { log: ... }" 覆盖 sandbox.console，Object.defineProperty
// 锁也会让该赋值失败，console.groupEnd() 继续走我们的 Proxy。
func TestConsoleStubSurvivesShadowing(t *testing.T) {
	script := `
		// 模拟 ikun 风格混淆脚本的全局 console 覆盖攻击。
		this.console = { log: function() {} };
		window.console = { log: function() {} };
		globalThis.console = { log: function() {} };
		// 即使有上面三次覆盖，console.groupEnd 仍应可用。
		console.groupEnd();
		console.groupEnd();
		console.groupEnd();
		lx.send('inited', { sources: { wy: {} } });
	`

	executor := NewScriptExecutorWithOptions(5*time.Second, false)
	res := executor.Execute(context.Background(), script)
	if !res.Valid {
		t.Fatalf("expected Valid=true even after console shadowing, got false; Error=%q", res.Error)
	}
}

// TestConsoleStubSurvivesShadowingStrict: 同样验证 'use strict' 模式下。
func TestConsoleStubSurvivesShadowingStrict(t *testing.T) {
	script := `
		"use strict";
		this.console = { log: function() {} };
		console.groupEnd();
		lx.send('inited', { sources: { tx: {} } });
	`

	executor := NewScriptExecutorWithOptions(5*time.Second, false)
	res := executor.Execute(context.Background(), script)
	if !res.Valid {
		t.Fatalf("expected Valid=true (strict mode), got false; Error=%q", res.Error)
	}
}

// TestConsoleMethodStubsDoNotThrow: 验证一个调用 group/groupEnd/table/trace
// 等"非核心"console 方法的脚本不会因为方法缺失而失败。
func TestConsoleMethodStubsDoNotThrow(t *testing.T) {
	script := `
		// Hit every non-core console method that's commonly used in lx-music style
		// scripts. None of them should throw, even though the sandbox has no
		// real console implementation.
		console.group('group-label');
		console.groupCollapsed('group-collapsed-label');
		console.groupEnd();
		console.trace('trace message');
		console.assert(true, 'this is fine');
		console.count('counter');
		console.countReset('counter');
		console.dir({ a: 1 });
		console.dirxml('<root/>');
		console.table([{ a: 1 }, { a: 2 }]);
		console.timeLog('t');
		console.timeStamp('t');
		console.profile('p');
		console.profileEnd('p');

		lx.send('inited', { sources: { wy: {}, tx: {} } });
	`

	executor := NewScriptExecutorWithOptions(5*time.Second, false)
	res := executor.Execute(context.Background(), script)
	if !res.Valid {
		t.Fatalf("expected Valid=true, got false; Error=%q", res.Error)
	}
	if len(res.Sources) != 2 {
		t.Errorf("expected 2 sources, got %d (%v)", len(res.Sources), res.Sources)
	}
	if res.Error != "" {
		t.Errorf("expected empty Error, got %q", res.Error)
	}
}

// TestGojaSandboxConsoleStubs: 验证 goja 兜底 sandbox（非 Node）路径下
// 同样不抛错（不依赖 node 二进制）。
func TestGojaSandboxConsoleStubs(t *testing.T) {
	script := `
		console.group('g');
		console.groupEnd();
		console.table([{ x: 1 }]);
		lx.send('inited', { sources: { kg: {} } });
	`

	// We can't directly call executeWithGoja, but we can force the goja
	// path by making the Node binary "unavailable" — easiest: just call
	// executeWithGoja directly with our own context.
	se := NewScriptExecutorWithOptions(5*time.Second, false)
	res := se.executeWithGoja(context.Background(), script)
	if !res.Valid {
		t.Fatalf("goja: expected Valid=true, got false; Error=%q", res.Error)
	}
	if !strings.Contains(strings.Join(res.Sources, ","), "kg") {
		t.Errorf("goja: expected 'kg' in sources, got %v", res.Sources)
	}
}
