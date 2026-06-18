package nativeapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/log"
)

const onlineDownloadScriptTimeout = 12 * time.Second
const onlineDownloadScriptRetries = 3

type onlineBrowserDownloadRequest struct {
	SongInfo map[string]any `json:"songInfo"`
	Quality  string         `json:"quality"`
	// NameTemplate is the ordered list of chip tokens the user picked
	// in the "下载命名设置" UI (e.g. ["歌名", "音质", "歌手"]). It is
	// persisted to settings.json, so the frontend reads it back on
	// mount and sends it with every download request. Empty list
	// means "fall back to the default name" — the file name builder
	// will substitute [歌名, 歌手] in that case so the filename
	// always has at least a name + singer component.
	NameTemplate []string `json:"nameTemplate,omitempty"`
}

type onlineBrowserDownloadStartResponse struct {
	TaskID string `json:"taskId"`
}

type onlineBrowserDownloadProgressResponse struct {
	TaskID    string `json:"taskId"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	Received  int64  `json:"received"`
	Total     int64  `json:"total"`
	Error     string `json:"error,omitempty"`
	FileReady bool   `json:"fileReady"`
	// SourceName is the human-readable name of the custom JS script that
	// actually resolved the URL (e.g. "ikun[赞助][永久]"). It is set as
	// soon as the resolve phase succeeds, so the frontend can show
	// "ikun[赞助]… 解析中…" before the file actually starts streaming.
	// Empty for built-in sources (wy/tx/kg/kw/mg) or if resolve failed.
	SourceName string `json:"sourceName,omitempty"`
}

type onlineDownloadResolveInput struct {
	Script      string         `json:"script"`
	AllowUnsafe bool           `json:"allowUnsafe"`
	Source      string         `json:"source"`
	MusicInfo   map[string]any `json:"musicInfo"`
	Quality     string         `json:"quality"`
}

type onlineDownloadResolveResult struct {
	Success bool              `json:"success"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Error   string            `json:"error,omitempty"`
}

type onlineDownloadTask struct {
	ID          string
	Mode        string
	Title       string
	Artist      string
	Source      string
	Quality     string
	SourceName  string
	Status      string
	Progress    int
	Received    int64
	Total       int64
	Speed       int64
	Error       string
	FilePath    string
	FileName    string
	ContentType string
	ResolvedURL string
	Headers     map[string]string
	DownloadDir string
	TempPath    string
	PauseWanted bool
	CancelFunc  context.CancelFunc
	// SongInfo is the full normalized music-info payload that was
	// handed to us by the frontend (id, songmid, hash, albumId, meta,
	// types, etc.). Server-mode tasks replay it through
	// resolveOnlineDownloadURLWithProgress on every fallback attempt,
	// so the resolve script must see the *same* fields it would have
	// seen in browser mode. Without this, scripts that key off
	// info.songmid / info.id construct broken URLs and the download
	// fails with 404. Browser-mode tasks keep this nil because
	// runOnlineDownloadTask receives the normalized map directly.
	SongInfo map[string]any
	// NameTemplate is the user-configured chip order from the
	// "下载命名设置" panel. Stored on the task so async and fallback
	// code paths can build the file name consistently even if the
	// global settings change mid-download.
	NameTemplate []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type onlineServerDownloadTaskView struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Source     string `json:"source"`
	SourceName string `json:"sourceName,omitempty"`
	Quality    string `json:"quality"`
	Status     string `json:"status"`
	Progress   int    `json:"progress"`
	Received   int64  `json:"received"`
	Total      int64  `json:"total"`
	Speed      int64  `json:"speed"`
	Error      string `json:"error,omitempty"`
}

type onlineServerDownloadTasksResponse struct {
	Tasks          []onlineServerDownloadTaskView `json:"tasks"`
	TaskCount      int                            `json:"taskCount"`
	ActiveCount    int                            `json:"activeCount"`
	TotalSpeed     int64                          `json:"totalSpeed"`
	TotalSpeedText string                         `json:"totalSpeedText"`
	TotalProgress  int                            `json:"totalProgress"`
}

var onlineDownloadTasks = struct {
	sync.RWMutex
	items map[string]*onlineDownloadTask
}{items: map[string]*onlineDownloadTask{}}

const onlineDownloadTaskTTL = 30 * time.Minute

// onlineDownloadTaskStallTimeout is the maximum wall-clock time a
// server download task may stay in the "downloading" state with zero
// bytes received before the stall detector fails it. The check fires
// ONLY for tasks that have not yet produced any progress (Received == 0)
// — once a task has read at least one byte, the deadline no longer
// applies, because slow upstream servers or large files can take much
// longer than 1 minute to complete.
const onlineDownloadTaskStallTimeout = 1 * time.Minute

// onlineDownloadTaskStallScanInterval is how often the stall detector
// wakes up to scan for dead-link tasks. Smaller than the stall timeout
// so we react promptly.
const onlineDownloadTaskStallScanInterval = 10 * time.Second

var startStallDetectorOnce sync.Once

// downloadTaskBroker is a lightweight fan-out pub/sub that notifies
// SSE clients whenever the download task list mutates. Each subscriber
// holds a *persistent* buffered channel of size 1. The broadcaster
// does a non-blocking send to each channel — if the channel already
// holds an unread token the send is dropped (the subscriber will still
// wake up and re-snapshot). Subscribers never need to re-subscribe
// between signals, which eliminates the window where high-frequency
// updates (e.g. per-chunk progress ticks) race with re-subscribe and
// get silently dropped.
var downloadTaskBroker = struct {
	sync.Mutex
	subs map[chan struct{}]struct{}
}{subs: map[chan struct{}]struct{}{}}

// subscribeDownloadTaskNotifications registers a persistent buffered
// channel (capacity 1) and returns it together with an unsubscribe
// function. The broker sends a non-blocking token into the channel on
// every task mutation. Callers MUST NOT close the channel themselves;
// they should call the returned unsubscribe function instead.
func subscribeDownloadTaskNotifications() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	downloadTaskBroker.Lock()
	downloadTaskBroker.subs[ch] = struct{}{}
	downloadTaskBroker.Unlock()
	return ch, func() {
		downloadTaskBroker.Lock()
		delete(downloadTaskBroker.subs, ch)
		downloadTaskBroker.Unlock()
		// Drain the channel so any blocked receiver unblocks cleanly.
		select {
		case <-ch:
		default:
		}
	}
}

// broadcastDownloadTaskChange sends a non-blocking signal to every
// subscriber. If a subscriber's channel already has an unconsumed token
// the extra send is intentionally dropped — the subscriber will still
// wake up and see the latest snapshot, which is always the canonical
// source of truth. Safe to call from any goroutine and never blocks.
func broadcastDownloadTaskChange() {
	downloadTaskBroker.Lock()
	defer downloadTaskBroker.Unlock()
	for ch := range downloadTaskBroker.subs {
		select {
		case ch <- struct{}{}:
		default: // already has a pending token; subscriber will still wake up
		}
	}
}

const nodeOnlineDownloadScript = `
const vm = require('vm');
const https = require('https');
const http = require('http');
const crypto = require('crypto');
const zlib = require('zlib');

let raw = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => { raw += chunk; });
process.stdin.on('end', async () => {
  const payload = JSON.parse(raw || '{}');
  const allowUnsafe = !!payload.allowUnsafe;
  const timeoutMs = Number(process.env.ND_TIMEOUT_MS || '12000');

  const decontextify = (obj) => {
    if (obj === null || obj === undefined) return obj;
    if (typeof obj !== 'object') return obj;
    try {
      if (Buffer.isBuffer(obj) || obj instanceof Uint8Array || (obj && obj.constructor && obj.constructor.name === 'Buffer')) {
        return Buffer.from(Uint8Array.from(obj));
      }
    } catch (e) {}
    if (Array.isArray(obj)) {
      try { return obj.map(item => decontextify(item)); } catch (e) { return []; }
    }
    if (obj instanceof Error || (obj && obj.constructor && obj.constructor.name === 'Error')) {
      const err = new Error(obj.message);
      err.stack = obj.stack;
      return err;
    }
    try {
      const newObj = {};
      const keys = Object.keys(obj);
      for (const key of keys) {
        try { newObj[key] = decontextify(obj[key]); } catch (e) {}
      }
      return newObj;
    } catch (e) {
      try {
        const str = JSON.stringify(obj);
        return str ? JSON.parse(str) : String(obj);
      } catch (e2) {
        return String(obj);
      }
    }
  };

  const createLxRequest = () => {
    return (targetUrl, options, callback) => {
      const safeOptions = decontextify(options || {});
      const method = String((safeOptions.method || 'GET')).toUpperCase();
      const headers = Object.assign({}, safeOptions.headers || {});
      const requestTimeout = Math.min(Number(safeOptions.timeout || timeoutMs) || timeoutMs, timeoutMs);
      
      if (!headers['User-Agent']) {
        headers['User-Agent'] = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36';
      }
      
      let body = safeOptions.body;
      if (!body && safeOptions.form) {
        body = new URLSearchParams(safeOptions.form).toString();
        if (!headers['Content-Type']) headers['Content-Type'] = 'application/x-www-form-urlencoded';
      }
			if (!body && safeOptions.formData) {
				body = safeOptions.formData;
			}
      if (body && typeof body !== 'string' && !Buffer.isBuffer(body)) {
        body = JSON.stringify(body);
        if (!headers['Content-Type']) headers['Content-Type'] = 'application/json';
      }
      if (body) headers['Content-Length'] = Buffer.byteLength(body);

      const run = (requestUrl, redirects) => {
        let parsed;
        try {
          parsed = new URL(requestUrl);
        } catch (error) {
          callback(error, null, null);
          return;
        }
        
        if (!headers['Referer']) {
          headers['Referer'] = parsed.protocol + '//' + parsed.host;
        }
        
        const lib = parsed.protocol === 'https:' ? https : http;
        const req = lib.request({
          hostname: parsed.hostname,
          port: parsed.port ? Number(parsed.port) : (parsed.protocol === 'https:' ? 443 : 80),
          path: parsed.pathname + parsed.search,
          method,
          headers,
          rejectUnauthorized: false,
        }, (res) => {
          if ([301, 302, 303, 307, 308].includes(res.statusCode) && res.headers.location && redirects > 0) {
            const nextUrl = new URL(res.headers.location, requestUrl).toString();
            run(nextUrl, redirects - 1);
            return;
          }
          let chunks = [];
          res.on('data', chunk => chunks.push(chunk));
          res.on('end', () => {
						let buffer = Buffer.concat(chunks);

						const contentEncoding = String(res.headers['content-encoding'] || '').toLowerCase();
						try {
							if (contentEncoding.includes('gzip')) {
								buffer = zlib.gunzipSync(buffer);
							} else if (contentEncoding.includes('deflate')) {
								buffer = zlib.inflateSync(buffer);
							} else if (contentEncoding.includes('br') && typeof zlib.brotliDecompressSync === 'function') {
								buffer = zlib.brotliDecompressSync(buffer);
							}
						} catch (_) {}

						const contentType = String(res.headers['content-type'] || '').toLowerCase();
						let parsedBody = buffer;
						const isTextLike =
							contentType.includes('application/json') ||
							contentType.includes('text/') ||
							contentType.includes('javascript') ||
							contentType.includes('xml') ||
							contentType.includes('application/x-www-form-urlencoded');

						if (isTextLike) {
							const text = buffer.toString('utf8');
							parsedBody = text;
							try { parsedBody = JSON.parse(text); } catch (_) {}
						}

            const safeResp = {
              statusCode: res.statusCode,
              statusMessage: res.statusMessage,
              headers: res.headers,
              body: decontextify(parsedBody),
            };
            callback(null, safeResp, safeResp.body);
          });
        });
        req.setTimeout(requestTimeout, () => req.destroy(new Error('request timeout')));
        req.on('error', err => callback(err, null, null));
        if (body) req.write(body);
        req.end();
      };

      run(targetUrl, 3);
      return () => {};
    };
  };

  let requestHandler = null;
  let initResolve;
  let initReject;
  let registeredSources = {};
  const initPromise = new Promise((resolve, reject) => {
    initResolve = resolve;
    initReject = reject;
  });

  const lxObject = {
    version: '2.0.0',
    env: 'desktop',
    platform: 'web',
    EVENT_NAMES: { request: 'request', inited: 'inited', updateAlert: 'updateAlert' },
    utils: {
      buffer: {
				from: (d, e) => Buffer.from(decontextify(d), decontextify(e)),
        bufToString: (b, f) => Buffer.isBuffer(b) ? b.toString(f) : Buffer.from(b, 'binary').toString(f),
      },
      crypto: {
        md5: (str) => crypto.createHash('md5').update(String(str || '')).digest('hex'),
				aesEncrypt: (buffer, mode, key, iv) => {
					const dKey = decontextify(key);
					const dIv = decontextify(iv);
					const dBuffer = decontextify(buffer);
					const algorithm = 'aes-' + (dKey.length * 8) + '-' + mode;
					const cipher = crypto.createCipheriv(algorithm, dKey, dIv);
					return Buffer.concat([cipher.update(dBuffer), cipher.final()]);
				},
				aesDecrypt: (buffer, mode, key, iv) => {
					const dKey = decontextify(key);
					const dIv = decontextify(iv);
					const dBuffer = decontextify(buffer);
					const algorithm = 'aes-' + (dKey.length * 8) + '-' + mode;
					const decipher = crypto.createDecipheriv(algorithm, dKey, dIv);
					return Buffer.concat([decipher.update(dBuffer), decipher.final()]);
				},
				rsaEncrypt: (buffer, key) => crypto.publicEncrypt(decontextify(key), decontextify(buffer)),
        randomBytes: (size) => crypto.randomBytes(size),
      },
      zlib: {
        inflate: (buffer) => zlib.inflateSync(Buffer.from(buffer)),
        deflate: (buffer) => zlib.deflateSync(Buffer.from(buffer)),
      },
    },
    request: createLxRequest(),
    send: (eventName, data) => {
      const safeData = decontextify(data);
      if (eventName === 'inited') {
        registeredSources = safeData && safeData.sources ? safeData.sources : {};
        initResolve();
      } else if (eventName === 'updateAlert') {
        initReject(new Error('发现新版本,需要更新'));
      }
    },
    on: (eventName, handler) => {
      if (eventName === 'request' && typeof handler === 'function') {
        requestHandler = handler;
      }
    },
  };

  const sandbox = {
    // Mirror the script_executor bootstrap: Proxy-based console stub
    // that returns a noop for every standard method (so lx-music style
    // scripts that call console.groupEnd / table / count don't crash)
    // AND is locked via Object.defineProperty below to prevent
    // obfuscated scripts from overwriting it with a bare object.
    console: allowUnsafe ? console : new Proxy({}, {
      get: function(_target, prop) {
        if (typeof prop === 'string' && /^[a-zA-Z_$][\w$]*$/.test(prop)) {
          return function() {};
        }
        return undefined;
      },
    }),
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    Buffer,
    URL,
    URLSearchParams,
    TextEncoder,
    TextDecoder,
    process: allowUnsafe ? process : { nextTick: (fn, ...args) => setTimeout(() => fn(...args), 0), env: { NODE_ENV: process.env.NODE_ENV || 'production' } },
    lx: lxObject,
    global: null,
    window: null,
    globalThis: null,
    atob: (s) => Buffer.from(s, 'base64').toString('binary'),
    btoa: (s) => Buffer.from(s, 'binary').toString('base64'),
    crypto,
    module: { exports: {} },
    exports: {},
    require: allowUnsafe ? require : () => { throw new Error('REQUIRE_UNSAFE_VM'); },
  };
  sandbox.global = sandbox;
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;
  // Lock the console stub on the sandbox global so scripts cannot
  // replace it with "this.console = ..." or "globalThis.console = ...".
  // Without this, console.groupEnd() on a bare replacement object
  // throws "is not a function" mid-request and aborts the download.
  Object.defineProperty(sandbox, 'console', {
    value: sandbox.console,
    writable: false,
    configurable: false,
    enumerable: true,
  });

  // 脚本期望一个对象参数：{ action, source, info }。两个 action 共用
  // 同一个 request 处理器：musicUrl 拿下载链接，lyric 拿歌词文本。
  // 脚本如果不支持 lyric action，requestHandler 可能会抛错；我们对
  // 这种情况做静默降级，由 Go 端把它当作"无歌词"处理。
  //
  // IMPORTANT: requestedAction and info MUST be declared
  // OUTSIDE the outer try block. In strict-mode JavaScript
  // const/let are block-scoped, so a const declared inside
  // the try block is not visible inside the matching catch
  // block. Without this, the lyric_unsupported branch in
  // the catch throws ReferenceError: requestedAction is
  // not defined and the entire script path silently
  // breaks for the lyric case (the bug the user reported
  // in navidrome.log line lyric:node-failed). The values
  // are independent of the script's init phase so it's
  // safe to compute them up here.
  const requestedAction = (payload && payload.action) === 'lyric' ? 'lyric' : 'musicUrl';
  const info = decontextify({
    musicInfo: payload.musicInfo || {},
    quality: payload.quality,
    type: payload.quality,
  });
  let inputData = { action: requestedAction, source: payload.source, info: info };

  try {
    vm.runInContext(payload.script, vm.createContext(sandbox), {
      filename: 'online_download_source.js',
      timeout: timeoutMs,
    });

    await Promise.race([
      initPromise,
      new Promise((_, reject) => setTimeout(() => reject(new Error('初始化超时，请确保脚本调用了 lx.send("inited", ...)')), 3000)),
    ]);

    if (!registeredSources || !registeredSources[payload.source]) {
      throw new Error('当前脚本未声明支持该音源');
    }
    if (typeof requestHandler !== 'function') {
      throw new Error('当前脚本未注册 request 处理器');
    }

    if (allowUnsafe) {
      inputData = JSON.parse(JSON.stringify(inputData));
    }
    const result = await requestHandler(inputData);

    if (requestedAction === 'lyric') {
      // Scripts that support a lyric handler return either a raw
      // string (most common) or an object { lyric / lrc }. We
      // normalize both shapes and serialize only the lyric field on
      // stdout; the Go side reads the lyric field back.
      const dResult = decontextify(result);
      let lyricText = '';
      if (typeof dResult === 'string') {
        lyricText = dResult;
      } else if (dResult && typeof dResult === 'object') {
        lyricText = String(dResult.lyric || dResult.lrc || '');
      }
      process.stdout.write(JSON.stringify({ success: true, lyric: String(lyricText) }));
      return;
    }

		let finalUrl = '';
		let finalHeaders = null;
    const dResult = decontextify(result);
    if (typeof dResult === 'string') {
      finalUrl = dResult;
    } else if (dResult && typeof dResult === 'object') {
      if (typeof dResult.url === 'string') {
        finalUrl = dResult.url;
      } else if (typeof dResult.data === 'string') {
        finalUrl = dResult.data;
			} else if (dResult.data && typeof dResult.data.url === 'string') {
				finalUrl = dResult.data.url;
			}

			if (dResult.headers && typeof dResult.headers === 'object') {
				finalHeaders = dResult.headers;
			} else if (dResult.header && typeof dResult.header === 'object') {
				finalHeaders = dResult.header;
			} else if (dResult.requestHeaders && typeof dResult.requestHeaders === 'object') {
				finalHeaders = dResult.requestHeaders;
			} else if (dResult.options && dResult.options.headers && typeof dResult.options.headers === 'object') {
				finalHeaders = dResult.options.headers;
			} else if (dResult.data && dResult.data.headers && typeof dResult.data.headers === 'object') {
				finalHeaders = dResult.data.headers;
			} else if (dResult.data && dResult.data.header && typeof dResult.data.header === 'object') {
				finalHeaders = dResult.data.header;
	      }
    }

    if (!finalUrl || typeof finalUrl !== 'string') {
      console.error('[OnlineDownload] Invalid result:', JSON.stringify(dResult));
      throw new Error('脚本未返回有效下载链接');
    }

    finalUrl = String(finalUrl).trim();
    if (!finalUrl) {
      throw new Error('脚本返回的下载链接为空');
    }

		const normalizedHeaders = {};
		if (finalHeaders && typeof finalHeaders === 'object') {
			for (const [k, v] of Object.entries(finalHeaders)) {
				if (!k) continue;
				if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean') {
					normalizedHeaders[String(k)] = String(v);
				} else if (Array.isArray(v)) {
					const parts = v
						.map(x => (typeof x === 'string' || typeof x === 'number' || typeof x === 'boolean') ? String(x) : '')
						.filter(Boolean);
					if (parts.length > 0) normalizedHeaders[String(k)] = parts.join('; ');
				}
			}
		}

		process.stdout.write(JSON.stringify({ success: true, url: finalUrl, headers: normalizedHeaders }));
  } catch (error) {
    // For lyric action, scripts that don't implement a lyric handler
    // throw here. We surface that as success=false with a
    // distinctive error prefix so the Go side can match it and skip
    // silently instead of logging a warning for every song.
    if (requestedAction === 'lyric') {
      const msg = (error && error.message) ? String(error.message) : String(error);
      process.stdout.write(JSON.stringify({ success: false, error: 'lyric_unsupported: ' + msg }));
    } else {
      process.stdout.write(JSON.stringify({ success: false, error: error && error.message ? error.message : String(error) }));
    }
  }
});
`

func (api *Router) addOnlineDownloadRoutes(r chi.Router) {
	// Start the stall detector exactly once, the first time the routes
	// are wired up. Doing it here (instead of package init) means tests
	// that exercise the package without registering routes do not leak
	// the background goroutine.
	startStallDetector()

	r.Route("/online/download", func(r chi.Router) {
		r.Post("/server/start", api.onlineServerDownloadStart)
		r.Get("/tasks", api.onlineServerDownloadTasks)
		r.Get("/tasks/stream", api.onlineServerDownloadTasksStream)
		r.Post("/task/{taskID}/toggle", api.onlineServerDownloadToggle)
		r.Post("/tasks/retry", api.onlineServerDownloadRetryAll)
		r.Post("/tasks/cancel", api.onlineServerDownloadCancelAll)
		r.Post("/tasks/clear-completed", api.onlineServerDownloadClearCompleted)
		r.Post("/tasks/clear-failed", api.onlineServerDownloadClearFailed)
		r.Post("/browser/start", api.onlineBrowserDownloadStart)
		r.Get("/browser/progress/{taskID}", api.onlineBrowserDownloadProgress)
		r.Get("/browser/file/{taskID}", api.onlineBrowserDownloadFile)
		r.Post("/browser", api.onlineBrowserDownload)
	})

	// Playlist sync endpoints require authenticated user context.
	r.Post("/online/playlist/sync/start", handlePlaylistSyncStart)
	r.Get("/online/playlist/sync/status/{taskID}", handlePlaylistSyncStatus)
	r.Get("/online/playlist/sync/tasks", handlePlaylistSyncTasks)
	r.Post("/online/playlist/sync/tasks/retry", handlePlaylistSyncRetryAll)
	r.Post("/online/playlist/sync/tasks/cancel", handlePlaylistSyncCancelAll)
	r.Post("/online/playlist/sync/tasks/clear-completed", handlePlaylistSyncClearCompleted)
	r.Post("/online/playlist/sync/tasks/clear-failed", handlePlaylistSyncClearFailed)
}

func (api *Router) onlineBrowserDownloadStart(w http.ResponseWriter, r *http.Request) {
	var req onlineBrowserDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	songSource := strings.TrimSpace(stringValue(req.SongInfo["source"]))
	if songSource == "" {
		http.Error(w, "songInfo.source is required", http.StatusBadRequest)
		return
	}

	quality := strings.TrimSpace(req.Quality)
	if quality == "" {
		quality = bestOnlineDownloadQuality(req.SongInfo)
	}

	taskID := createOnlineDownloadTask(req.NameTemplate)
	normalized := normalizeOnlineDownloadSongInfo(req.SongInfo)
	go runOnlineDownloadTask(taskID, songSource, normalized, quality)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(onlineBrowserDownloadStartResponse{TaskID: taskID})
}

func (api *Router) onlineBrowserDownloadProgress(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chi.URLParam(r, "taskID"))
	task, ok := getOnlineDownloadTask(taskID)
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(onlineBrowserDownloadProgressResponse{
		TaskID:     task.ID,
		Status:     task.Status,
		Progress:   task.Progress,
		Received:   task.Received,
		Total:      task.Total,
		Error:      task.Error,
		FileReady:  task.Status == "completed" && task.FilePath != "",
		SourceName: task.SourceName,
	})
}

func (api *Router) onlineBrowserDownloadFile(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chi.URLParam(r, "taskID"))
	task, ok := getOnlineDownloadTask(taskID)
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	if task.Status != "completed" || task.FilePath == "" {
		http.Error(w, "task not ready", http.StatusConflict)
		return
	}

	file, err := os.Open(task.FilePath)
	if err != nil {
		http.Error(w, "file not found", http.StatusGone)
		deleteOnlineDownloadTask(taskID)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.Error(w, "file stat failed", http.StatusInternalServerError)
		return
	}

	contentType := strings.TrimSpace(task.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, X-Online-Download-Mode")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDispositionValue(task.FileName))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("X-Online-Download-Mode", "buffered-task-file")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)

	deleteOnlineDownloadTask(taskID)
}

func (api *Router) onlineBrowserDownload(w http.ResponseWriter, r *http.Request) {
	var req onlineBrowserDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	songSource := strings.TrimSpace(stringValue(req.SongInfo["source"]))
	if songSource == "" {
		http.Error(w, "songInfo.source is required", http.StatusBadRequest)
		return
	}

	quality := strings.TrimSpace(req.Quality)
	if quality == "" {
		quality = bestOnlineDownloadQuality(req.SongInfo)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	normalized := normalizeOnlineDownloadSongInfo(req.SongInfo)
	resolvedURL, sourceName, resolvedHeaders, err := resolveOnlineDownloadURL(ctx, songSource, normalized, quality)
	if err != nil {
		log.Warn(r.Context(), "Online browser download resolve failed", "source", songSource, "quality", quality, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Expose resolved info for frontend console debug and manual verification.
	w.Header().Set("X-Online-Resolved-URL", resolvedURL)
	w.Header().Set("X-Online-Resolver-Source", sourceName)
	w.Header().Set("Access-Control-Expose-Headers", "X-Online-Resolved-URL, X-Online-Resolver-Source, Content-Disposition")

	fileName := onlineDownloadFileName(normalized, quality, req.NameTemplate, resolvedURL)

	// Embed hook: run cover / metadata / lyrics embedding on the
	// downloaded temp file *before* it gets streamed back to the
	// user's browser. We do NOT have the source script handle in
	// this streaming path, so lyric lookup is limited to the
	// songInfo.meta.lrcUrl fallback. Failure is logged and ignored.
	// The hook is only registered when the user has metadata
	// embedding enabled in settings — a noop proxy otherwise keeps
	// the streaming path zero-overhead for users who turned it off.
	var embedHook func(tempPath string, contentType string, size int64)
	if onlineEmbedEnabled() {
		embedHook = func(tempPath string, _ string, _ int64) {
			embedCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			downloadDir := filepath.Dir(tempPath)
			// Breadcrumb before we touch anything else. If the user
			// only sees the legacy log.Info line below and not this
			// [EMBED] one, they ran an older binary. The legacy line
			// is kept for grep-compatibility.
			embedTrace(embedCtx, "browser-stream:enter", "audio", tempPath)
			log.Info(embedCtx, "Online embed: browser-stream embed starting", "audio", tempPath)
			// We don't have a stable taskID here, so synthesize one
			// for the cover artifact path to avoid clashes between
			// concurrent downloads.
			coverRef := fetchAndPersistOnlineCover(embedCtx, downloadDir, "stream-"+filepath.Base(tempPath), normalized)
			lyric := lookupOnlineBrowserLyricFromSongInfo(embedCtx, normalized)
			if _, finalPath, err := onlineEmbedDownloadMetadata(embedCtx, tempPath, normalized, quality, coverRef, lyric); err != nil {
				embedTrace(embedCtx, "browser-stream:metadata-failed", "audio", tempPath, "err", err.Error())
				log.Error(embedCtx, "Online embed: streaming-path embed failed", "err", err)
			} else {
				embedTrace(embedCtx, "browser-stream:metadata-done", "audio", finalPath)
			}
			onlineEmbedCleanupArtwork(downloadDir)
		}
	}

	if err := proxyOnlineDownloadWithEmbed(ctx, w, r, resolvedURL, fileName, resolvedHeaders, embedHook); err != nil {
		log.Warn(r.Context(), "Online browser download proxy failed", "source", songSource, "resolver", sourceName, "url", resolvedURL, "err", err)
		if w.Header().Get("Content-Type") == "" {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}
}

func (api *Router) onlineServerDownloadStart(w http.ResponseWriter, r *http.Request) {
	var req onlineBrowserDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	songSource := strings.TrimSpace(stringValue(req.SongInfo["source"]))
	if songSource == "" {
		http.Error(w, "songInfo.source is required", http.StatusBadRequest)
		return
	}

	quality := strings.TrimSpace(req.Quality)
	if quality == "" {
		quality = bestOnlineDownloadQuality(req.SongInfo)
	}

	settings, err := loadOnlineSourceSettings()
	if err != nil {
		http.Error(w, "could not load online settings", http.StatusInternalServerError)
		return
	}
	downloadDir := strings.TrimSpace(settings.DownloadPath)
	if downloadDir == "" {
		downloadDir = defaultOnlineDownloadPath()
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		http.Error(w, "could not create download directory", http.StatusInternalServerError)
		return
	}

	normalized := normalizeOnlineDownloadSongInfo(req.SongInfo)
	taskID := createOnlineServerDownloadTask(normalized, songSource, quality, downloadDir, req.NameTemplate)
	go runOnlineServerDownloadTask(taskID) //nolint:gosec

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(onlineBrowserDownloadStartResponse{TaskID: taskID})
}

func (api *Router) onlineServerDownloadTasks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listOnlineServerDownloadTasks())
}

// onlineServerDownloadTasksStream pushes a "tasks-changed" event over
// SSE every time the in-memory download task list mutates. The browser
// is expected to call GET /api/online/download/tasks on each event to
// fetch the actual snapshot. We send only a signal, not the payload, to
// keep the wire format tiny and avoid races between mutation and
// snapshot generation.
//
// A 15s keep-alive comment is also emitted so reverse proxies (nginx)
// and intermediaries do not close the idle connection.
func (api *Router) onlineServerDownloadTasksStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send a comment line immediately so the browser's EventSource
	// onopen fires before any real event. This also primes proxies
	// that buffer until the first write.
	_, _ = fmt.Fprint(w, ": stream-open\n\n")
	flusher.Flush()

	ch, unsubscribe := subscribeDownloadTaskNotifications()
	defer unsubscribe()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ch:
			if _, err := fmt.Fprint(w, "event: tasks-changed\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
			// The channel is persistent (non-closing broker); no need
			// to re-subscribe. Simply drain and loop back to select.
		}
	}
}

func (api *Router) onlineServerDownloadToggle(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chi.URLParam(r, "taskID"))
	if taskID == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}

	action, err := toggleOnlineServerDownloadTask(taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"action": action})
}

func (api *Router) onlineServerDownloadRetryAll(w http.ResponseWriter, _ *http.Request) {
	retryAllOnlineServerDownloadTasks()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (api *Router) onlineServerDownloadCancelAll(w http.ResponseWriter, _ *http.Request) {
	cancelAllOnlineServerDownloadTasks()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (api *Router) onlineServerDownloadClearCompleted(w http.ResponseWriter, _ *http.Request) {
	clearCompletedOnlineServerDownloadTasks()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (api *Router) onlineServerDownloadClearFailed(w http.ResponseWriter, _ *http.Request) {
	clearFailedOnlineServerDownloadTasks()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// createOnlineServerDownloadTask inserts a freshly-created server-mode
// download task into the in-memory task list and broadcasts a change
// event so the Download_list UI sees the new entry immediately.
//
// ResolvedURL, Headers, FilePath, FileName and TempPath are intentionally
// left empty: the resolve phase is run asynchronously by
// runOnlineServerDownloadTask so the user can see the task (and the
// "resolving…" state) right away, and any later fallback to a different
// script can rewrite the on-disk filename without leaving orphan .part
// files from the previous attempt.
func createOnlineServerDownloadTask(
	songInfo map[string]any,
	source string,
	quality string,
	downloadDir string,
	nameTemplate []string,
) string {
	now := time.Now()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		_ = err // fallback to sequential ID if crypto/rand fails
	}
	id := fmt.Sprintf("odl_%d_%d", now.UnixNano(), int64(buf[0])<<56|int64(buf[1])<<48|int64(buf[2])<<40|int64(buf[3])<<32|int64(buf[4])<<24|int64(buf[5])<<16|int64(buf[6])<<8|int64(buf[7]))

	title := stringValue(songInfo["name"])
	if title == "" {
		title = "未知标题"
	}
	artist := stringValue(songInfo["singer"])
	if artist == "" {
		artist = "未知歌手"
	}

	onlineDownloadTasks.Lock()
	_ = cleanupExpiredOnlineDownloadTasksLocked(now)
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:           id,
		Mode:         "server",
		Title:        title,
		Artist:       artist,
		Source:       source,
		Quality:      quality,
		Status:       "queued",
		Progress:     0,
		DownloadDir:  downloadDir,
		SongInfo:     songInfo,
		NameTemplate: append([]string{}, nameTemplate...),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	onlineDownloadTasks.Unlock()
	// Notify SSE subscribers after the write lock is released so
	// subscribers can immediately re-snapshot the new state. Always
	// broadcast — the new task itself is a change worth signaling.
	broadcastDownloadTaskChange()
	return id
}

func runOnlineServerDownloadTask(taskID string) {
	task, ok := getOnlineDownloadTaskPointer(taskID)
	if !ok {
		return
	}
	if task.Mode != "server" {
		return
	}

	downloadDir := strings.TrimSpace(task.DownloadDir)
	if downloadDir == "" {
		settings, settingsErr := loadOnlineSourceSettings()
		if settingsErr == nil {
			downloadDir = strings.TrimSpace(settings.DownloadPath)
		}
	}
	if downloadDir == "" {
		downloadDir = defaultOnlineDownloadPath()
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		setOnlineDownloadTaskFailed(taskID, fmt.Errorf("创建下载目录失败: %w", err))
		return
	}

	songSource := task.Source
	quality := task.Quality

	// Load the candidate list once up front. The set of enabled scripts
	// is allowed to change between candidates (the user might toggle one
	// off mid-download) but re-reading on every iteration would let a
	// race reorder the fallback sequence, which is harder to reason
	// about than simply locking the order at task start.
	candidates, err := loadEnabledSourcesForSong(songSource)
	if err != nil {
		setOnlineDownloadTaskFailed(taskID, fmt.Errorf("加载音源失败: %w", err))
		return
	}
	if len(candidates) == 0 {
		setOnlineDownloadTaskFailed(taskID, fmt.Errorf("未找到支持 %s 的启用音源脚本", songSource))
		return
	}

	// Pre-populate the resolve phase with the first candidate so the
	// download list shows a meaningful name immediately. The actual
	// download phase rewrites the source name once a candidate wins.
	firstCandidateName := candidates[0].Name
	ctx, cancel := context.WithCancel(context.Background())
	updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
		t.CancelFunc = cancel
		t.PauseWanted = false
		t.Error = ""
		t.Status = "resolving"
		t.Progress = 0
		t.Received = 0
		t.Total = 0
		t.Speed = 0
		t.SourceName = firstCandidateName
	})

	// Re-fetch the songInfo from the (now potentially updated) task.
	// The struct only carries source/quality, so we re-normalize from
	// the persisted fields via the helper in case other fields are
	// present on the task (none currently are, but this keeps the
	// interface symmetric with runOnlineDownloadTask).
	_ = quality

	var attemptErrors []string
	done := false
	for _, candidate := range candidates {
		if done {
			break
		}
		// Each iteration gets its own attempt ctx so a failed previous
		// candidate's deadline doesn't carry over. We bail cleanly when
		// the outer ctx (user cancel / stall detector) is canceled.
		attemptCtx, attemptCancel := context.WithCancel(ctx)

		updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
			t.Status = "resolving"
			t.Progress = 0
			t.Received = 0
			t.Total = 0
			t.Speed = 0
			t.SourceName = candidate.Name
		})

		// Replay the original normalized songInfo for every fallback
		// attempt so the resolve script sees the same payload it would
		// have seen in browser mode (id, songmid, hash, meta, …).
		// Reconstructing it from Title/Artist/etc. was lossy and caused
		// scripts that key off info.songmid to construct broken URLs.
		normalized := task.SongInfo
		if normalized == nil {
			normalized = buildOnlineDownloadTaskSongInfo(task)
		}

		resolvedURL, resolvedHeaders, sourceName, resolveErr := resolveOnlineDownloadURLWithProgress(attemptCtx, candidate, songSource, normalized, quality)
		if resolveErr != nil {
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s 解析失败: %v", sourceName, resolveErr))
			log.Info(attemptCtx, "Online server download resolve failed, trying next candidate", "task", taskID, "candidate", sourceName, "err", resolveErr)
			attemptCancel()
			if ctx.Err() != nil {
				markServerTaskTerminal(taskID, ctx.Err())
				done = true
			}
			continue
		}

		// Resolve succeeded: build the on-disk file paths for this
		// attempt, populate the task, and stream.
		fileName := onlineDownloadFileName(normalized, quality, task.NameTemplate, resolvedURL)
		if !strings.Contains(fileName, ".") {
			fileName += ".mp3"
		}
		finalPath := uniqueOnlineDownloadPath(downloadDir, fileName)
		tempPath := finalPath + ".part"
		headers := map[string]string{}
		for k, v := range resolvedHeaders {
			headers[k] = v
		}

		updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
			t.Status = "downloading"
			t.Progress = 0
			t.Received = 0
			t.Total = 0
			t.Speed = 0
			t.SourceName = sourceName
			t.FileName = fileName
			t.FilePath = finalPath
			t.TempPath = tempPath
			t.ResolvedURL = resolvedURL
			t.Headers = headers
		})

		// downloadOnlineServerTaskToPath streams the body into
		// task.TempPath, supporting HTTP redirects (6 hops) and Range
		// resume. It returns once the file is fully written and
		// renamed to task.FilePath.
		result, fetchErr := downloadOnlineServerTaskToPath(attemptCtx, taskID)
		attemptCancel()
		if fetchErr == nil {
			// Best-effort: cover / metadata / lyrics embed. All
			// sub-steps log and continue on failure, so the user
			// always gets a playable file even if ffmpeg is missing
			// or the upstream never returned cover art.
			embedOnlineServerDownloadMetadata(attemptCtx, task, candidate, quality, result.FilePath)
			updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
				t.Status = "completed"
				t.Progress = 100
				t.Received = result.Size
				t.Total = result.Size
				t.Speed = 0
				t.FilePath = result.FilePath
				t.FileName = result.FileName
				t.ContentType = result.ContentType
				t.CancelFunc = nil
			})
			done = true
			continue
		}

		// Download failed. Honor outer-ctx cancellation (user cancel
		// or stall detector) before recording the failure.
		if ctx.Err() != nil {
			markServerTaskTerminal(taskID, ctx.Err())
			done = true
			continue
		}

		attemptErrors = append(attemptErrors, fmt.Sprintf("%s 下载失败: %v", sourceName, fetchErr))
		log.Warn(attemptCtx, "Online server download failed, trying next candidate", "task", taskID, "candidate", sourceName, "err", fetchErr)

		// Strip the orphaned .part file so the next candidate starts
		// clean. downloadOnlineServerTaskToPath already cleans up
		// its own tempPath on most errors, but a few edge cases (e.g.
		// the file was successfully renamed but the ctx fired right
		// before this point) can leak a file behind.
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}

	if !done {
		setOnlineDownloadTaskFailed(taskID, fmt.Errorf("所有启用音源均解析或下载失败: %s", strings.Join(attemptErrors, "; ")))
	}
}

// markServerTaskTerminal maps a context error into the appropriate
// terminal state (paused / canceled / failed) for a server-mode task.
// It mirrors the legacy behavior previously inlined into
// runOnlineServerDownloadTask.
func markServerTaskTerminal(taskID string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		task, ok := getOnlineDownloadTaskPointer(taskID)
		if !ok {
			return
		}
		if task.PauseWanted {
			updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
				t.Status = "paused"
				t.Speed = 0
				t.CancelFunc = nil
			})
			return
		}
		updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
			t.Status = "canceled"
			t.Speed = 0
			t.CancelFunc = nil
		})
		return
	}
	setOnlineDownloadTaskFailed(taskID, err)
}

// buildOnlineDownloadTaskSongInfo reconstructs a map suitable for
// resolveOnlineDownloadURLWithProgress from the fields we persist on
// onlineDownloadTask. Today the server-side task only keeps Source /
// Quality / Title / Artist, so the rest of the script's expected music
// info (name, singer, songmid, ...) is best-effort derived from those
// fields plus any other keys that ended up on the struct via the
// initial /online/server/start payload.
func buildOnlineDownloadTaskSongInfo(task *onlineDownloadTask) map[string]any {
	out := map[string]any{
		"name":    task.Title,
		"singer":  task.Artist,
		"source":  task.Source,
		"quality": task.Quality,
		"id":      task.ID,
	}
	return out
}

func downloadOnlineServerTaskToPath(ctx context.Context, taskID string) (*fetchedOnlineTempFile, error) {
	task, ok := getOnlineDownloadTaskPointer(taskID)
	if !ok {
		return nil, fmt.Errorf("task not found")
	}

	cookieJar := map[string]string{}
	seedCookieJar(cookieJar, task.Headers)

	startOffset := int64(0)
	if stat, err := os.Stat(task.TempPath); err == nil {
		startOffset = stat.Size()
	}

	if err := os.MkdirAll(filepath.Dir(task.TempPath), 0o755); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(task.TempPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: 0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	currentURL := task.ResolvedURL
	resp, err := doOnlineDownloadRequestWithRedirect(ctx, client, currentURL, task.Headers, cookieJar, startOffset)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download upstream returned %d", resp.StatusCode)
	}

	if startOffset > 0 && resp.StatusCode == http.StatusOK {
		if err := file.Truncate(0); err != nil {
			return nil, err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		startOffset = 0
	}

	total := int64(0)
	if resp.ContentLength > 0 {
		total = startOffset + resp.ContentLength
	}

	written := startOffset
	lastTick := time.Now()
	lastBytes := startOffset
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := file.Write(buf[:n]); writeErr != nil {
				return nil, writeErr
			}
			written += int64(n)
			now := time.Now()
			delta := now.Sub(lastTick)
			speed := int64(0)
			if delta >= 600*time.Millisecond {
				speed = int64(float64(written-lastBytes) / delta.Seconds())
				if speed < 0 {
					speed = 0
				}
				lastTick = now
				lastBytes = written
			}
			updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
				t.Received = written
				t.Total = total
				if total > 0 {
					p := int((written * 100) / total)
					if p > 99 {
						p = 99
					}
					if p < 0 {
						p = 0
					}
					t.Progress = p
				}
				if speed > 0 {
					t.Speed = speed
				}
			})
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}

	if written <= 0 {
		return nil, fmt.Errorf("upstream returned empty audio payload")
	}
	if err := validateOnlineDownloadedAudio(task.TempPath, resp.Header.Get("Content-Type"), written); err != nil {
		_ = os.Remove(task.TempPath)
		return nil, err
	}

	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(task.TempPath, task.FilePath); err != nil {
		return nil, err
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return &fetchedOnlineTempFile{
		FilePath:    task.FilePath,
		FileName:    filepath.Base(task.FilePath),
		ContentType: contentType,
		Size:        written,
	}, nil
}

func doOnlineDownloadRequestWithRedirect(
	ctx context.Context,
	client *http.Client,
	initialURL string,
	resolveHeaders map[string]string,
	cookieJar map[string]string,
	startOffset int64,
) (*http.Response, error) {
	currentURL := initialURL
	for i := 0; i < 6; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return nil, err
		}
		for key, value := range resolveHeaders {
			k := strings.TrimSpace(key)
			if k == "" || strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
				continue
			}
			req.Header.Set(k, value)
		}
		if startOffset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startOffset))
		}
		if cookieHeader := cookieJarHeader(cookieJar); cookieHeader != "" {
			req.Header.Set("Cookie", cookieHeader)
		}
		if parsed, parseErr := url.Parse(currentURL); parseErr == nil {
			if req.Header.Get("Referer") == "" {
				req.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host)
			}
			if req.Header.Get("Origin") == "" {
				req.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
			}
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		updateCookieJar(cookieJar, resp.Header.Values("Set-Cookie"))

		if resp.StatusCode == http.StatusMovedPermanently ||
			resp.StatusCode == http.StatusFound ||
			resp.StatusCode == http.StatusSeeOther ||
			resp.StatusCode == http.StatusTemporaryRedirect ||
			resp.StatusCode == http.StatusPermanentRedirect {
			location := strings.TrimSpace(resp.Header.Get("Location"))
			resp.Body.Close()
			if location == "" {
				return nil, fmt.Errorf("redirect without location")
			}
			nextURL, parseErr := url.Parse(location)
			if parseErr != nil {
				return nil, parseErr
			}
			if !nextURL.IsAbs() {
				baseURL, baseErr := url.Parse(currentURL)
				if baseErr != nil {
					return nil, baseErr
				}
				nextURL = baseURL.ResolveReference(nextURL)
			}
			currentURL = nextURL.String()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("too many redirects")
}

func uniqueOnlineDownloadPath(dir string, fileName string) string {
	base := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	ext := filepath.Ext(fileName)
	candidate := filepath.Join(dir, fileName)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	for i := 2; i < 10000; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext))
}

func getOnlineDownloadTaskPointer(id string) (*onlineDownloadTask, bool) {
	onlineDownloadTasks.RLock()
	defer onlineDownloadTasks.RUnlock()
	task, ok := onlineDownloadTasks.items[id]
	if !ok {
		return nil, false
	}
	return task, true
}

// isActiveServerDownloadStatus is the single source of truth for
// "this task is still doing work, count it as active". The same
// predicate is used by both the badge counter in the top nav and
// the "in flight" chip in the Download_list panel, so a state that
// we ever introduce a new variant of (e.g. "verifying") only needs
// to be added in one place. The set is intentionally conservative:
// anything in {queued, resolving, downloading} is in flight; once
// a task reaches failed / completed / paused / canceled we stop
// counting it so the badge resets cleanly.
func isActiveServerDownloadStatus(status string) bool {
	switch status {
	case "queued", "resolving", "downloading":
		return true
	}
	return false
}

func listOnlineServerDownloadTasks() onlineServerDownloadTasksResponse {
	onlineDownloadTasks.RLock()
	defer onlineDownloadTasks.RUnlock()

	resp := onlineServerDownloadTasksResponse{}
	tasks := make([]onlineServerDownloadTaskView, 0)
	progressSum := 0
	for _, task := range onlineDownloadTasks.items {
		if task.Mode != "server" {
			continue
		}
		view := onlineServerDownloadTaskView{
			ID:         task.ID,
			Title:      task.Title,
			Artist:     task.Artist,
			Source:     task.Source,
			SourceName: task.SourceName,
			Quality:    task.Quality,
			Status:     task.Status,
			Progress:   task.Progress,
			Received:   task.Received,
			Total:      task.Total,
			Speed:      task.Speed,
			Error:      task.Error,
		}
		tasks = append(tasks, view)
		progressSum += task.Progress
		// ActiveCount includes every task that hasn't reached a
		// terminal state yet — queued (waiting for a free worker),
		// resolving (asking the source script for a URL), and
		// downloading (streaming bytes to disk). The badge in the
		// top nav uses this so the user sees "1 in flight" the
		// moment they click download, not only once the file is
		// halfway written. Terminal states (completed / failed /
		// paused / canceled) do NOT count.
		if isActiveServerDownloadStatus(task.Status) {
			resp.ActiveCount++
		}
		if task.Status == "downloading" {
			resp.TotalSpeed += task.Speed
		}
	}
	resp.Tasks = tasks
	resp.TaskCount = len(tasks)
	if len(tasks) > 0 {
		resp.TotalProgress = progressSum / len(tasks)
	}
	if resp.TotalProgress < 0 {
		resp.TotalProgress = 0
	}
	if resp.TotalProgress > 100 {
		resp.TotalProgress = 100
	}
	resp.TotalSpeedText = humanOnlineSpeed(resp.TotalSpeed)
	return resp
}

func humanOnlineSpeed(bytesPerSec int64) string {
	if bytesPerSec <= 0 {
		return "0 B/s"
	}
	v := float64(bytesPerSec)
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func toggleOnlineServerDownloadTask(taskID string) (string, error) {
	task, ok := getOnlineDownloadTaskPointer(taskID)
	if !ok || task.Mode != "server" {
		return "", fmt.Errorf("task not found")
	}

	switch task.Status {
	case "downloading", "resolving":
		updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
			t.PauseWanted = true
			t.Speed = 0
			if t.CancelFunc != nil {
				t.CancelFunc()
			}
		})
		return "paused", nil
	case "paused", "failed", "canceled":
		updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
			t.PauseWanted = false
			t.Error = ""
			if t.Progress < 0 {
				t.Progress = 0
			}
		})
		go runOnlineServerDownloadTask(taskID)
		return "resumed", nil
	default:
		return "", fmt.Errorf("task is not pausable")
	}
}

func retryAllOnlineServerDownloadTasks() {
	onlineDownloadTasks.RLock()
	ids := make([]string, 0)
	for _, task := range onlineDownloadTasks.items {
		if task.Mode == "server" && (task.Status == "failed" || task.Status == "paused" || task.Status == "canceled") {
			ids = append(ids, task.ID)
		}
	}
	onlineDownloadTasks.RUnlock()

	for _, id := range ids {
		_, _ = toggleOnlineServerDownloadTask(id)
	}
}

func cancelAllOnlineServerDownloadTasks() {
	onlineDownloadTasks.RLock()
	ids := make([]string, 0)
	for _, task := range onlineDownloadTasks.items {
		if task.Mode == "server" && (task.Status == "downloading" || task.Status == "paused" || task.Status == "failed" || task.Status == "queued" || task.Status == "resolving") {
			ids = append(ids, task.ID)
		}
	}
	onlineDownloadTasks.RUnlock()

	for _, id := range ids {
		updateOnlineDownloadTask(id, func(t *onlineDownloadTask) {
			t.PauseWanted = false
			t.Status = "canceled"
			t.Speed = 0
			if t.CancelFunc != nil {
				t.CancelFunc()
				t.CancelFunc = nil
			}
		})
	}
}

func clearCompletedOnlineServerDownloadTasks() {
	onlineDownloadTasks.Lock()
	removed := false
	for id, task := range onlineDownloadTasks.items {
		if task.Mode == "server" && task.Status == "completed" {
			delete(onlineDownloadTasks.items, id)
			removed = true
		}
	}
	onlineDownloadTasks.Unlock()
	if removed {
		broadcastDownloadTaskChange()
	}
}

// clearFailedOnlineServerDownloadTasks removes all server-mode tasks
// that ended up in a terminal "no progress" state (failed, paused,
// canceled), and also deletes their on-disk .part temp files. In-flight
// downloading tasks are left alone.
func clearFailedOnlineServerDownloadTasks() {
	terminal := map[string]bool{
		"failed":   true,
		"paused":   true,
		"canceled": true,
	}

	// First pass: collect temp paths to clean up *after* releasing the
	// lock. We hold the write lock briefly, but never touch the
	// filesystem while holding it (other goroutines may also want the
	// lock and we don't want file I/O to gate them).
	type removedTask struct {
		id       string
		tempPath string
	}
	onlineDownloadTasks.Lock()
	removed := make([]removedTask, 0)
	for id, task := range onlineDownloadTasks.items {
		if task.Mode == "server" && terminal[task.Status] {
			removed = append(removed, removedTask{id: id, tempPath: task.TempPath})
			delete(onlineDownloadTasks.items, id)
		}
	}
	onlineDownloadTasks.Unlock()

	for _, r := range removed {
		if r.tempPath != "" {
			_ = os.Remove(r.tempPath)
		}
	}

	if len(removed) > 0 {
		broadcastDownloadTaskChange()
	}
}

// startStallDetector launches a single background goroutine that
// periodically scans for download tasks that have been "downloading"
// with zero bytes received for longer than onlineDownloadTaskStallTimeout.
// Such tasks are treated as dead-link failures and moved to status
// "failed" — their in-flight ctx is canceled and their .part file is
// deleted. The check is gated on Received == 0, so a slow but
// progressing download is never killed.
func startStallDetector() {
	startStallDetectorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(onlineDownloadTaskStallScanInterval)
			defer ticker.Stop()
			for range ticker.C {
				detectAndFailStalledTasks()
			}
		}()
	})
}

// detectAndFailStalledTasks is the body of the stall detector. It is
// also exported (lowercase, same package) for unit tests that drive it
// directly without waiting for the ticker.
func detectAndFailStalledTasks() {
	now := time.Now()

	// Snapshot the candidate IDs under the read lock, then operate on
	// them under write locks (one per task). This keeps the read lock
	// window short and avoids holding any lock across the ctx.Cancel /
	// os.Remove calls below.
	type candidate struct {
		id         string
		createdAt  time.Time
		cancelFunc context.CancelFunc
		tempPath   string
	}

	onlineDownloadTasks.RLock()
	candidates := make([]candidate, 0)
	for id, task := range onlineDownloadTasks.items {
		// Cover both server-mode and browser-mode download tasks. We
		// also accept the empty string for backwards compatibility with
		// tasks created before the Mode field was added.
		if task.Mode != "server" && task.Mode != "browser" && task.Mode != "" {
			continue
		}
		if task.Status != "downloading" && task.Status != "resolving" {
			continue
		}
		if task.Received > 0 {
			continue
		}
		if now.Sub(task.CreatedAt) <= onlineDownloadTaskStallTimeout {
			continue
		}
		candidates = append(candidates, candidate{
			id:         id,
			createdAt:  task.CreatedAt,
			cancelFunc: task.CancelFunc,
			tempPath:   task.TempPath,
		})
	}
	onlineDownloadTasks.RUnlock()

	for _, c := range candidates {
		if c.cancelFunc != nil {
			c.cancelFunc()
		}
		updateOnlineDownloadTask(c.id, func(t *onlineDownloadTask) {
			t.Status = "failed"
			t.Error = fmt.Sprintf("Download stalled: no bytes received within %s (likely dead link)", onlineDownloadTaskStallTimeout)
			t.Speed = 0
			t.CancelFunc = nil
		})
		if c.tempPath != "" {
			_ = os.Remove(c.tempPath)
		}
	}

	if len(candidates) > 0 {
		broadcastDownloadTaskChange()
	}
}

func resolveOnlineDownloadURL(ctx context.Context, songSource string, songInfo map[string]any, quality string) (string, string, map[string]string, error) {
	sources, err := loadEnabledSourcesForSong(songSource)
	if err != nil {
		return "", "", nil, err
	}
	if len(sources) == 0 {
		return "", "", nil, fmt.Errorf("未找到支持 %s 的启用音源脚本", songSource)
	}

	var attemptErrors []string
	for _, source := range sources {
		url, headers, name, resolveErr := resolveOnlineDownloadURLWithProgress(ctx, source, songSource, songInfo, quality)
		if resolveErr == nil {
			return url, name, headers, nil
		}
		attemptErrors = append(attemptErrors, fmt.Sprintf("%s: %v", source.Name, resolveErr))
		if ctx.Err() != nil {
			break
		}
	}
	return "", "", nil, fmt.Errorf("%s", strings.Join(attemptErrors, "; "))
}

// loadEnabledSourcesForSong returns the enabled source scripts that
// support the given song source, in the user-configured priority order.
func loadEnabledSourcesForSong(songSource string) ([]onlineSource, error) {
	all, err := loadOnlineSources()
	if err != nil {
		return nil, err
	}
	all = normalizeOnlineSourcesOrder(all)
	out := make([]onlineSource, 0, len(all))
	for _, s := range all {
		if s.Enabled && containsString(s.SupportedSources, songSource) {
			out = append(out, s)
		}
	}
	return out, nil
}

// resolveOnlineDownloadURLWithProgress invokes a single source script up to
// onlineDownloadScriptRetries times until it returns a resolved URL.
// Callers that want to try multiple scripts in sequence (with a download
// phase in between) should iterate loadEnabledSourcesForSong themselves
// and invoke this helper for each candidate.
func resolveOnlineDownloadURLWithProgress(
	ctx context.Context,
	source onlineSource,
	songSource string,
	songInfo map[string]any,
	quality string,
) (string, map[string]string, string, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return "", nil, "", fmt.Errorf("node not available")
	}

	scriptPath := filepath.Join(onlineScriptsDir(), source.ID)
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		return "", nil, "", fmt.Errorf("读取脚本失败: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= onlineDownloadScriptRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, onlineDownloadScriptTimeout)
		url, headers, resolveErr := executeOnlineDownloadScript(attemptCtx, onlineDownloadResolveInput{
			Script:      string(scriptContent),
			AllowUnsafe: source.AllowUnsafeVM,
			Source:      songSource,
			MusicInfo:   songInfo,
			Quality:     quality,
		})
		cancel()
		if resolveErr == nil {
			return url, headers, source.Name, nil
		}
		lastErr = fmt.Errorf("第%d次: %w", attempt, resolveErr)
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no attempts made")
	}
	return "", nil, "", lastErr
}
func executeOnlineDownloadScript(ctx context.Context, input onlineDownloadResolveInput) (string, map[string]string, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return "", nil, err
	}

	cmd := exec.CommandContext(ctx, "node", "-e", nodeOnlineDownloadScript)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(), fmt.Sprintf("ND_TIMEOUT_MS=%d", onlineDownloadScriptTimeout.Milliseconds()))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", nil, fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
		}
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, err
	}

	var result onlineDownloadResolveResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return "", nil, fmt.Errorf("invalid node response: %w", err)
	}
	if !result.Success {
		return "", nil, fmt.Errorf("%s", strings.TrimSpace(result.Error))
	}
	if strings.TrimSpace(result.URL) == "" {
		return "", nil, fmt.Errorf("empty resolved url")
	}
	return strings.TrimSpace(result.URL), result.Headers, nil
}

func normalizeOnlineDownloadSongInfo(songInfo map[string]any) map[string]any {
	normalized := cloneStringMap(songInfo)
	meta := mapValue(normalized["meta"])
	for key, value := range meta {
		if _, exists := normalized[key]; !exists {
			normalized[key] = value
		}
	}

	if value := stringValueFromMap(meta, "songId"); value != "" && stringValue(normalized["songmid"]) == "" {
		normalized["songmid"] = value
	}
	if value := stringValueFromMap(meta, "picUrl"); value != "" && stringValue(normalized["img"]) == "" {
		normalized["img"] = value
	}
	if value := mapValue(meta["qualitys"]); len(value) > 0 && mapValue(normalized["types"]) == nil {
		normalized["types"] = value
	}
	if value := mapValue(meta["_qualitys"]); len(value) > 0 && mapValue(normalized["_types"]) == nil {
		normalized["_types"] = value
	}

	for _, key := range []string{"hash", "albumId", "copyrightId", "lrcUrl", "mrcUrl", "trcUrl", "strMediaMid", "albumMid"} {
		if stringValue(normalized[key]) != "" {
			continue
		}
		if value := stringValueFromMap(meta, key); value != "" {
			normalized[key] = value
		}
	}

	// Ensure songmid is set: lxmusic custom scripts universally use songmid as the primary ID.
	// Our navidrome search result stores the song ID in the 'id' field, so copy it over.
	if stringValue(normalized["songmid"]) == "" {
		if id := stringValue(normalized["id"]); id != "" {
			normalized["songmid"] = id
		}
	}

	// KuWo: MUSICRID may have 'MUSIC_' prefix; strip it to get the numeric ID expected by scripts.
	if stringValue(normalized["source"]) == "kw" {
		if sm := stringValue(normalized["songmid"]); strings.HasPrefix(sm, "MUSIC_") {
			normalized["songmid"] = strings.TrimPrefix(sm, "MUSIC_")
		}
		if id := stringValue(normalized["id"]); strings.HasPrefix(id, "MUSIC_") {
			normalized["id"] = strings.TrimPrefix(id, "MUSIC_")
		}
	}

	// KuGou: scripts use 'hash' as primary ID; copy from id when missing.
	if stringValue(normalized["source"]) == "kg" && stringValue(normalized["hash"]) == "" {
		if id := stringValue(normalized["id"]); id != "" {
			normalized["hash"] = id
		}
	}

	return normalized
}

func cloneStringMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func mapValue(value any) map[string]any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func stringValueFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	return stringValue(values[key])
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func bestOnlineDownloadQuality(songInfo map[string]any) string {
	qualitys := mapValue(songInfo["qualitys"])
	for _, key := range []string{"master", "flac24bit", "ape", "flac", "320k", "128k"} {
		if qualitys != nil {
			if raw, ok := qualitys[key]; ok {
				if flag, ok := raw.(bool); ok && flag {
					return key
				}
			}
		}
	}
	return "128k"
}

// resolveNameTemplateToken returns the song-info value the user wants
// for the given chip token, or "" if the token is unknown or the
// song info doesn't carry that field. The mapping is:
//
//	歌名   → songInfo["name"]
//	歌手   → songInfo["singer"]
//	专辑   → songInfo["albumName"]
//	来源   → sourceLabel(songInfo["source"])  (wy→网易, etc.)
//	音质   → the explicit quality argument
func resolveNameTemplateToken(token string, songInfo map[string]any, quality string) string {
	switch token {
	case "歌名":
		return stringValue(songInfo["name"])
	case "歌手":
		return stringValue(songInfo["singer"])
	case "专辑":
		return stringValue(songInfo["albumName"])
	case "来源":
		return sourceLabel(stringValue(songInfo["source"]))
	case "音质":
		return quality
	default:
		return ""
	}
}

// sourceLabel maps a song-info source code (wy/tx/kg/kw/mg) to the
// short label users see in the UI. Falls back to the raw code when
// the source is one we don't recognize (e.g. a custom JS source).
func sourceLabel(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "wy":
		return "网易"
	case "tx":
		return "QQ"
	case "kg":
		return "酷狗"
	case "kw":
		return "酷我"
	case "mg":
		return "咪咕"
	}
	return source
}

// onlineDownloadFileName composes the on-disk file name for a
// downloaded song. The user-configurable `nameTemplate` controls the
// order of the parts (e.g. [歌名, 音质, 歌手] → "海屿你-320k-马也_Crabbit"),
// separated by '-'. Tokens whose value is missing are silently
// dropped so the filename never has dangling separators. An empty
// or invalid template falls back to the default [歌名, 歌手] order.
//
// The file extension is taken from the resolved URL's path (or
// Content-Type detection downstream) and is NOT part of the
// template — that lets the user keep the same nameTemplate across
// different qualities / sources without leaking the bitrate info
// into the extension.
func onlineDownloadFileName(songInfo map[string]any, quality string, nameTemplate []string, resolvedURL string) string {
	template := sanitizeNameTemplate(nameTemplate)
	if len(template) == 0 {
		template = append([]string{}, defaultOnlineNameTemplate...)
	}

	parts := make([]string, 0, len(template))
	for _, token := range template {
		value := resolveNameTemplateToken(token, songInfo, quality)
		if value == "" {
			continue
		}
		parts = append(parts, sanitizeOnlineFileName(value))
	}
	if len(parts) == 0 {
		// Last-ditch fallback: at least give the file a name so it
		// doesn't end up as ".mp3".
		parts = append(parts, sanitizeOnlineFileName(stringValue(songInfo["name"])))
		if parts[0] == "" {
			parts[0] = "download"
		}
	}
	base := strings.Join(parts, "-")

	// Parse the URL to extract only the path component before
	// getting the extension; path.Ext on a raw URL string would
	// include the query string in the extension.
	urlExt := ""
	if parsed, err := url.Parse(resolvedURL); err == nil {
		urlExt = path.Ext(parsed.Path)
	}
	ext := strings.ToLower(strings.TrimPrefix(urlExt, "."))
	// Only accept well-known audio/container extensions to avoid
	// leaking API path segments.
	knownExts := map[string]bool{"mp3": true, "flac": true, "ape": true, "m4a": true, "aac": true, "ogg": true, "wav": true, "wma": true, "opus": true}
	if !knownExts[ext] {
		// Leave extension-less; fetchOnlineDownloadToTempFile /
		// downloadOnlineServerTaskToPath will add it from Content-Type.
		return base
	}
	return base + "." + ext
}

// sanitizeNameTemplate returns the input template with unknown
// tokens filtered out, duplicates removed, and the original order
// preserved. It mirrors sanitizeOnlineNameTemplate in online_source.go
// (frontend uses the same token set) so a server task built from a
// frontend request can't accidentally include garbage tokens that
// would make the filename look weird.
func sanitizeNameTemplate(in []string) []string {
	allowed := map[string]bool{
		"歌名": true, "歌手": true, "专辑": true, "来源": true, "音质": true,
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		token := strings.TrimSpace(raw)
		if !allowed[token] || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

func sanitizeOnlineFileName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	name = strings.TrimSpace(replacer.Replace(name))
	if name == "" {
		return "download"
	}
	return name
}

func proxyOnlineDownloadWithEmbed(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	targetURL string,
	fileName string,
	resolveHeaders map[string]string,
	embedHook func(tempPath string, contentType string, size int64),
) error {
	result, err := fetchOnlineDownloadToTempFile(ctx, targetURL, fileName, resolveHeaders, nil)
	if err != nil {
		return err
	}
	defer os.Remove(result.FilePath)

	if embedHook != nil {
		embedHook(result.FilePath, result.ContentType, result.Size)
	}

	serveFile, err := os.Open(result.FilePath)
	if err != nil {
		return err
	}
	defer serveFile.Close()

	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Content-Disposition", contentDispositionValue(result.FileName))
	w.Header().Set("Content-Length", strconv.FormatInt(result.Size, 10))
	w.Header().Set("X-Online-Download-Mode", "buffered-temp-file")
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, serveFile)
	return err
}

func createOnlineDownloadTask(nameTemplate []string) string {
	now := time.Now()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		_ = err // fallback to sequential ID if crypto/rand fails
	}
	id := fmt.Sprintf("odl_%d_%d", now.UnixNano(), int64(buf[0])<<56|int64(buf[1])<<48|int64(buf[2])<<40|int64(buf[3])<<32|int64(buf[4])<<24|int64(buf[5])<<16|int64(buf[6])<<8|int64(buf[7]))

	onlineDownloadTasks.Lock()
	_ = cleanupExpiredOnlineDownloadTasksLocked(now)
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:           id,
		Mode:         "browser",
		Status:       "queued",
		Progress:     0,
		NameTemplate: append([]string{}, nameTemplate...),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	onlineDownloadTasks.Unlock()
	broadcastDownloadTaskChange()
	return id
}

func getOnlineDownloadTask(id string) (onlineDownloadTask, bool) {
	onlineDownloadTasks.RLock()
	defer onlineDownloadTasks.RUnlock()
	task, ok := onlineDownloadTasks.items[id]
	if !ok {
		return onlineDownloadTask{}, false
	}
	return *task, true
}

func updateOnlineDownloadTask(id string, updater func(task *onlineDownloadTask)) {
	onlineDownloadTasks.Lock()
	task, ok := onlineDownloadTasks.items[id]
	if !ok {
		onlineDownloadTasks.Unlock()
		return
	}
	updater(task)
	task.UpdatedAt = time.Now()
	onlineDownloadTasks.Unlock()
	// Notify SSE subscribers after the write lock is released, so
	// concurrent subscribers can re-acquire the read lock and snapshot
	// the post-mutation state without contending with us.
	broadcastDownloadTaskChange()
}

func deleteOnlineDownloadTask(id string) {
	onlineDownloadTasks.Lock()
	task, ok := onlineDownloadTasks.items[id]
	if ok {
		if task.FilePath != "" {
			_ = os.Remove(task.FilePath)
		}
		delete(onlineDownloadTasks.items, id)
	}
	onlineDownloadTasks.Unlock()
	if ok {
		broadcastDownloadTaskChange()
	}
}

// cleanupExpiredOnlineDownloadTasksLocked removes tasks whose
// UpdatedAt is older than onlineDownloadTaskTTL. Returns the number
// of tasks removed so the caller can broadcast a change event after
// releasing the write lock.
func cleanupExpiredOnlineDownloadTasksLocked(now time.Time) int {
	removed := 0
	for id, task := range onlineDownloadTasks.items {
		if now.Sub(task.UpdatedAt) > onlineDownloadTaskTTL {
			if task.FilePath != "" {
				_ = os.Remove(task.FilePath)
			}
			delete(onlineDownloadTasks.items, id)
			removed++
		}
	}
	return removed
}

func setOnlineDownloadTaskFailed(taskID string, err error) {
	updateOnlineDownloadTask(taskID, func(task *onlineDownloadTask) {
		task.Status = "failed"
		task.Error = err.Error()
	})
}

func runOnlineDownloadTask(taskID string, songSource string, normalized map[string]any, quality string) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	candidates, err := loadEnabledSourcesForSong(songSource)
	if err != nil {
		setOnlineDownloadTaskFailed(taskID, fmt.Errorf("加载音源失败: %w", err))
		return
	}
	if len(candidates) == 0 {
		setOnlineDownloadTaskFailed(taskID, fmt.Errorf("未找到支持 %s 的启用音源脚本", songSource))
		return
	}

	// markResolving transitions the task into the "resolving" state with
	// the given candidate's display name. Called once per candidate so the
	// UI can show "ikun[赞助]… 解析中…" / "wyymusic… 解析中…" as we cycle
	// through fallbacks.
	markResolving := func(displayName string) {
		updateOnlineDownloadTask(taskID, func(task *onlineDownloadTask) {
			task.Status = "resolving"
			task.Progress = 0
			task.Received = 0
			task.Total = 0
			task.SourceName = displayName
		})
		broadcastDownloadTaskChange()
	}

	// Pre-populate the resolving state with the first candidate so the
	// very first progress poll already shows a meaningful name.
	markResolving(candidates[0].Name)

	var attemptErrors []string
	for i, candidate := range candidates {
		// Update the displayed candidate name each time we move on to the
		// next resolver. This covers the post-fallback "downloading just
		// failed, going back to resolving" transition cleanly.
		markResolving(candidate.Name)

		resolvedURL, resolvedHeaders, sourceName, resolveErr := resolveOnlineDownloadURLWithProgress(ctx, candidate, songSource, normalized, quality)
		if resolveErr != nil {
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s 解析失败: %v", sourceName, resolveErr))
			log.Info(ctx, "Online browser download resolve failed, trying next candidate", "task", taskID, "candidate", sourceName, "err", resolveErr)
			if ctx.Err() != nil {
				setOnlineDownloadTaskFailed(taskID, ctx.Err())
				return
			}
			// Reset progress for the next candidate.
			continue
		}

		// Re-read the task here so the file name uses the *current*
		// chip template — it may have been updated mid-download if the
		// user reordered chips in the settings panel between resolves.
		var fileName string
		if cur, ok := getOnlineDownloadTaskPointer(taskID); ok {
			fileName = onlineDownloadFileName(normalized, quality, cur.NameTemplate, resolvedURL)
		} else {
			fileName = onlineDownloadFileName(normalized, quality, nil, resolvedURL)
		}
		updateOnlineDownloadTask(taskID, func(task *onlineDownloadTask) {
			task.Status = "downloading"
			task.Progress = 0
			task.Received = 0
			task.Total = 0
			// Lock in the candidate we are about to stream from.
			task.SourceName = sourceName
		})
		broadcastDownloadTaskChange()

		result, fetchErr := fetchOnlineDownloadToTempFile(ctx, resolvedURL, fileName, resolvedHeaders, func(received, total int64) {
			updateOnlineDownloadTask(taskID, func(task *onlineDownloadTask) {
				task.Status = "downloading"
				task.Received = received
				task.Total = total
				if total > 0 {
					p := int((received * 100) / total)
					if p > 99 {
						p = 99
					}
					if p < 0 {
						p = 0
					}
					task.Progress = p
				} else {
					task.Progress = 0
				}
			})
		})
		if fetchErr == nil {
			// Best-effort cover / metadata / lyrics embed. Failure
			// here is logged and does not affect the task status —
			// the user always sees a playable file.
			//
			// onlineEmbedEnabled reads the user's settings.json
			// embedMode field via the embed entry point's own
			// onlineEmbedMode() call. We log a "before" trace
			// here so the user can grep their navidrome.log for
			// [EMBED] browser-gate-ok / browser-gate-skipped and
			// tell whether the gate passed.
			embedTrace(context.Background(), "browser-gate-check", "task", taskID, "songSource", songSource, "mode", onlineEmbedMode())
			if onlineEmbedEnabled() {
				embedOnlineBrowserTaskWithScript(ctx, taskID, nil, candidate, normalized, songSource, quality, result.FilePath)
			} else {
				embedTrace(context.Background(), "browser-gate-skipped-mode-none", "task", taskID, "songSource", songSource)
			}
			updateOnlineDownloadTask(taskID, func(task *onlineDownloadTask) {
				task.Status = "completed"
				task.Progress = 100
				task.Received = result.Size
				task.Total = result.Size
				task.FilePath = result.FilePath
				task.FileName = result.FileName
				task.ContentType = result.ContentType
			})
			broadcastDownloadTaskChange()
			return
		}

		// Download failed for this candidate. If the user/stall detector
		// canceled us, honor that and bail out.
		if ctx.Err() != nil {
			setOnlineDownloadTaskFailed(taskID, ctx.Err())
			return
		}

		attemptErrors = append(attemptErrors, fmt.Sprintf("%s 下载失败: %v", sourceName, fetchErr))
		log.Warn(ctx, "Online browser download failed, trying next candidate", "task", taskID, "candidate", sourceName, "err", fetchErr)

		// Strip any orphaned .part file from the failed attempt so the
		// next candidate starts clean. fetchOnlineDownloadToTempFile
		// already cleans up its own tempFile on error, so this is
		// defensive only.
		_ = i
	}

	setOnlineDownloadTaskFailed(taskID, fmt.Errorf("所有启用音源均解析或下载失败: %s", strings.Join(attemptErrors, "; ")))
}

type fetchedOnlineTempFile struct {
	FilePath    string
	FileName    string
	ContentType string
	Size        int64
}

// nolint:gocyclo
func fetchOnlineDownloadToTempFile(
	ctx context.Context,
	targetURL string,
	fileName string,
	resolveHeaders map[string]string,
	onProgress func(received int64, total int64),
) (*fetchedOnlineTempFile, error) {
	cookieJar := map[string]string{}
	seedCookieJar(cookieJar, resolveHeaders)

	client := &http.Client{
		Timeout: 0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	currentURL := targetURL
	var resp *http.Response
	var err error
	for i := 0; i < 6; i++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if reqErr != nil {
			return nil, reqErr
		}

		for key, value := range resolveHeaders {
			k := strings.TrimSpace(key)
			if k == "" || strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
				continue
			}
			req.Header.Set(k, value)
		}
		if cookieHeader := cookieJarHeader(cookieJar); cookieHeader != "" {
			req.Header.Set("Cookie", cookieHeader)
		}
		if parsed, parseErr := url.Parse(currentURL); parseErr == nil {
			if req.Header.Get("Referer") == "" {
				req.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host)
			}
			if req.Header.Get("Origin") == "" {
				req.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
			}
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		}

		resp, err = client.Do(req)
		if err != nil {
			return nil, err
		}
		updateCookieJar(cookieJar, resp.Header.Values("Set-Cookie"))

		if resp.StatusCode == http.StatusMovedPermanently ||
			resp.StatusCode == http.StatusFound ||
			resp.StatusCode == http.StatusSeeOther ||
			resp.StatusCode == http.StatusTemporaryRedirect ||
			resp.StatusCode == http.StatusPermanentRedirect {
			location := strings.TrimSpace(resp.Header.Get("Location"))
			resp.Body.Close()
			if location == "" {
				break
			}
			nextURL, parseErr := url.Parse(location)
			if parseErr != nil {
				return nil, parseErr
			}
			if !nextURL.IsAbs() {
				baseURL, baseErr := url.Parse(currentURL)
				if baseErr != nil {
					return nil, baseErr
				}
				nextURL = baseURL.ResolveReference(nextURL)
			}
			currentURL = nextURL.String()
			continue
		}

		break
	}
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("download upstream returned empty response")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download upstream returned %d", resp.StatusCode)
	}

	contentTypeLower := strings.ToLower(resp.Header.Get("Content-Type"))
	contentLength := strings.TrimSpace(resp.Header.Get("Content-Length"))
	if strings.Contains(contentTypeLower, "application/json") {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		preview := strings.TrimSpace(string(peek))
		if len(preview) > 240 {
			preview = preview[:240]
		}
		if strings.Contains(preview, "use_cookie") || strings.Contains(preview, `"code":201`) || strings.Contains(preview, `"msg":"error"`) {
			return nil, fmt.Errorf("upstream requires authenticated cookie, response=%q", preview)
		}
		return nil, fmt.Errorf("upstream returned json instead of audio, response=%q", preview)
	}
	if strings.Contains(contentTypeLower, "text/html") || strings.Contains(contentTypeLower, "application/xhtml") || strings.Contains(contentTypeLower, "php") {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		preview := strings.TrimSpace(string(peek))
		if len(preview) > 180 {
			preview = preview[:180]
		}
		return nil, fmt.Errorf("upstream returned non-audio content-type=%s len=%s preview=%q", contentTypeLower, contentLength, preview)
	}

	tmpFile, err := os.CreateTemp("", "nd-online-download-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()

	total := int64(0)
	if contentLength != "" {
		if n, parseErr := strconv.ParseInt(contentLength, 10, 64); parseErr == nil && n > 0 {
			total = n
		}
	}

	var written int64
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
				_ = tmpFile.Close()
				_ = os.Remove(tmpPath)
				return nil, writeErr
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			return nil, readErr
		}
	}

	if closeErr := tmpFile.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return nil, closeErr
	}
	if written <= 0 {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("upstream returned empty audio payload")
	}
	if err := validateOnlineDownloadedAudio(tmpPath, resp.Header.Get("Content-Type"), written); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}

	if written <= 4096 {
		probe, _ := os.ReadFile(tmpPath)
		preview := strings.TrimSpace(string(probe))
		if len(preview) > 180 {
			preview = preview[:180]
		}
		lowerPreview := strings.ToLower(preview)
		if strings.Contains(lowerPreview, "use_cookie") ||
			strings.Contains(lowerPreview, `"code":201`) ||
			strings.Contains(lowerPreview, "<html") ||
			strings.Contains(lowerPreview, "<?php") {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("upstream returned auth/placeholder content preview=%q", preview)
		}
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		probeFile, openErr := os.Open(tmpPath)
		if openErr == nil {
			defer probeFile.Close()
			peek := make([]byte, 512)
			n, _ := probeFile.Read(peek)
			contentType = http.DetectContentType(peek[:n])
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	finalName := fileName
	if !strings.Contains(finalName, ".") {
		finalName = finalName + detectFileExtension(contentType, resp.Request.URL.Path)
	}

	return &fetchedOnlineTempFile{
		FilePath:    tmpPath,
		FileName:    finalName,
		ContentType: contentType,
		Size:        written,
	}, nil
}

func seedCookieJar(cookieJar map[string]string, resolveHeaders map[string]string) {
	for key, value := range resolveHeaders {
		if strings.EqualFold(strings.TrimSpace(key), "Cookie") {
			for _, part := range strings.Split(value, ";") {
				p := strings.TrimSpace(part)
				if p == "" {
					continue
				}
				eq := strings.Index(p, "=")
				if eq <= 0 {
					continue
				}
				name := strings.TrimSpace(p[:eq])
				val := strings.TrimSpace(p[eq+1:])
				if name != "" {
					cookieJar[name] = val
				}
			}
		}
	}
}

func updateCookieJar(cookieJar map[string]string, setCookies []string) {
	for _, entry := range setCookies {
		if entry == "" {
			continue
		}
		first := strings.Split(entry, ";")[0]
		eq := strings.Index(first, "=")
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(first[:eq])
		val := strings.TrimSpace(first[eq+1:])
		if name == "" {
			continue
		}
		cookieJar[name] = val
	}
}

func cookieJarHeader(cookieJar map[string]string) string {
	if len(cookieJar) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookieJar))
	for k, v := range cookieJar {
		if strings.TrimSpace(k) == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func detectFileExtension(contentType string, requestPath string) string {
	if ext := path.Ext(requestPath); ext != "" {
		return ext
	}
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if mediaType != "" {
		if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
			return exts[0]
		}
	}
	return ".mp3"
}

func validateOnlineDownloadedAudio(filePath, rawContentType string, size int64) error {
	if size <= 0 {
		return fmt.Errorf("upstream returned empty audio payload")
	}
	if size < 512 {
		return fmt.Errorf("downloaded payload too small (%d bytes), not a valid audio file", size)
	}

	contentTypeLower := strings.ToLower(strings.TrimSpace(rawContentType))
	if strings.Contains(contentTypeLower, "application/json") ||
		strings.Contains(contentTypeLower, "text/") ||
		strings.Contains(contentTypeLower, "javascript") ||
		strings.Contains(contentTypeLower, "xml") ||
		strings.Contains(contentTypeLower, "application/x-www-form-urlencoded") {
		return fmt.Errorf("upstream returned non-audio content-type: %s", strings.TrimSpace(rawContentType))
	}

	b, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("cannot read downloaded file: %w", err)
	}
	if len(b) == 0 {
		return fmt.Errorf("downloaded file is empty")
	}

	trimmed := strings.ToLower(strings.TrimSpace(string(b[:minInt(len(b), 256)])))
	if strings.HasPrefix(trimmed, "<") ||
		strings.HasPrefix(trimmed, "{") ||
		strings.HasPrefix(trimmed, "[") ||
		strings.Contains(trimmed, "<!doctype") ||
		strings.Contains(trimmed, "<html") ||
		strings.Contains(trimmed, "<?php") ||
		strings.Contains(trimmed, "use_cookie") {
		return fmt.Errorf("upstream returned placeholder/html/json payload instead of audio")
	}

	head := b
	if len(head) > 16 {
		head = head[:16]
	}
	hasKnownAudioMagic := false
	switch {
	case len(head) >= 3 && bytes.Equal(head[:3], []byte("ID3")):
		hasKnownAudioMagic = true
	case len(head) >= 2 && head[0] == 0xFF && (head[1]&0xE0) == 0xE0:
		hasKnownAudioMagic = true
	case len(head) >= 4 && bytes.Equal(head[:4], []byte("fLaC")):
		hasKnownAudioMagic = true
	case len(head) >= 4 && bytes.Equal(head[:4], []byte("OggS")):
		hasKnownAudioMagic = true
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WAVE")):
		hasKnownAudioMagic = true
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		hasKnownAudioMagic = true
	}

	if !hasKnownAudioMagic && size < 16*1024 {
		return fmt.Errorf("downloaded file does not look like valid audio (size=%d bytes)", size)
	}

	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func contentDispositionValue(fileName string) string {
	escaped := url.PathEscape(fileName)
	return fmt.Sprintf("attachment; filename*=UTF-8''%s", escaped)
}

var _ = chi.NewRouter
