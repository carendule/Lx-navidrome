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
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type onlineServerDownloadTaskView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Source   string `json:"source"`
	Quality  string `json:"quality"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Speed    int64  `json:"speed"`
	Error    string `json:"error,omitempty"`
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
    console: allowUnsafe ? console : { log() {}, info() {}, warn() {}, error() {}, debug() {}, time() {}, timeEnd() {} },
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

		const info = decontextify({
			musicInfo: payload.musicInfo || {},
			quality: payload.quality,
			type: payload.quality,
		});

    // 脚本期望一个对象参数：{ action, source, info }
    let inputData = { action: 'musicUrl', source: payload.source, info: info };
    if (allowUnsafe) {
      inputData = JSON.parse(JSON.stringify(inputData));
    }
    const result = await requestHandler(inputData);
    
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
    process.stdout.write(JSON.stringify({ success: false, error: error && error.message ? error.message : String(error) }));
  }
});
`

func (api *Router) addOnlineDownloadRoutes(r chi.Router) {
	r.Route("/online/download", func(r chi.Router) {
		r.Post("/server/start", api.onlineServerDownloadStart)
		r.Get("/tasks", api.onlineServerDownloadTasks)
		r.Post("/task/{taskID}/toggle", api.onlineServerDownloadToggle)
		r.Post("/tasks/retry", api.onlineServerDownloadRetryAll)
		r.Post("/tasks/cancel", api.onlineServerDownloadCancelAll)
		r.Post("/tasks/clear-completed", api.onlineServerDownloadClearCompleted)
		r.Post("/browser/start", api.onlineBrowserDownloadStart)
		r.Get("/browser/progress/{taskID}", api.onlineBrowserDownloadProgress)
		r.Get("/browser/file/{taskID}", api.onlineBrowserDownloadFile)
		r.Post("/browser", api.onlineBrowserDownload)
	})
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

	taskID := createOnlineDownloadTask()
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
		TaskID:    task.ID,
		Status:    task.Status,
		Progress:  task.Progress,
		Received:  task.Received,
		Total:     task.Total,
		Error:     task.Error,
		FileReady: task.Status == "completed" && task.FilePath != "",
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

	fileName := onlineDownloadFileName(normalized, quality, resolvedURL)
	if err := proxyOnlineDownload(ctx, w, r, resolvedURL, fileName, resolvedHeaders); err != nil {
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

	normalized := normalizeOnlineDownloadSongInfo(req.SongInfo)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	resolvedURL, sourceName, resolvedHeaders, err := resolveOnlineDownloadURL(ctx, songSource, normalized, quality)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
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

	taskID := createOnlineServerDownloadTask(normalized, songSource, sourceName, quality, resolvedURL, resolvedHeaders, downloadDir)
	go runOnlineServerDownloadTask(taskID) //nolint:gosec

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(onlineBrowserDownloadStartResponse{TaskID: taskID})
}

func (api *Router) onlineServerDownloadTasks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listOnlineServerDownloadTasks())
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

func createOnlineServerDownloadTask(
	songInfo map[string]any,
	source string,
	sourceName string,
	quality string,
	resolvedURL string,
	resolvedHeaders map[string]string,
	downloadDir string,
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

	fileName := onlineDownloadFileName(songInfo, quality, resolvedURL)
	if !strings.Contains(fileName, ".") {
		fileName += ".mp3"
	}
	finalPath := uniqueOnlineDownloadPath(downloadDir, fileName)
	tempPath := finalPath + ".part"

	headers := map[string]string{}
	for k, v := range resolvedHeaders {
		headers[k] = v
	}

	onlineDownloadTasks.Lock()
	defer onlineDownloadTasks.Unlock()
	cleanupExpiredOnlineDownloadTasksLocked(now)
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:          id,
		Mode:        "server",
		Title:       title,
		Artist:      artist,
		Source:      source,
		SourceName:  sourceName,
		Quality:     quality,
		Status:      "queued",
		Progress:    0,
		FilePath:    finalPath,
		FileName:    fileName,
		ResolvedURL: resolvedURL,
		Headers:     headers,
		DownloadDir: downloadDir,
		TempPath:    tempPath,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
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

	ctx, cancel := context.WithCancel(context.Background())
	updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
		t.CancelFunc = cancel
		t.PauseWanted = false
		t.Error = ""
		t.Status = "downloading"
		if t.Progress < 0 {
			t.Progress = 0
		}
	})

	result, err := downloadOnlineServerTaskToPath(ctx, taskID)
	if err != nil {
		taskAfter, exists := getOnlineDownloadTaskPointer(taskID)
		if !exists {
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if taskAfter.PauseWanted {
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
		updateOnlineDownloadTask(taskID, func(t *onlineDownloadTask) {
			t.Status = "failed"
			t.Error = err.Error()
			t.Speed = 0
			t.CancelFunc = nil
		})
		return
	}

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
			ID:       task.ID,
			Title:    task.Title,
			Artist:   task.Artist,
			Source:   task.Source,
			Quality:  task.Quality,
			Status:   task.Status,
			Progress: task.Progress,
			Received: task.Received,
			Total:    task.Total,
			Speed:    task.Speed,
			Error:    task.Error,
		}
		tasks = append(tasks, view)
		progressSum += task.Progress
		if task.Status == "downloading" {
			resp.ActiveCount++
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
	defer onlineDownloadTasks.Unlock()
	for id, task := range onlineDownloadTasks.items {
		if task.Mode == "server" && task.Status == "completed" {
			delete(onlineDownloadTasks.items, id)
		}
	}
}

func resolveOnlineDownloadURL(ctx context.Context, songSource string, songInfo map[string]any, quality string) (string, string, map[string]string, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return "", "", nil, fmt.Errorf("node not available")
	}

	sources, err := loadOnlineSources()
	if err != nil {
		return "", "", nil, err
	}
	sources = normalizeOnlineSourcesOrder(sources)

	var attemptErrors []string
	for _, source := range sources {
		if !source.Enabled || !containsString(source.SupportedSources, songSource) {
			continue
		}

		scriptPath := filepath.Join(onlineScriptsDir(), source.ID)
		scriptContent, err := os.ReadFile(scriptPath)
		if err != nil {
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s: 读取脚本失败: %v", source.Name, err))
			continue
		}

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
				return url, source.Name, headers, nil
			}
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s 第%d次: %v", source.Name, attempt, resolveErr))
			if ctx.Err() != nil {
				break
			}
		}
	}

	if len(attemptErrors) == 0 {
		return "", "", nil, fmt.Errorf("未找到支持 %s 的启用音源脚本", songSource)
	}
	return "", "", nil, fmt.Errorf("%s", strings.Join(attemptErrors, "; "))
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

func onlineDownloadFileName(songInfo map[string]any, quality string, resolvedURL string) string {
	name := stringValue(songInfo["name"])
	if name == "" {
		name = "download"
	}
	singer := stringValue(songInfo["singer"])
	base := name
	if singer != "" {
		base = singer + " - " + name
	}
	base = sanitizeOnlineFileName(base)
	if quality != "" {
		base += " [" + sanitizeOnlineFileName(quality) + "]"
	}
	// Parse the URL to extract only the path component before getting the extension;
	// path.Ext on a raw URL string would include the query string in the extension.
	urlExt := ""
	if parsed, err := url.Parse(resolvedURL); err == nil {
		urlExt = path.Ext(parsed.Path)
	}
	ext := strings.ToLower(strings.TrimPrefix(urlExt, "."))
	// Only accept well-known audio/container extensions to avoid leaking API path segments.
	knownExts := map[string]bool{"mp3": true, "flac": true, "ape": true, "m4a": true, "aac": true, "ogg": true, "wav": true, "wma": true, "opus": true}
	if !knownExts[ext] {
		ext = ""
	}
	if ext == "" {
		// Leave extension-less; proxyOnlineDownload will add it from Content-Type.
		return base
	}
	return base + "." + ext
}

func sanitizeOnlineFileName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	name = strings.TrimSpace(replacer.Replace(name))
	if name == "" {
		return "download"
	}
	return name
}

func proxyOnlineDownload(ctx context.Context, w http.ResponseWriter, _ *http.Request, targetURL string, fileName string, resolveHeaders map[string]string) error {
	result, err := fetchOnlineDownloadToTempFile(ctx, targetURL, fileName, resolveHeaders, nil)
	if err != nil {
		return err
	}
	defer os.Remove(result.FilePath)

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

func createOnlineDownloadTask() string {
	now := time.Now()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		_ = err // fallback to sequential ID if crypto/rand fails
	}
	id := fmt.Sprintf("odl_%d_%d", now.UnixNano(), int64(buf[0])<<56|int64(buf[1])<<48|int64(buf[2])<<40|int64(buf[3])<<32|int64(buf[4])<<24|int64(buf[5])<<16|int64(buf[6])<<8|int64(buf[7]))

	onlineDownloadTasks.Lock()
	defer onlineDownloadTasks.Unlock()

	cleanupExpiredOnlineDownloadTasksLocked(now)
	onlineDownloadTasks.items[id] = &onlineDownloadTask{
		ID:        id,
		Status:    "queued",
		Progress:  0,
		CreatedAt: now,
		UpdatedAt: now,
	}
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
	defer onlineDownloadTasks.Unlock()
	task, ok := onlineDownloadTasks.items[id]
	if !ok {
		return
	}
	updater(task)
	task.UpdatedAt = time.Now()
}

func deleteOnlineDownloadTask(id string) {
	onlineDownloadTasks.Lock()
	defer onlineDownloadTasks.Unlock()
	task, ok := onlineDownloadTasks.items[id]
	if ok {
		if task.FilePath != "" {
			_ = os.Remove(task.FilePath)
		}
		delete(onlineDownloadTasks.items, id)
	}
}

func cleanupExpiredOnlineDownloadTasksLocked(now time.Time) {
	for id, task := range onlineDownloadTasks.items {
		if now.Sub(task.UpdatedAt) > onlineDownloadTaskTTL {
			if task.FilePath != "" {
				_ = os.Remove(task.FilePath)
			}
			delete(onlineDownloadTasks.items, id)
		}
	}
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

	updateOnlineDownloadTask(taskID, func(task *onlineDownloadTask) {
		task.Status = "resolving"
		task.Progress = 0
	})

	resolvedURL, _, resolvedHeaders, err := resolveOnlineDownloadURL(ctx, songSource, normalized, quality)
	if err != nil {
		setOnlineDownloadTaskFailed(taskID, err)
		return
	}

	fileName := onlineDownloadFileName(normalized, quality, resolvedURL)
	updateOnlineDownloadTask(taskID, func(task *onlineDownloadTask) {
		task.Status = "downloading"
		task.Progress = 0
	})

	result, err := fetchOnlineDownloadToTempFile(ctx, resolvedURL, fileName, resolvedHeaders, func(received, total int64) {
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
	if err != nil {
		setOnlineDownloadTaskFailed(taskID, err)
		return
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

func contentDispositionValue(fileName string) string {
	escaped := url.PathEscape(fileName)
	return fmt.Sprintf("attachment; filename*=UTF-8''%s", escaped)
}

var _ = chi.NewRouter
