package nativeapi

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// ScriptExecutor handles safe execution of user scripts in a sandbox environment
type ScriptExecutor struct {
	timeout       time.Duration
	allowUnsafeVM bool
}

// ScriptExecutionResult contains the results of script execution
type ScriptExecutionResult struct {
	Valid             bool
	Sources           []string
	Error             string
	RequireUnsafe     bool
	InitCallback      bool
	RequestCallback   bool
	InitCalledSources []string
}

type nodeScriptExecutionResult struct {
	Valid           bool     `json:"valid"`
	Sources         []string `json:"sources"`
	Error           string   `json:"error"`
	InitCallback    bool     `json:"initCallback"`
	RequestCallback bool     `json:"requestCallback"`
}

const scriptExecutorBootstrap = `
(function() {
	var __global = this;
	if (!__global.global) __global.global = __global;
	if (!__global.window) __global.window = __global;
	if (!__global.globalThis) __global.globalThis = __global;

	if (!__global.console) {
		__global.console = {};
	}
	['log', 'info', 'warn', 'error', 'debug', 'time', 'timeEnd'].forEach(function(name) {
		if (typeof __global.console[name] !== 'function') __global.console[name] = function() {};
	});

	if (typeof __global.setTimeout !== 'function') {
		__global.setTimeout = function(fn) {
			if (typeof fn === 'function') fn();
			return 1;
		};
	}
	if (typeof __global.clearTimeout !== 'function') {
		__global.clearTimeout = function() {};
	}
	if (typeof __global.setInterval !== 'function') {
		__global.setInterval = function(fn) {
			if (typeof fn === 'function') fn();
			return 1;
		};
	}
	if (typeof __global.clearInterval !== 'function') {
		__global.clearInterval = function() {};
	}

	if (!__global.process) {
		__global.process = { env: { NODE_ENV: 'production' } };
	}
	if (typeof __global.process.nextTick !== 'function') {
		__global.process.nextTick = function(fn) {
			if (typeof fn === 'function') fn();
		};
	}

	if (typeof __global.atob !== 'function') {
		__global.atob = function(s) { return s; };
	}
	if (typeof __global.btoa !== 'function') {
		__global.btoa = function(s) { return s; };
	}

	if (!__global.module) __global.module = { exports: {} };
	if (!__global.exports) __global.exports = __global.module.exports;
	if (typeof __global.require !== 'function') {
		__global.require = function() { return {}; };
	}

	if (typeof __global.TextEncoder !== 'function') {
		__global.TextEncoder = function TextEncoder() {};
		__global.TextEncoder.prototype.encode = function(str) {
			str = String(str == null ? '' : str);
			var out = [];
			for (var i = 0; i < str.length; i++) out.push(str.charCodeAt(i) & 255);
			return out;
		};
	}
	if (typeof __global.TextDecoder !== 'function') {
		__global.TextDecoder = function TextDecoder() {};
		__global.TextDecoder.prototype.decode = function(input) {
			if (!input || typeof input.length !== 'number') return '';
			var out = '';
			for (var i = 0; i < input.length; i++) out += String.fromCharCode(input[i]);
			return out;
		};
	}

	if (!__global.URL) {
		__global.URL = function URL(value) { this.href = String(value || ''); };
	}
	if (!__global.URLSearchParams) {
		__global.URLSearchParams = function URLSearchParams() {};
	}
})();
`

const nodeValidationBootstrap = `
const vm = require('vm');
const crypto = require('crypto');
const zlib = require('zlib');
const vmTimeout = Number(process.env.ND_VM_TIMEOUT_MS || '5000');
const initTimeoutMs = Number(process.env.ND_INIT_TIMEOUT_MS || '3000');
const allowUnsafeVM = process.env.ND_UNSAFE_VM === 'true';

let script = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => { script += chunk; });
process.stdin.on('end', async () => {
	let registeredSources = {};
	let requestCallback = false;
	let initResolve;
	let initReject;
	const initPromise = new Promise((resolve, reject) => {
		initResolve = resolve;
		initReject = reject;
	});

	const decontextify = (data) => {
		try {
			return JSON.parse(JSON.stringify(data));
		} catch (_) {
			return data;
		}
	};

	const lxUtils = {
		buffer: {
			from: (d, e) => Buffer.from(d, e),
			bufToString: (b, f) => Buffer.isBuffer(b) ? b.toString(f) : Buffer.from(b, 'binary').toString(f),
		},
		crypto: {
			md5: (str) => crypto.createHash('md5').update(String(str ?? '')).digest('hex'),
			aesEncrypt: (buffer) => buffer,
			aesDecrypt: (buffer) => buffer,
			rsaEncrypt: (buffer) => buffer,
			randomBytes: (size) => crypto.randomBytes(size),
		},
		zlib: {
			inflate: (buffer) => zlib.inflateSync(Buffer.from(buffer)),
			deflate: (buffer) => zlib.deflateSync(Buffer.from(buffer)),
		},
	};

	const lxObject = {
		version: '2.0.0',
		env: 'desktop',
		platform: 'web',
		EVENT_NAMES: {
			request: 'request',
			inited: 'inited',
			updateAlert: 'updateAlert',
		},
		utils: lxUtils,
		request: (url, options, callback) => {
			const resp = { status: 200, statusText: 'OK', data: '', body: '', headers: {} };
			if (typeof callback === 'function') callback(resp);
			return Promise.resolve(resp);
		},
		send: (eventName, data) => {
			const dData = decontextify(data);
			if (eventName === 'inited') {
				if (dData && dData.sources) registeredSources = dData.sources;
				if (initResolve) initResolve();
			} else if (eventName === 'updateAlert') {
				if (initReject) initReject(new Error('发现新版本,需要更新'));
			}
		},
		on: (eventName, handler) => {
			if (eventName === 'request' && typeof handler === 'function') requestCallback = true;
		},
	};

	const sandbox = {
		console: allowUnsafeVM ? console : { log() {}, info() {}, warn() {}, error() {}, debug() {}, time() {}, timeEnd() {} },
		setTimeout,
		clearTimeout,
		setInterval,
		clearInterval,
		Buffer,
		URL,
		URLSearchParams,
		TextEncoder,
		TextDecoder,
		process: allowUnsafeVM ? process : {
			nextTick: (fn, ...args) => setTimeout(() => fn(...args), 0),
			env: { NODE_ENV: process.env.NODE_ENV || 'production' },
		},
		lx: lxObject,
		global: null,
		window: null,
		globalThis: null,
		atob: (s) => Buffer.from(s, 'base64').toString('binary'),
		btoa: (s) => Buffer.from(s, 'binary').toString('base64'),
		crypto,
		module: { exports: {} },
		exports: {},
		require: allowUnsafeVM ? require : () => { throw new Error('REQUIRE_UNSAFE_VM'); },
	};
	sandbox.global = sandbox;
	sandbox.window = sandbox;
	sandbox.globalThis = sandbox;

	try {
		vm.runInContext(script, vm.createContext(sandbox), {
			filename: 'custom_source_validation.js',
			timeout: vmTimeout,
		});

		let initTimer;
		try {
			await Promise.race([
				initPromise,
				new Promise((_, reject) => {
					initTimer = setTimeout(() => reject(new Error('初始化超时，请确保脚本调用了 lx.send("inited", ...)')), initTimeoutMs);
				}),
			]);
		} finally {
			if (initTimer) clearTimeout(initTimer);
		}

		const result = {
			valid: Object.keys(registeredSources || {}).length > 0,
			sources: Object.keys(registeredSources || {}),
			error: Object.keys(registeredSources || {}).length > 0 ? '' : 'Script init callback did not contain any sources',
			initCallback: true,
			requestCallback,
		};
		process.stdout.write(JSON.stringify(result));
	} catch (err) {
		process.stdout.write(JSON.stringify({
			valid: false,
			sources: [],
			error: err && err.message ? err.message : String(err),
			initCallback: false,
			requestCallback,
		}));
	}
});
`

// NewScriptExecutor creates a new script executor with the specified timeout
func NewScriptExecutor(timeout time.Duration) *ScriptExecutor {
	return NewScriptExecutorWithOptions(timeout, false)
}

// NewScriptExecutorWithOptions creates a script executor with explicit VM mode options.
func NewScriptExecutorWithOptions(timeout time.Duration, allowUnsafeVM bool) *ScriptExecutor {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &ScriptExecutor{timeout: timeout, allowUnsafeVM: allowUnsafeVM}
}

// Execute runs the script and extracts metadata about it
func (se *ScriptExecutor) Execute(ctx context.Context, scriptContent string) *ScriptExecutionResult {
	if result, err := se.executeWithNodeVM(ctx, scriptContent); err == nil {
		return result
	} else if !errors.Is(err, exec.ErrNotFound) {
		return &ScriptExecutionResult{
			Valid:   false,
			Sources: []string{},
			Error:   fmt.Sprintf("Script execution error: %v", err),
		}
	}

	return se.executeWithGoja(ctx, scriptContent)
}

func (se *ScriptExecutor) executeWithNodeVM(ctx context.Context, scriptContent string) (*ScriptExecutionResult, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return nil, exec.ErrNotFound
	}

	cmd := exec.CommandContext(ctx, "node", "-e", nodeValidationBootstrap)
	cmd.Stdin = bytes.NewBufferString(scriptContent)
	cmd.Env = append(execEnv(),
		fmt.Sprintf("ND_VM_TIMEOUT_MS=%d", se.timeout.Milliseconds()),
		"ND_INIT_TIMEOUT_MS=3000",
		fmt.Sprintf("ND_UNSAFE_VM=%t", se.allowUnsafeVM),
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("node vm failed: %s", bytes.TrimSpace(stderr.Bytes()))
		}
		return nil, err
	}

	var nodeResult nodeScriptExecutionResult
	if err := json.Unmarshal(stdout.Bytes(), &nodeResult); err != nil {
		return nil, fmt.Errorf("invalid node vm response: %w", err)
	}
	if nodeResult.Error != "" && bytes.Contains([]byte(nodeResult.Error), []byte("timed out")) {
		nodeResult.Error = fmt.Sprintf("Script execution timeout (limit: %v)", se.timeout)
	}
	if !nodeResult.Valid && !se.allowUnsafeVM && isUnsafeVMRequiredError(nodeResult.Error) {
		return &ScriptExecutionResult{
			Valid:         false,
			Sources:       []string{},
			Error:         "Script requires native VM mode to complete initialization",
			RequireUnsafe: true,
		}, nil
	}

	return &ScriptExecutionResult{
		Valid:             nodeResult.Valid,
		Sources:           nodeResult.Sources,
		Error:             nodeResult.Error,
		RequireUnsafe:     false,
		InitCallback:      nodeResult.InitCallback,
		RequestCallback:   nodeResult.RequestCallback,
		InitCalledSources: nodeResult.Sources,
	}, nil
}

func isUnsafeVMRequiredError(errMsg string) bool {
	errMsg = strings.TrimSpace(errMsg)
	if errMsg == "" {
		return false
	}
	patterns := []string{
		"REQUIRE_UNSAFE_VM",
		"contextified object",
		"Operation not allowed",
	}
	for _, pattern := range patterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}

func execEnv() []string {
	return os.Environ()
}

// nolint:gocyclo
func (se *ScriptExecutor) executeWithGoja(ctx context.Context, scriptContent string) *ScriptExecutionResult {
	result := &ScriptExecutionResult{
		Valid:             false,
		InitCalledSources: []string{},
		RequestCallback:   false,
		InitCallback:      false,
	}

	// Create a new VM for this execution
	vm := goja.New()
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())

	// lx.on(event, callback) - register event handlers
	lx := vm.NewObject()

	// lx.send(event, data) - used to report initialization and send results
	sendFunc := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			return goja.Undefined()
		}

		eventArg := call.Arguments[0]
		dataArg := call.Arguments[1]

		event := eventArg.String()
		if event == "inited" {
			result.InitCallback = true

			// Extract sources from the data object
			if dataObj, ok := dataArg.(*goja.Object); ok {
				sourcesVal := dataObj.Get("sources")
				if sourcesVal != nil && !goja.IsUndefined(sourcesVal) {
					if sourcesObj, ok := sourcesVal.(*goja.Object); ok {
						result.InitCalledSources = extractSourcesFromVM(sourcesObj)
					}
				}
			}
		}

		return goja.Undefined()
	}

	// lx.on(event, callback) - register event handlers
	onFunc := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			return goja.Undefined()
		}

		eventArg := call.Arguments[0]
		callbackArg := call.Arguments[1]

		event := eventArg.String()
		if event == "request" {
			// Check if the argument is actually a function
			_, isFunc := goja.AssertFunction(callbackArg)
			if isFunc {
				result.RequestCallback = true
			}
		}

		return goja.Undefined()
	}

	// lx.request(url, options, callback) - mock HTTP request
	requestFunc := func(call goja.FunctionCall) goja.Value {
		resp := vm.NewObject()
		_ = resp.Set("status", 200)
		_ = resp.Set("statusText", "OK")
		_ = resp.Set("data", "")
		_ = resp.Set("body", "")
		_ = resp.Set("headers", vm.NewObject())

		if len(call.Arguments) >= 3 {
			if callback, ok := goja.AssertFunction(call.Arguments[2]); ok {
				_, _ = callback(goja.Undefined(), resp)
			}
		}
		return resp
	}

	// lx.utils - utility functions
	utils := vm.NewObject()

	// crypto utilities
	crypto := vm.NewObject()
	_ = crypto.Set("md5", func(call goja.FunctionCall) goja.Value {
		input := ""
		if len(call.Arguments) > 0 {
			input = call.Arguments[0].String()
		}
		sum := md5.Sum([]byte(input))
		return vm.ToValue(fmt.Sprintf("%x", sum))
	})
	_ = crypto.Set("aesEncrypt", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		return call.Arguments[0]
	})
	_ = crypto.Set("aesDecrypt", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		return call.Arguments[0]
	})
	_ = crypto.Set("randomBytes", func(call goja.FunctionCall) goja.Value {
		size := 0
		if len(call.Arguments) > 0 {
			size = int(call.Arguments[0].ToInteger())
		}
		if size < 0 {
			size = 0
		}
		buf := make([]byte, size)
		return vm.ToValue(buf)
	})
	_ = utils.Set("crypto", crypto)

	// zlib utilities
	zlib := vm.NewObject()
	_ = zlib.Set("inflate", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		return call.Arguments[0]
	})
	_ = zlib.Set("deflate", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		return call.Arguments[0]
	})
	_ = utils.Set("zlib", zlib)

	buffer := vm.NewObject()
	_ = buffer.Set("from", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		return vm.ToValue(call.Arguments[0].String())
	})
	_ = buffer.Set("bufToString", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		return vm.ToValue(call.Arguments[0].String())
	})
	_ = utils.Set("buffer", buffer)

	_ = lx.Set("version", "2.0.0")
	_ = lx.Set("env", "desktop")
	_ = lx.Set("platform", "web")
	_ = lx.Set("EVENT_NAMES", map[string]string{
		"request":     "request",
		"inited":      "inited",
		"updateAlert": "updateAlert",
	})

	_ = lx.Set("send", sendFunc)
	_ = lx.Set("on", onFunc)
	_ = lx.Set("request", requestFunc)
	_ = lx.Set("utils", utils)

	// Set the lx global
	_ = vm.Set("lx", lx)

	console := vm.NewObject()
	_ = console.Set("log", func(args ...interface{}) {})
	_ = console.Set("info", func(args ...interface{}) {})
	_ = console.Set("warn", func(args ...interface{}) {})
	_ = console.Set("error", func(args ...interface{}) {})
	_ = console.Set("debug", func(args ...interface{}) {})
	_ = console.Set("time", func(args ...interface{}) {})
	_ = console.Set("timeEnd", func(args ...interface{}) {})
	_ = vm.Set("console", console)

	_ = vm.Set("setTimeout", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			if callback, ok := goja.AssertFunction(call.Arguments[0]); ok {
				_, _ = callback(goja.Undefined())
			}
		}
		return vm.ToValue(1)
	})
	_ = vm.Set("clearTimeout", func(call goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = vm.Set("setInterval", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			if callback, ok := goja.AssertFunction(call.Arguments[0]); ok {
				_, _ = callback(goja.Undefined())
			}
		}
		return vm.ToValue(1)
	})
	_ = vm.Set("clearInterval", func(call goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = vm.Set("atob", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		decoded, err := base64.StdEncoding.DecodeString(call.Arguments[0].String())
		if err != nil {
			return vm.ToValue(call.Arguments[0].String())
		}
		return vm.ToValue(string(decoded))
	})
	_ = vm.Set("btoa", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		return vm.ToValue(base64.StdEncoding.EncodeToString([]byte(call.Arguments[0].String())))
	})
	_ = vm.Set("process", map[string]interface{}{
		"env": map[string]string{"NODE_ENV": "production"},
		"nextTick": func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) > 0 {
				if callback, ok := goja.AssertFunction(call.Arguments[0]); ok {
					_, _ = callback(goja.Undefined())
				}
			}
			return goja.Undefined()
		},
	})
	_ = vm.Set("global", vm.GlobalObject())
	_ = vm.Set("window", vm.GlobalObject())
	_ = vm.Set("globalThis", vm.GlobalObject())
	_ = vm.Set("module", map[string]interface{}{"exports": map[string]interface{}{}})
	_ = vm.Set("exports", map[string]interface{}{})
	_ = vm.Set("require", func(call goja.FunctionCall) goja.Value { return vm.ToValue(map[string]interface{}{}) })
	_ = vm.Set("URL", func(call goja.ConstructorCall) *goja.Object {
		obj := call.This
		href := ""
		if len(call.Arguments) > 0 {
			href = call.Arguments[0].String()
		}
		_ = obj.Set("href", href)
		return obj
	})
	_ = vm.Set("URLSearchParams", func(call goja.ConstructorCall) *goja.Object {
		return call.This
	})
	_ = vm.Set("TextEncoder", func(call goja.ConstructorCall) *goja.Object {
		obj := call.This
		_ = obj.Set("encode", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) == 0 {
				return vm.ToValue([]int{})
			}
			input := call.Arguments[0].String()
			out := make([]int, 0, len(input))
			for _, ch := range []byte(input) {
				out = append(out, int(ch))
			}
			return vm.ToValue(out)
		})
		return obj
	})
	_ = vm.Set("TextDecoder", func(call goja.ConstructorCall) *goja.Object {
		obj := call.This
		_ = obj.Set("decode", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) == 0 {
				return vm.ToValue("")
			}
			return vm.ToValue(call.Arguments[0].String())
		})
		return obj
	})
	_ = vm.Set("Buffer", map[string]interface{}{
		"from": func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) == 0 {
				return vm.ToValue("")
			}
			return vm.ToValue(call.Arguments[0].String())
		},
		"isBuffer": func(call goja.FunctionCall) goja.Value { return vm.ToValue(false) },
	})

	if _, err := vm.RunString(scriptExecutorBootstrap); err != nil {
		result.Error = fmt.Sprintf("Script bootstrap error: %v", err)
		return result
	}

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(ctx, se.timeout)
	defer cancel()

	// Run the script with timeout
	doneChan := make(chan error, 1)
	go func() {
		_, err := vm.RunString(scriptContent)
		doneChan <- err
	}()

	// Wait for completion or timeout
	select {
	case err := <-doneChan:
		if err != nil {
			result.Error = fmt.Sprintf("Script execution error: %v", err)
			return result
		}

		// Execution successful
		// Check if init callback was called
		if !result.InitCallback {
			result.Error = "Script did not call lx.send('inited', ...)"
			return result
		}

		// Extract sources from the init callback
		if len(result.InitCalledSources) == 0 {
			result.Error = "Script init callback did not contain any sources"
			return result
		}

		result.Valid = true
		result.Sources = result.InitCalledSources
		return result

	case <-ctx.Done():
		result.Error = fmt.Sprintf("Script execution timeout (limit: %v)", se.timeout)
		return result
	}
}

// extractSourcesFromVM extracts source keys from a JavaScript object
func extractSourcesFromVM(obj *goja.Object) []string {
	sources := make([]string, 0)
	seen := make(map[string]bool)

	// Iterate through object properties
	for _, key := range obj.Keys() {
		val := obj.Get(key)
		if val != nil && !goja.IsUndefined(val) {
			// Check if value is an object (source definition)
			if _, ok := val.(*goja.Object); ok {
				if !seen[key] {
					sources = append(sources, key)
					seen[key] = true
				}
			}
		}
	}

	return sources
}

// ValidateScriptExecution validates that a script can run and properly initializes
// Returns: (valid, sources, error message)
func ValidateScriptExecution(scriptContent string) (bool, []string, string) {
	executor := NewScriptExecutor(5 * time.Second)
	result := executor.Execute(context.Background(), scriptContent)

	if !result.Valid {
		return false, []string{}, result.Error
	}

	return true, result.Sources, ""
}

func ValidateScriptExecutionWithOptions(scriptContent string, allowUnsafeVM bool) *ScriptExecutionResult {
	executor := NewScriptExecutorWithOptions(5*time.Second, allowUnsafeVM)
	return executor.Execute(context.Background(), scriptContent)
}

// ValidateScriptWithMetadata validates script has both metadata and proper execution
func ValidateScriptWithMetadata(scriptContent string) (bool, scriptMetadata, []string, string) {
	meta, result := ValidateScriptWithMetadataOptions(scriptContent, false)
	if !result.Valid {
		return false, meta, []string{}, result.Error
	}
	return true, meta, result.Sources, ""
}

func ValidateScriptWithMetadataOptions(scriptContent string, allowUnsafeVM bool) (scriptMetadata, *ScriptExecutionResult) {
	// First check metadata
	meta := extractMetadataFromCode(scriptContent)

	// Then check execution
	executor := NewScriptExecutorWithOptions(5*time.Second, allowUnsafeVM)
	result := executor.Execute(context.Background(), scriptContent)
	return meta, result
}
