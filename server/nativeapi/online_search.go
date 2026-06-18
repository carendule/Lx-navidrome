package nativeapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/log"
)

// hotSearchCacheData represents the cached hot search data for a source
type hotSearchCacheData struct {
	Timestamp int64    `json:"timestamp"`
	List      []string `json:"list"`
}

// hotSearchCacheDuration defines how long cached data is valid (24 hours)
const hotSearchCacheDuration = 24 * time.Hour

// getHotSearchCachePath returns the file path for a source's hot search cache
func getHotSearchCachePath(source string) string {
	cacheDir := filepath.Join(os.TempDir(), "navidrome_cache")
	// Ensure cache directory exists
	_ = os.MkdirAll(cacheDir, 0755)
	return filepath.Join(cacheDir, fmt.Sprintf("hot_search_%s.json", source))
}

// loadHotSearchCache loads cached hot search data for a source if it exists and is fresh
func loadHotSearchCache(source string) ([]string, bool) {
	cachePath := getHotSearchCachePath(source)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, false
	}

	var cache hotSearchCacheData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, false
	}

	// Check if cache is still valid (within 24 hours)
	if time.Since(time.UnixMilli(cache.Timestamp)) < hotSearchCacheDuration {
		return cache.List, true
	}

	return nil, false
}

// saveHotSearchCache saves hot search data to cache
func saveHotSearchCache(source string, list []string) error {
	cachePath := getHotSearchCachePath(source)
	cache := hotSearchCacheData{
		Timestamp: time.Now().UnixMilli(),
		List:      list,
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(cachePath, data, 0600)
}

// nodeHotSearchScript is an inline Node.js script that fetches hot search terms
// from external music platform APIs, matching lxserver's musicSdk implementations.
const nodeHotSearchScript = `
var https = require('https');
var http = require('http');
var source = process.env.ND_SOURCE || 'wy';
var timeoutMs = Number(process.env.ND_TIMEOUT_MS || '8000');
var debugUrls = [];

function makeRequest(targetUrl, opts) {
  opts = opts || {};
  debugUrls.push('Requesting: ' + targetUrl);
  console.error('DEBUG[' + source + ']: ' + targetUrl);
  return new Promise(function(resolve, reject) {
    var parsed;
    try { parsed = new URL(targetUrl); } catch(e) { return reject(e); }
    var lib = parsed.protocol === 'https:' ? https : http;
    var body = opts.body || null;
    if (body && typeof body !== 'string') {
      body = JSON.stringify(body);
    }
    var headers = Object.assign({}, opts.headers || {});
    if (body) {
      headers['Content-Length'] = Buffer.byteLength(body);
    }
    var port = parsed.port ? Number(parsed.port) : (parsed.protocol === 'https:' ? 443 : 80);
    var reqOpts = {
      hostname: parsed.hostname,
      port: port,
      path: parsed.pathname + parsed.search,
      method: opts.method || 'GET',
      headers: headers,
      rejectUnauthorized: false,
    };
    var req = lib.request(reqOpts, function(res) {
      var data = '';
      res.on('data', function(c) { data += c; });
      res.on('end', function() {
        var b;
        try { b = JSON.parse(data); } catch(e) { b = data; }
        console.error('DEBUG[' + source + ']: status=' + res.statusCode + ', responseLen=' + (data.length || 0));
        resolve({ body: b, statusCode: res.statusCode });
      });
    });
    req.setTimeout(timeoutMs, function() { req.destroy(new Error('request timeout')); });
    req.on('error', reject);
    if (body) req.write(body);
    req.end();
  });
}

function fetchWY() {
  return makeRequest('https://music.163.com/api/search/hot', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      'Referer': 'https://music.163.com/',
      'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36',
    },
    body: 'type=1111&limit=20&total=true&offset=0',
  }).then(function(r) {
    if (r.statusCode !== 200) throw new Error('WY returned ' + r.statusCode);
    var result = (r.body && r.body.result) ? r.body.result : (r.body || {});
    var hots = result.hots || result.hotwords || [];
    console.error('DEBUG[WY]: extracted ' + hots.length + ' hot items');
    return hots.slice(0, 20).map(function(item) {
      return item.first || item.searchWord || String(item);
    }).filter(Boolean);
  });
}

function fetchTX() {
  var bodyStr = JSON.stringify({
    comm: { ct: '19', cv: '1803', guid: '0', tmeAppID: 'qqmusic', uin: '0',
      psrf_access_token_expiresAt: 0, psrf_qqaccess_token: '', psrf_qqopenid: '',
      psrf_qqunionid: '', tmeLoginType: 0, wid: '0' },
    hotkey: {
      method: 'GetHotkeyForQQMusicPC',
      module: 'tencent_musicsoso_hotkey.HotkeyService',
      param: { search_id: '', uin: 0 },
    },
  });
  return makeRequest('https://u.y.qq.com/cgi-bin/musicu.fcg', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Referer': 'https://y.qq.com/portal/player.html',
    },
    body: bodyStr,
  }).then(function(r) {
    if (r.statusCode !== 200 || !r.body || r.body.code !== 0) throw new Error('TX returned ' + r.statusCode + ', code=' + (r.body.code || 'N/A'));
    var vec = (r.body.hotkey && r.body.hotkey.data && r.body.hotkey.data.vec_hotkey) || [];
    console.error('DEBUG[TX]: extracted ' + vec.length + ' hot items');
    return vec.map(function(item) { return item.query; }).filter(Boolean);
  });
}

function fetchKG() {
  return makeRequest(
    'http://gateway.kugou.com/api/v3/search/hot_tab?signature=ee44edb9d7155821412d220bcaf509dd&appid=1005&clientver=10026&plat=0',
    {
      headers: {
        'dfid': '1ssiv93oVqMp27cirf2CvoF1',
        'mid': '156798703528610303473757548878786007104',
        'User-Agent': 'Android9-AndroidPhone-10020-130-0-searchrecommendprotocol-wifi',
        'kg-rc': '1',
      },
    }
  ).then(function(r) {
    if (r.statusCode !== 200 || !r.body || r.body.errcode !== 0) throw new Error('KG failed: status=' + r.statusCode + ', errcode=' + (r.body.errcode || 'N/A'));
    var list = [];
    ((r.body.data && r.body.data.list) || []).forEach(function(item) {
      (item.keywords || []).forEach(function(k) { if (k.keyword) list.push(k.keyword); });
    });
    console.error('DEBUG[KG]: extracted ' + list.length + ' hot items');
    return list.slice(0, 20);
  });
}

function fetchKW() {
  return makeRequest(
    'http://hotword.kuwo.cn/hotword.s?prod=kwplayer_ar_9.3.0.1&corp=kuwo&newver=2&vipver=9.3.0.1&source=kwplayer_ar_9.3.0.1_40.apk&p2p=1&notrace=0&uid=0&plat=kwplayer_ar&rformat=json&encoding=utf8&tabid=1',
    { headers: { 'User-Agent': 'Dalvik/2.1.0 (Linux; U; Android 9;)' } }
  ).then(function(r) {
    if (r.statusCode !== 200 || !r.body || r.body.status !== 'ok') throw new Error('KW failed: status=' + r.statusCode + ', response.status=' + (r.body.status || 'N/A'));
    var items = (r.body.tagvalue || []).map(function(item) { return item.key; }).filter(Boolean).slice(0, 20);
    console.error('DEBUG[KW]: extracted ' + items.length + ' hot items');
    return items;
  });
}

function fetchMG() {
  return makeRequest('http://jadeite.migu.cn:7090/music_search/v3/search/hotword').then(function(r) {
    if (r.statusCode !== 200 || !r.body || r.body.code !== '000000') throw new Error('MG failed: status=' + r.statusCode + ', code=' + (r.body.code || 'N/A'));
    var hotwords = (r.body.data && r.body.data.hotwords && r.body.data.hotwords[0])
      ? r.body.data.hotwords[0].hotwordList : [];
    var songs = hotwords
      .filter(function(item) { return item.resourceType === 'song'; })
      .map(function(item) { return item.word; })
      .filter(Boolean)
      .slice(0, 20);
    console.error('DEBUG[MG]: extracted ' + songs.length + ' hot items');
    return songs;
  });
}

function withRetry(task, retries) {
  return task().catch(function(err) {
    if (retries > 0) return withRetry(task, retries - 1);
    throw err;
  });
}

var fetchers = { wy: fetchWY, tx: fetchTX, kg: fetchKG, kw: fetchKW, mg: fetchMG };
var fn = fetchers[source];
if (!fn) {
  process.stdout.write(JSON.stringify({ success: false, error: 'Unsupported source: ' + source, list: [], debug: debugUrls.join('; ') }));
} else {
  withRetry(fn, 2)
    .then(function(list) {
      process.stdout.write(JSON.stringify({ success: true, list: list || [], debug: debugUrls.join('; ') }));
    })
    .catch(function(err) {
      process.stdout.write(JSON.stringify({ success: false, error: err.message || 'unknown error', list: [], debug: debugUrls.join('; ') }));
    });
}
`

type hotSearchNodeResult struct {
	Success bool     `json:"success"`
	List    []string `json:"list"`
	Error   string   `json:"error,omitempty"`
	Debug   string   `json:"debug,omitempty"`
}

type onlineSearchNodeResult struct {
	Success bool             `json:"success"`
	List    []map[string]any `json:"list"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit,omitempty"`
	Info    map[string]any   `json:"info,omitempty"`
	Error   string           `json:"error,omitempty"`
}

type onlinePlaylistTagsNodeResult struct {
	Success  bool                `json:"success"`
	Tags     []playlistTagGroup  `json:"tags"`
	HotTag   []onlinePlaylistTag `json:"hotTag,omitempty"`
	SortList []map[string]string `json:"sortList,omitempty"`
	Error    string              `json:"error,omitempty"`
}

type onlinePlaylistPlazaNodeResult struct {
	Success bool             `json:"success"`
	List    []map[string]any `json:"list"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// nodeSearchScript executes source-specific search requests and normalizes
// results to a compact shape that the frontend can render consistently.
const nodeSearchScript = `
var https = require('https');
var http = require('http');
var crypto = require('crypto');

var source = process.env.ND_SOURCE || 'wy';
var searchType = process.env.ND_TYPE || 'song';
var keywordB64 = process.env.ND_KEYWORD_B64 || '';
var keyword = '';
try {
  keyword = Buffer.from(keywordB64, 'base64').toString('utf8');
} catch (e) {
  keyword = '';
}
var page = Number(process.env.ND_PAGE || '1');
var limit = Number(process.env.ND_LIMIT || '20');
var sortId = String(process.env.ND_SORT_ID || '').trim();
var timeoutMs = Number(process.env.ND_TIMEOUT_MS || '8000');

function makeRequest(targetUrl, opts) {
  opts = opts || {};
  return new Promise(function(resolve, reject) {
    var parsed;
    try { parsed = new URL(targetUrl); } catch (e) { return reject(e); }
    var lib = parsed.protocol === 'https:' ? https : http;
    var body = opts.body || null;
    var retries = Number(opts.retries == null ? 1 : opts.retries);
    var maxRedirects = Number(opts.maxRedirects == null ? 2 : opts.maxRedirects);
    if (body && typeof body !== 'string') body = JSON.stringify(body);
    var headers = Object.assign({}, opts.headers || {});
    if (body) headers['Content-Length'] = Buffer.byteLength(body);

    var req = lib.request({
      hostname: parsed.hostname,
      port: parsed.port ? Number(parsed.port) : (parsed.protocol === 'https:' ? 443 : 80),
      path: parsed.pathname + parsed.search,
      method: opts.method || 'GET',
      headers: headers,
      rejectUnauthorized: false,
    }, function(res) {
      var data = '';
      res.on('data', function(c) { data += c; });
      res.on('end', function() {
        if ((res.statusCode === 301 || res.statusCode === 302 || res.statusCode === 303 || res.statusCode === 307 || res.statusCode === 308)
          && res.headers && res.headers.location && maxRedirects > 0) {
          var redirectUrl = '';
          try {
            redirectUrl = new URL(res.headers.location, targetUrl).toString();
          } catch (_) {
            redirectUrl = res.headers.location;
          }
          resolve(makeRequest(redirectUrl, Object.assign({}, opts, { maxRedirects: maxRedirects - 1 })));
          return;
        }
        var parsedBody;
        try { parsedBody = JSON.parse(data); } catch (e) { parsedBody = data; }
        resolve({ statusCode: res.statusCode, body: parsedBody });
      });
    });
    req.setTimeout(timeoutMs, function() { req.destroy(new Error('request timeout')); });
    req.on('error', function(err) {
      if (retries > 0) {
        resolve(makeRequest(targetUrl, Object.assign({}, opts, { retries: retries - 1 })));
        return;
      }
      reject(err);
    });
    if (body) req.write(body);
    req.end();
  });
}

function toDurationMs(input) {
  if (typeof input === 'number' && isFinite(input)) {
    if (input > 100000) return Math.floor(input);
    return Math.floor(input * 1000);
  }
  if (typeof input === 'string') {
    if (/^\d+:\d+(?::\d+)?$/.test(input)) {
      var parts = input.split(':').map(function(v) { return Number(v) || 0; });
      var sec = 0;
      while (parts.length) sec = sec * 60 + parts.shift();
      return sec * 1000;
    }
    var n = Number(input);
    if (isFinite(n) && n > 0) return n > 100000 ? Math.floor(n) : Math.floor(n * 1000);
  }
  return 0;
}

function qualityFromFlags(flags) {
  var q = {};
  if (flags.master) q.master = true;
  if (flags.flac24bit) q.flac24bit = true;
  if (flags.ape) q.ape = true;
  if (flags.flac) q.flac = true;
  if (flags.k128) q['128k'] = true;
  if (flags.k320) q['320k'] = true;
  return q;
}

// formatKWPic normalizes the size segment in an already-absolute KuWo URL.
function formatKWPic(url, size) {
  if (!url) return '';
  var t = String(size || 1000);
  return url
    .replace(/(\/star\/albumcover\/)\d+/, '$1' + t)
    .replace(/(pictype=)\d+/, '$1' + t)
    .replace(/(size=)\d+/, '$1' + t);
}

// kwPicUrl builds an absolute, size-normalized KuWo cover URL.
// web_albumpic_short = "120/s4s36/36/xxx.jpg" (embedded size prefix, no leading slash).
// We concat base+shortPath so the regex in formatKWPic merges and replaces the size:
//   "1000" + "120/s4s..." -> "1000120/s4s..." -> regex -> "1000/s4s..."
function kwPicUrl(probAlbumpic, webShortPath, fallback) {
  if (probAlbumpic && /^https?:\/\//.test(probAlbumpic)) return formatKWPic(probAlbumpic);
  if (webShortPath) return formatKWPic('https://img4.kuwo.cn/star/albumcover/1000' + webShortPath);
  if (fallback && /^https?:\/\//.test(fallback)) return formatKWPic(fallback);
  return '';
}

function mgSignature(text) {
  var deviceId = '963B7AA0D21511ED807EE5846EC87D20';
  var signatureMd5 = '6cdc72a439cef99a3418d2a78aa28c73';
  var timestamp = Date.now().toString();
  var sign = crypto.createHash('md5')
    .update(String(text || '') + signatureMd5 + 'yyapp2d16148780a1dcc7408e06336b98cfd50' + deviceId + timestamp)
    .digest('hex');
  return { deviceId: deviceId, timestamp: timestamp, sign: sign };
}

function mgSearchSwitch(type) {
  if (type === 'singer') return '%7B%22song%22%3A0%2C%22album%22%3A0%2C%22singer%22%3A1%2C%22tagSong%22%3A0%2C%22mvSong%22%3A0%2C%22bestShow%22%3A1%2C%22songlist%22%3A0%2C%22lyricSong%22%3A0%7D';
  if (type === 'album') return '%7B%22song%22%3A0%2C%22album%22%3A1%2C%22singer%22%3A0%2C%22tagSong%22%3A0%2C%22mvSong%22%3A0%2C%22bestShow%22%3A1%2C%22songlist%22%3A0%2C%22lyricSong%22%3A0%7D';
  if (type === 'playlist') return '%7B%22song%22%3A0%2C%22album%22%3A0%2C%22singer%22%3A0%2C%22tagSong%22%3A0%2C%22mvSong%22%3A0%2C%22bestShow%22%3A1%2C%22songlist%22%3A1%2C%22lyricSong%22%3A0%7D';
  return '%7B%22song%22%3A1%2C%22album%22%3A0%2C%22singer%22%3A0%2C%22tagSong%22%3A1%2C%22mvSong%22%3A0%2C%22bestShow%22%3A1%2C%22songlist%22%3A0%2C%22lyricSong%22%3A0%7D';
}

function flattenMGResultList(input) {
  var out = [];
  (input || []).forEach(function(item) {
    if (Array.isArray(item)) out = out.concat(item);
    else if (item) out.push(item);
  });
  return out;
}

function mapWYSong(list) {
  return (list || []).map(function(item) {
    var ar = item.ar || item.artists || [];
    var singer = ar.map(function(a) { return a && a.name; }).filter(Boolean).join('/');
    var durationInput = item.dt;
    if (!(typeof durationInput === 'number' && isFinite(durationInput) && durationInput > 0)) {
      durationInput = item.duration;
      // Some WY search payloads expose duration in centiseconds.
      // Example: 24415 => 244.15s (about 04:04), not 24415s.
      if (typeof durationInput === 'number' && isFinite(durationInput) && durationInput >= 10000 && durationInput < 100000) {
        durationInput = durationInput * 10;
      }
    }
    return {
      id: String(item.id || ''),
      name: item.name || '',
      singer: singer,
      albumName: (item.al && item.al.name) || (item.album && item.album.name) || '',
      duration: toDurationMs(durationInput),
      img: (item.al && item.al.picUrl) || (item.album && item.album.picUrl) || '',
      source: 'wy',
      meta: item,
      qualitys: qualityFromFlags({
        flac24bit: !!item.hr,
        flac: !!item.sq,
        k320: !!item.h,
        k128: !!item.l || !!item.m || !!item.h || !!item.sq || !!item.hr,
      }),
    };
  });
}

function mapTXSong(list) {
  return (list || []).map(function(item) {
    var singers = (item.singer || []).map(function(s) { return s && s.name; }).filter(Boolean).join('/');
    var albumMid = item.album && item.album.mid;
    var img = albumMid ? ('https://y.gtimg.cn/music/photo_new/T002R300x300M000' + albumMid + '.jpg') : '';
    var file = item.file || {};
    return {
      id: String(item.mid || item.songmid || ''),
      name: (item.name || item.title || '') + (item.title_extra || ''),
      singer: singers,
      albumName: (item.album && item.album.name) || '',
      duration: toDurationMs(item.interval),
      img: img,
      source: 'tx',
      meta: item,
      qualitys: qualityFromFlags({
        flac24bit: Number(file.size_hires || 0) > 0,
        flac: Number(file.size_flac || 0) > 0,
        k320: Number(file.size_320mp3 || 0) > 0,
        k128: Number(file.size_128mp3 || 0) > 0,
      }),
    };
  });
}

// kgImageUrl replaces the {size} placeholder in Kugou's image template
// URLs with a concrete pixel size. KG uses different field names per
// endpoint for the same template:
//   - songsearch.kugou.com/song_search_v2   -> "Image"
//   - mobilecdn.kugou.com/api/v3/search/song -> "union_cover"
//   - mobiles.kugou.com/api/v3/search/album  -> "imgurl"
// All of them carry the literal "{size}" token, which is a placeholder
// rather than a usable URL. We default to 240px (matches lxmusic web's
// chosen size — clear thumbnails without being wasteful).
function kgImageUrl(item) {
  if (!item) return '';
  var raw = item.Image || item.union_cover || item.imgurl || item.img || item.pic || '';
  if (!raw) return '';
  if (raw.indexOf('{size}') === -1) return raw;
  return String(raw).replace('{size}', '240');
}

function mapKGSong(list) {
  return (list || []).map(function(item) {
    return {
      id: String(item.hash || item.FileHash || item.songid || item.audio_id || ''),
      name: item.songname || item.SongName || item.filename || item.FileName || '',
      singer: item.singername || item.SingerName || '',
      albumName: item.album_name || item.AlbumName || '',
      duration: toDurationMs(item.duration || item.Duration),
      img: kgImageUrl(item),
      source: 'kg',
      meta: item,
      qualitys: qualityFromFlags({
        flac24bit: Number(item.ResFileSize || 0) > 0,
        ape: Number(item.filesize_ape || item.APEFileSize || 0) > 0,
        flac: Number(item.sqfilesize || item.SQFileSize || 0) > 0,
        k320: Number(item['320filesize'] || item.HQFileSize || 0) > 0,
        k128: Number(item.FileSize || item.filesize || 0) > 0,
      }),
    };
  });
}

function mapKWSong(list) {
  return (list || []).map(function(item) {
    var minfo = String(item.MINFO || item.minfo || '');
    var hasMaster = /master/i.test(minfo);
    var hasHiRes = /24bit|flac24/i.test(minfo);
    var hasApe = /ape/i.test(minfo);
    var hasFlac = /flac/i.test(minfo);
    var has320 = /320/i.test(minfo);
    var has128 = /128/i.test(minfo);
    return {
      id: String(item.MUSICRID || item.rid || item.id || ''),
      name: item.SONGNAME || item.name || '',
      singer: item.ARTIST || item.artist || '',
      albumName: item.ALBUM || item.album || '',
      duration: toDurationMs(item.DURATION || item.duration),
      img: kwPicUrl(item.prob_albumpic, item.web_albumpic_short, item.pic || item.albumpic || item.img),
      source: 'kw',
      meta: item,
      qualitys: qualityFromFlags({
        master: hasMaster,
        flac24bit: hasHiRes,
        ape: hasApe,
        flac: hasFlac,
        k320: has320,
        k128: has128,
      }),
    };
  });
}

function mapMGSong(list) {
  return (list || []).map(function(item) {
    var qualitys = qualityFromFlags({});
    var rates = item.audioFormats || item.newRateFormats || item.rateFormats || [];
    rates.forEach(function(r) {
      var t = String((r && (r.formatType || r.qualityType || r.type)) || '').toUpperCase();
      if (t.indexOf('MASTER') >= 0) qualitys.master = true;
      if (t.indexOf('ZQ24') >= 0 || t.indexOf('HIRES') >= 0) qualitys.flac24bit = true;
      if (t.indexOf('SQ') >= 0 || t.indexOf('FLAC') >= 0) qualitys.flac = true;
      if (t.indexOf('HQ') >= 0 || t.indexOf('320') >= 0) qualitys['320k'] = true;
      if (t.indexOf('PQ') >= 0 || t.indexOf('128') >= 0) qualitys['128k'] = true;
    });
    var singers = item.singer || item.singerName || ((item.singerList || []).map(function(s) { return s && s.name; }).filter(Boolean).join('/'));
    var img = item.pic || item.cover || item.img || item.img3 || item.img2 || item.img1 || '';
    if (img && img.indexOf('http') !== 0) img = 'http://d.musicapp.migu.cn' + img;
    return {
      id: String(item.copyrightId || item.songId || item.id || ''),
      name: item.name || item.songName || '',
      singer: singers || '',
      albumName: item.album || item.albumName || '',
      duration: toDurationMs(item.duration || item.length),
      img: img,
      source: 'mg',
      meta: item,
      qualitys: qualitys,
    };
  });
}

function mapMGImg(raw) {
  var img = raw || '';
  if (img && img.indexOf('http') !== 0) img = 'http://d.musicapp.migu.cn' + img;
  return img;
}

function mapSinger(list, src) {
  return (list || []).map(function(item) {
    var id, name, img;
    if (src === 'wy') {
      id = String(item.id || '');
      name = item.name || '';
      img = item.picUrl || item.img || '';
    } else if (src === 'tx') {
      var mid = item.singerMID || item.mid || '';
      id = mid;
      name = item.singerName || item.name || '';
      img = mid
        ? ('https://y.gtimg.cn/music/photo_new/T001R500x500M000' + mid + '.jpg')
        : (item.singerPic || item.picUrl || '');
    } else if (src === 'kg') {
      id = String(item.id || item.singerid || item.singerID || '');
      name = item.singername || item.singerName || item.name || '';
      img = kgImageUrl(item);
    } else if (src === 'kw') {
      id = String(item.ARTISTID || item.artistId || item.id || '');
      name = item.ARTIST || item.name || '';
      img = kwPicUrl(item.ARTISTPIC || item.prob_albumpic, item.web_artistpic_short, item.pic || item.img || '');
    } else if (src === 'mg') {
      id = String(item.id || item.singerId || '');
      name = item.singerName || item.name || '';
      img = mapMGImg(item.img || item.singerPic || item.pic || '');
    } else {
      id = String(item.id || '');
      name = item.name || '';
      img = item.picUrl || item.img || '';
    }
    return {
      id: id,
      name: name,
      singer: name,
      albumName: '',
      duration: 0,
      img: img,
      source: src,
      qualitys: {},
    };
  });
}

function mapAlbum(list, src) {
  return (list || []).map(function(item) {
    var id, name, singer, img;
    if (src === 'wy') {
      id = String(item.id || '');
      name = item.name || '';
      var ar = item.artists || [];
      singer = ar.map(function(a) { return a && a.name; }).filter(Boolean).join('/');
      img = item.picUrl || item.blurPicUrl || '';
    } else if (src === 'tx') {
      var albumMid = item.albumMID || item.mid || '';
      var sl = item.singer_list || [];
      id = albumMid;
      name = item.albumName || item.name || '';
      singer = item.singerName || (sl.length ? sl.map(function(s) { return s.name; }).join('/') : '');
      img = albumMid
        ? ('https://y.gtimg.cn/music/photo_new/T002R300x300M000' + albumMid + '.jpg')
        : (item.albumPic || item.picUrl || '');
    } else if (src === 'kg') {
      id = String(item.albumid || item.id || '');
      name = item.albumname || item.albumName || item.name || '';
      singer = item.singername || item.singerName || '';
      img = kgImageUrl(item);
    } else if (src === 'kw') {
      id = String(item.ALBUMID || item.albumId || item.id || '');
      name = item.ALBUM || item.albumName || item.name || '';
      singer = item.ARTIST || item.artist || '';
      img = kwPicUrl(item.prob_albumpic, item.web_albumpic_short, item.albumPic || item.pic || item.img || '');
    } else if (src === 'mg') {
      id = String(item.albumId || item.id || '');
      name = item.album || item.albumName || item.name || '';
      singer = (item.singerList || []).map(function(s) { return s && s.name; }).filter(Boolean).join('/') || item.singer || '';
      img = mapMGImg(item.img3 || item.img2 || item.img1 || item.pic || item.cover || '');
    } else {
      id = String(item.id || '');
      name = item.name || '';
      singer = '';
      img = item.picUrl || item.albumPic || item.img || '';
    }
    return {
      id: id,
      name: name,
      singer: singer,
      albumName: name,
      duration: 0,
      img: img,
      source: src,
      qualitys: {},
    };
  });
}

function mapPlaylist(list, src) {
  return (list || []).map(function(item) {
    var id = '';
    var name = '';
    var author = '';
    var img = '';
    var time = '';
    var total = 0;
    var playCount = 0;

    if (src === 'wy') {
      id = String(item.id || '');
      name = item.name || '';
      author = (item.creator && item.creator.nickname) || '';
      img = item.coverImgUrl || '';
      time = item.createTime ? new Date(item.createTime).toISOString().slice(0, 10) : '';
      total = Number(item.trackCount || item.songCount || 0) || 0;
      playCount = Number(item.playCount || 0) || 0;
    } else if (src === 'tx') {
      id = String(item.dissid || item.id || '');
      var numericId = Number(id || 0);
      if (item.docid && isFinite(numericId) && numericId > 0 && numericId < 1000000) return null;
      name = item.dissname || item.name || '';
      author = item.creator && item.creator.name ? item.creator.name : (item.nickname || '');
      img = item.imgurl || item.cover || '';
      time = item.createtime || item.create_time || '';
      total = Number(item.song_count || item.songnum || item.totalSongNum || 0) || 0;
      playCount = Number(item.listennum || item.play_num || 0) || 0;
    } else if (src === 'kg') {
      id = String(item.specialid || item.id || '');
      name = item.specialname || item.name || '';
      author = item.nickname || item.author_name || '';
      img = kgImageUrl(item);
      time = item.publish_time || item.pub_time || '';
      total = Number(item.songcount || item.song_count || 0) || 0;
      playCount = Number(item.play_count || item.playcount || 0) || 0;
    } else if (src === 'kw') {
      id = String(item.playlistid || item.id || '');
      name = item.name || item.title || '';
      author = item.uname || item.nickname || item.username || '';
      img = kwPicUrl(item.img, item.web_albumpic_short, item.pic || item.albumpic || '');
      time = item.pub || item.pubtime || item.createTime || '';
      total = Number(item.total || item.musicNum || item.songnum || 0) || 0;
      playCount = Number(item.listencnt || item.playcnt || item.play_count || 0) || 0;
    } else if (src === 'mg') {
      id = String(item.id || item.playListId || item.playlistId || '');
      name = item.name || item.playListName || '';
      author = item.userName || item.nickName || item.author || '';
      img = mapMGImg(item.img || item.cover || item.image || '');
      time = item.createTime || item.createDate || '';
      total = Number(item.musicNum || item.songNum || item.songCount || 0) || 0;
      playCount = Number(item.playNum || item.playCount || 0) || 0;
    }

    return {
      id: id,
      name: name,
      author: author,
      img: img,
      time: time,
      total: total,
      play_count: playCount,
      source: src,
    };
  }).filter(function(item) { return item && (item.id || item.name); });
}

function mapCommonList(list, src, searchType) {
  if (searchType === 'song') {
    if (src === 'wy') return mapWYSong(list);
    if (src === 'tx') return mapTXSong(list);
    if (src === 'kg') return mapKGSong(list);
    if (src === 'kw') return mapKWSong(list);
    if (src === 'mg') return mapMGSong(list);
    return [];
  }
  if (searchType === 'singer') return mapSinger(list, src);
  if (searchType === 'playlist') return mapPlaylist(list, src);
  return mapAlbum(list, src);
}

function parseDateScore(value) {
  if (!value) return 0;
  var text = String(value).trim();
  if (!text) return 0;
  var normalized = text.replace(/\./g, '-').replace(/\//g, '-');
  var ts = Date.parse(normalized);
  if (isFinite(ts)) return ts;
  var m = text.match(/^(\d{4})(\d{2})(\d{2})$/);
  if (m) {
    var ts2 = Date.parse(m[1] + '-' + m[2] + '-' + m[3]);
    if (isFinite(ts2)) return ts2;
  }
  return 0;
}

function isPlaylistNewSort(id, src) {
  var val = String(id || '').toLowerCase();
  if (!val) return false;
  if (val === 'new') return true;
  // TX: 2=最新, KG: 7=最新
  if (src === 'tx' && val === '2') return true;
  if (src === 'kg' && val === '7') return true;
  return false;
}

function applyPlaylistSort(list, src, id) {
  if (!Array.isArray(list) || list.length < 2) return list;
  var next = list.slice();
  if (isPlaylistNewSort(id, src)) {
    next.sort(function(a, b) {
      var d = parseDateScore(b && b.time) - parseDateScore(a && a.time);
      if (d !== 0) return d;
      var ai = Number(a && a.id);
      var bi = Number(b && b.id);
      if (isFinite(ai) && isFinite(bi) && bi !== ai) return bi - ai;
      var as = String((a && a.id) || '');
      var bs = String((b && b.id) || '');
      if (bs !== as) return bs > as ? 1 : -1;
      var pa = Number(a && a.play_count) || 0;
      var pb = Number(b && b.play_count) || 0;
      return pb - pa;
    });
    return next;
  }
  next.sort(function(a, b) {
    var pa = Number(a && a.play_count) || 0;
    var pb = Number(b && b.play_count) || 0;
    if (pb !== pa) return pb - pa;
    return parseDateScore(b && b.time) - parseDateScore(a && a.time);
  });
  return next;
}

function fetchWY() {
  var typeMap = { song: 1, singer: 100, album: 10, playlist: 1000 };
  var t = typeMap[searchType] || 1;
  var body = 's=' + encodeURIComponent(keyword) + '&type=' + t + '&offset=' + ((page - 1) * limit) + '&limit=' + limit;
  return makeRequest('https://music.163.com/api/cloudsearch/pc', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      'Referer': 'https://music.163.com/',
      'User-Agent': 'Mozilla/5.0',
    },
    body: body,
  }).then(function(r) {
    if (r.statusCode !== 200) throw new Error('WY returned ' + r.statusCode);
    var result = (r.body && r.body.result) || {};
    var list = [];
    var total = 0;
    if (searchType === 'song') list = result.songs || [];
    else if (searchType === 'singer') list = result.artists || [];
    else if (searchType === 'album') list = result.albums || [];
    else list = result.playlists || [];
    if (searchType === 'song') total = Number(result.songCount || result.songcount || 0);
    if (searchType === 'singer') total = Number(result.artistCount || result.artistcount || 0);
    if (searchType === 'album') total = Number(result.albumCount || result.albumcount || 0);
    if (searchType === 'playlist') total = Number(result.playlistCount || result.playlistcount || 0);
    if (!total || !isFinite(total)) total = list.length;
    return { list: mapCommonList(list, 'wy', searchType), total: total };
  });
}

function fetchTX() {
  if (searchType === 'song') {
    var mobilePayload = {
      comm: {
        ct: '11', cv: '14090508', v: '14090508', tmeAppID: 'qqmusic', phonetype: 'EBG-AN10',
        deviceScore: '553.47', devicelevel: '50', newdevicelevel: '20', rom: 'HuaWei/EMOTION/EmotionUI_14.2.0',
        os_ver: '12', OpenUDID: '0', OpenUDID2: '0', QIMEI36: '0', udid: '0', chid: '0', aid: '0',
        oaid: '0', taid: '0', tid: '0', wid: '0', uid: '0', sid: '0', modeSwitch: '6', teenMode: '0',
        ui_mode: '2', nettype: '1020', v4ip: ''
      },
      req: {
        module: 'music.search.SearchCgiService',
        method: 'DoSearchForQQMusicMobile',
        param: {
          search_type: 0,
          query: keyword,
          page_num: page,
          num_per_page: limit,
          highlight: 0,
          nqc_flag: 0,
          multi_zhida: 0,
          cat: 2,
          grp: 1,
          sin: 0,
          sem: 0,
        }
      }
    };
    return makeRequest('https://u.y.qq.com/cgi-bin/musicu.fcg', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'User-Agent': 'QQMusic 14090508(android 12)' },
      body: JSON.stringify(mobilePayload),
    }).then(function(r) {
      if (r.statusCode !== 200 || !r.body || r.body.code !== 0 || !r.body.req || r.body.req.code !== 0) {
        throw new Error('TX returned ' + r.statusCode);
      }
      var data = (r.body.req && r.body.req.data) || {};
      var body = data.body || {};
      var meta = data.meta || {};
      var list = body.item_song || [];
      var total = Number(meta.estimate_sum || meta.sum || meta.total || 0);
      if (!total || !isFinite(total)) total = list.length;
      return { list: mapCommonList(list, 'tx', searchType), total: total };
    });
  }

  var typeMap = { singer: 1, album: 2, playlist: 3 };
  var t = typeMap[searchType] || 1;
  var desktopPayload = {
    comm: { ct: '19', cv: '1859', uin: '0' },
    req: {
      method: 'DoSearchForQQMusicDesktop',
      module: 'music.search.SearchCgiService',
      param: { grp: 1, num_per_page: limit, page_num: page, query: keyword, search_type: t },
    },
  };
  return makeRequest('https://u.y.qq.com/cgi-bin/musicu.fcg', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json;charset=utf-8', 'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:109.0) Gecko/20100101 Firefox/115.0' },
    body: JSON.stringify(desktopPayload),
  }).then(function(r) {
    if (r.statusCode !== 200 || !r.body || r.body.code !== 0) throw new Error('TX returned ' + r.statusCode);
    var data = (r.body.req && r.body.req.data) || {};
    var body = data.body || {};
    var bucket = searchType === 'singer'
      ? body.singer
      : (searchType === 'album' ? body.album : (body.songlist || body.song_list || body.songList));
    var list = (bucket && bucket.list) || [];
    var total = Number((bucket && bucket.total) || (data.meta && (data.meta.sum || data.meta.total)) || 0);
    if (!total || !isFinite(total)) total = list.length;
    return { list: mapCommonList(list, 'tx', searchType), total: total };
  });
}

function fetchKG() {
  if (searchType === 'playlist') {
    var purl = 'http://mobilecdn.kugou.com/api/v3/search/special?format=json&keyword=' + encodeURIComponent(keyword)
      + '&page=' + page + '&pagesize=' + limit;
    return makeRequest(purl, {
      headers: { 'User-Agent': 'Mozilla/5.0' },
    }).then(function(r) {
      if (r.statusCode !== 200) throw new Error('KG returned ' + r.statusCode);
      var data = r.body && r.body.data;
      var list = [];
      var total = 0;
      if (data) {
        list = data.info || data.lists || data.list || [];
        total = Number(data.total || data.total_count || 0);
      }
      if (!total || !isFinite(total)) total = list.length;
      return { list: mapCommonList(list, 'kg', searchType), total: total };
    });
  }

  if (searchType === 'song') {
    var primary = 'https://songsearch.kugou.com/song_search_v2?keyword=' + encodeURIComponent(keyword)
      + '&page=' + page
      + '&pagesize=' + limit
      + '&userid=0&clientver=&platform=WebFilter&filter=2&iscorrection=1&privilege_filter=0&area_code=1';
    var fallback = 'http://mobilecdn.kugou.com/api/v3/search/song?format=json&keyword=' + encodeURIComponent(keyword)
      + '&page=' + page
      + '&pagesize=' + limit
      + '&showtype=1';

    return makeRequest(primary, {
      headers: {
        'User-Agent': 'Mozilla/5.0',
        'Referer': 'https://www.kugou.com/',
      },
      retries: 2,
      maxRedirects: 1,
    }).catch(function() {
      return makeRequest(fallback, {
        headers: { 'User-Agent': 'Mozilla/5.0' },
        retries: 2,
        maxRedirects: 1,
      });
    }).then(function(r) {
      if (r.statusCode !== 200) throw new Error('KG returned ' + r.statusCode);
      var list = [];
      var total = 0;
      if (r.body && r.body.error_code === 0 && r.body.data && r.body.data.lists) {
        list = r.body.data.lists;
        total = Number(r.body.data.total || r.body.data.total_count || 0);
      } else {
        var data = r.body && r.body.data;
        if (data) {
          list = data.info || data.lists || data.list || [];
          total = Number(data.total || data.total_count || 0);
        }
      }
      if (!total || !isFinite(total)) total = list.length;
      return { list: mapCommonList(list, 'kg', searchType), total: total };
    });
  }

  var ft = searchType === 'singer' ? 'singer' : 'album';
  var url = 'https://mobiles.kugou.com/api/v3/search/' + ft + '?format=json&keyword=' + encodeURIComponent(keyword) + '&page=' + page + '&pagesize=' + limit;
  return makeRequest(url, {
    headers: { 'User-Agent': 'Mozilla/5.0' },
  }).then(function(r) {
    if (r.statusCode !== 200) throw new Error('KG returned ' + r.statusCode);
    var data = r.body && r.body.data;
    var list = [];
    var total = 0;
    if (data) {
      list = data.info || data.lists || data.list || [];
      total = Number(data.total || data.total_count || 0);
    }
    if (!total || !isFinite(total)) total = list.length;
    return { list: mapCommonList(list, 'kg', searchType), total: total };
  });
}

function fetchKW() {
  var ft = searchType === 'song'
    ? 'music'
    : (searchType === 'singer' ? 'artist' : (searchType === 'playlist' ? 'playlist' : 'album'));
  var url = 'http://search.kuwo.cn/r.s?client=kt&all=' + encodeURIComponent(keyword) + '&pn=' + (page - 1) + '&rn=' + limit + '&uid=794762570&ver=kwplayer_ar_9.2.2.1&vipver=1&show_copyright_off=1&newver=1&ft=' + ft + '&cluster=0&strategy=2012&encoding=utf8&rformat=json&vermerge=1&mobi=1&issubtitle=1';
  return makeRequest(url, {
    headers: { 'User-Agent': 'Dalvik/2.1.0 (Linux; U; Android 9;)' },
  }).then(function(r) {
    if (r.statusCode !== 200) throw new Error('KW returned ' + r.statusCode);
    var body = r.body || {};
    var list;
    var total = Number(body.TOTAL || body.total || 0);
    if (searchType === 'song') list = body.abslist || body.ABSLIST || body.list || [];
    else if (searchType === 'singer') list = body.artistlist || body.ARTISTLIST || body.list || [];
    else if (searchType === 'playlist') list = body.abslist || body.ABSLIST || body.list || [];
    else list = body.albumlist || body.ALBUMLIST || body.list || [];
    if (!total || !isFinite(total)) total = list.length;
    return { list: mapCommonList(list, 'kw', searchType), total: total };
  });
}

function fetchMG() {
  var signData = mgSignature(keyword);
  var url = 'https://jadeite.migu.cn/music_search/v3/search/searchAll?isCorrect=0&isCopyright=1&searchSwitch=' + mgSearchSwitch(searchType)
    + '&pageSize=' + limit
    + '&text=' + encodeURIComponent(keyword)
    + '&pageNo=' + page
    + '&sort=0&sid=USS';
  return makeRequest(url, {
    headers: {
      uiVersion: 'A_music_3.6.1',
      deviceId: signData.deviceId,
      timestamp: signData.timestamp,
      sign: signData.sign,
      channel: '0146921',
      'User-Agent': 'Mozilla/5.0 (Linux; U; Android 11.0.0; zh-cn; MI 11 Build/OPR1.170623.032) AppleWebKit/534.30 (KHTML, like Gecko) Version/4.0 Mobile Safari/534.30',
    },
  }).then(function(r) {
    if (r.statusCode !== 200 || !r.body || r.body.code !== '000000') throw new Error('MG returned ' + r.statusCode);
    var body = r.body || {};
    var section = searchType === 'singer'
      ? (body.singerResultData || {})
      : (searchType === 'album'
          ? (body.albumResultData || {})
          : (searchType === 'playlist' ? (body.songListResultData || body.songlistResultData || {}) : (body.songResultData || {})));
    var list = flattenMGResultList(section.resultList || section.result || section.list || []);
    var total = Number(section.totalCount || section.total || section.count || 0);
    if (!total || !isFinite(total)) total = list.length;
    return { list: mapCommonList(list, 'mg', searchType), total: total };
  });
}

if (!keyword.trim()) {
  process.stdout.write(JSON.stringify({ success: true, list: [], total: 0 }));
} else {
  var fetchers = { wy: fetchWY, tx: fetchTX, kg: fetchKG, kw: fetchKW, mg: fetchMG };
  var fn = fetchers[source];
  if (!fn) {
    process.stdout.write(JSON.stringify({ success: false, error: 'Unsupported source: ' + source, list: [], total: 0 }));
  } else {
    fn()
      .then(function(list) {
        var total = Array.isArray(list) ? list.length : Number((list && list.total) || 0);
        var payloadList = Array.isArray(list) ? list : (list && list.list) || [];
        if (searchType === 'playlist') {
          payloadList = applyPlaylistSort(payloadList, source, sortId);
        }
        process.stdout.write(JSON.stringify({ success: true, list: payloadList || [], total: total || 0 }));
      })
      .catch(function(err) {
        process.stdout.write(JSON.stringify({ success: false, error: (err && err.message) ? err.message : 'unknown error', list: [], total: 0 }));
      });
  }
}
`

const nodePlaylistDetailScript = `
var https = require('https');
var http = require('http');

var source = process.env.ND_SOURCE || 'wy';
var playlistId = String(process.env.ND_PLAYLIST_ID || '').trim();
var page = Number(process.env.ND_PAGE || '1');
var limit = Number(process.env.ND_LIMIT || '30');
var timeoutMs = Number(process.env.ND_TIMEOUT_MS || '8000');

function makeRequest(targetUrl, opts) {
  opts = opts || {};
  return new Promise(function(resolve, reject) {
    var parsed;
    try { parsed = new URL(targetUrl); } catch (e) { return reject(e); }
    var lib = parsed.protocol === 'https:' ? https : http;
    var body = opts.body || null;
    var retries = Number(opts.retries == null ? 1 : opts.retries);
    var maxRedirects = Number(opts.maxRedirects == null ? 2 : opts.maxRedirects);
    if (body && typeof body !== 'string') body = JSON.stringify(body);
    var headers = Object.assign({}, opts.headers || {});
    if (body) headers['Content-Length'] = Buffer.byteLength(body);

    var req = lib.request({
      hostname: parsed.hostname,
      port: parsed.port ? Number(parsed.port) : (parsed.protocol === 'https:' ? 443 : 80),
      path: parsed.pathname + parsed.search,
      method: opts.method || 'GET',
      headers: headers,
      rejectUnauthorized: false,
    }, function(res) {
      var data = '';
      res.on('data', function(c) { data += c; });
      res.on('end', function() {
        if ((res.statusCode === 301 || res.statusCode === 302 || res.statusCode === 303 || res.statusCode === 307 || res.statusCode === 308)
          && res.headers && res.headers.location && maxRedirects > 0) {
          var redirectUrl = '';
          try {
            redirectUrl = new URL(res.headers.location, targetUrl).toString();
          } catch (_) {
            redirectUrl = res.headers.location;
          }
          resolve(makeRequest(redirectUrl, Object.assign({}, opts, { maxRedirects: maxRedirects - 1 })));
          return;
        }
        var parsedBody;
        try { parsedBody = JSON.parse(data); } catch (e) { parsedBody = data; }
        resolve({ statusCode: res.statusCode, body: parsedBody, headers: res.headers || {} });
      });
    });
    req.setTimeout(timeoutMs, function() { req.destroy(new Error('request timeout')); });
    req.on('error', function(err) {
      if (retries > 0) {
        resolve(makeRequest(targetUrl, Object.assign({}, opts, { retries: retries - 1 })));
        return;
      }
      reject(err);
    });
    if (body) req.write(body);
    req.end();
  });
}

function toDurationMs(input) {
  if (typeof input === 'number' && isFinite(input)) {
    if (input > 100000) return Math.floor(input);
    return Math.floor(input * 1000);
  }
  if (typeof input === 'string') {
    if (/^\d+:\d+(?::\d+)?$/.test(input)) {
      var parts = input.split(':').map(function(v) { return Number(v) || 0; });
      var sec = 0;
      while (parts.length) sec = sec * 60 + parts.shift();
      return sec * 1000;
    }
    var n = Number(input);
    if (isFinite(n) && n > 0) return n > 100000 ? Math.floor(n) : Math.floor(n * 1000);
  }
  return 0;
}

function qualityFromFlags(flags) {
  var q = {};
  if (flags.master) q.master = true;
  if (flags.flac24bit) q.flac24bit = true;
  if (flags.ape) q.ape = true;
  if (flags.flac) q.flac = true;
  if (flags.k128) q['128k'] = true;
  if (flags.k320) q['320k'] = true;
  return q;
}

function formatKWPic(url, size) {
  if (!url) return '';
  var t = String(size || 1000);
  return url
    .replace(/(\/star\/albumcover\/)\d+/, '$1' + t)
    .replace(/(pictype=)\d+/, '$1' + t)
    .replace(/(size=)\d+/, '$1' + t);
}

function kwPicUrl(probAlbumpic, webShortPath, fallback) {
  if (probAlbumpic && /^https?:\/\//.test(probAlbumpic)) return formatKWPic(probAlbumpic);
  if (webShortPath) return formatKWPic('https://img4.kuwo.cn/star/albumcover/1000' + webShortPath);
  if (fallback && /^https?:\/\//.test(fallback)) return formatKWPic(fallback);
  return '';
}

function kgImageUrl(item) {
  if (!item) return '';
  var raw = item.Image || item.union_cover || item.imgurl || item.img || item.pic || item.album_img || '';
  if (!raw) return '';
  if (raw.indexOf('{size}') === -1) return raw;
  return String(raw).replace('{size}', '240');
}

function mapMGImg(raw) {
  var img = raw || '';
  if (img && img.indexOf('http') !== 0) img = 'http://d.musicapp.migu.cn' + img;
  return img;
}

function slicePage(list, pageNum, pageSize) {
  var start = Math.max(0, (pageNum - 1) * pageSize);
  return (list || []).slice(start, start + pageSize);
}

function mapWYSong(list) {
  return (list || []).map(function(item) {
    var ar = item.ar || item.artists || [];
    var singer = ar.map(function(a) { return a && a.name; }).filter(Boolean).join('/');
    var durationInput = item.dt;
    if (!(typeof durationInput === 'number' && isFinite(durationInput) && durationInput > 0)) {
      durationInput = item.duration;
      // Some WY search payloads expose duration in centiseconds.
      // Example: 24415 => 244.15s (about 04:04), not 24415s.
      if (typeof durationInput === 'number' && isFinite(durationInput) && durationInput >= 10000 && durationInput < 100000) {
        durationInput = durationInput * 10;
      }
    }
    return {
      id: String(item.id || ''),
      name: item.name || '',
      singer: singer,
      albumName: (item.al && item.al.name) || (item.album && item.album.name) || '',
      duration: toDurationMs(durationInput),
      img: (item.al && item.al.picUrl) || (item.album && item.album.picUrl) || '',
      source: 'wy',
      meta: item,
      qualitys: qualityFromFlags({
        flac24bit: !!item.hr,
        flac: !!item.sq,
        k320: !!item.h,
        k128: !!item.l || !!item.m || !!item.h || !!item.sq || !!item.hr,
      }),
    };
  });
}

function mapTXSong(list) {
  return (list || []).map(function(item) {
    var singers = (item.singer || []).map(function(s) { return s && s.name; }).filter(Boolean).join('/');
    var albumMid = item.album && item.album.mid;
    var img = albumMid ? ('https://y.gtimg.cn/music/photo_new/T002R300x300M000' + albumMid + '.jpg') : '';
    var file = item.file || {};
    return {
      id: String(item.mid || item.songmid || ''),
      name: (item.name || item.title || '') + (item.title_extra || ''),
      singer: singers,
      albumName: (item.album && item.album.name) || '',
      duration: toDurationMs(item.interval),
      img: img,
      source: 'tx',
      meta: item,
      qualitys: qualityFromFlags({
        flac24bit: Number(file.size_hires || 0) > 0,
        flac: Number(file.size_flac || 0) > 0,
        k320: Number(file.size_320mp3 || 0) > 0,
        k128: Number(file.size_128mp3 || 0) > 0,
      }),
    };
  });
}

function mapKGSong(list) {
  return (list || []).map(function(item) {
    return {
      id: String(item.hash || item.FileHash || item.songid || item.audio_id || ''),
      name: item.songname || item.SongName || item.filename || item.FileName || '',
      singer: item.singername || item.SingerName || '',
      albumName: item.album_name || item.AlbumName || '',
      duration: toDurationMs(item.duration || item.Duration),
      img: kgImageUrl(item),
      source: 'kg',
      meta: item,
      qualitys: qualityFromFlags({
        flac24bit: Number(item.ResFileSize || 0) > 0,
        ape: Number(item.filesize_ape || item.APEFileSize || 0) > 0,
        flac: Number(item.sqfilesize || item.SQFileSize || 0) > 0,
        k320: Number(item['320filesize'] || item.HQFileSize || 0) > 0,
        k128: Number(item.FileSize || item.filesize || 0) > 0,
      }),
    };
  });
}

function mapKWSong(list) {
  return (list || []).map(function(item) {
    var minfo = String(item.MINFO || item.minfo || item.N_MINFO || '');
    var hasMaster = /master/i.test(minfo);
    var hasHiRes = /24bit|flac24/i.test(minfo);
    var hasApe = /ape/i.test(minfo);
    var hasFlac = /flac/i.test(minfo);
    var has320 = /320/i.test(minfo);
    var has128 = /128/i.test(minfo);
    return {
      id: String(item.MUSICRID || item.rid || item.id || ''),
      name: item.SONGNAME || item.name || '',
      singer: item.ARTIST || item.artist || '',
      albumName: item.ALBUM || item.album || '',
      duration: toDurationMs(item.DURATION || item.duration),
      img: kwPicUrl(item.prob_albumpic, item.web_albumpic_short, item.pic || item.albumpic || item.img),
      source: 'kw',
      meta: item,
      qualitys: qualityFromFlags({
        master: hasMaster,
        flac24bit: hasHiRes,
        ape: hasApe,
        flac: hasFlac,
        k320: has320,
        k128: has128,
      }),
    };
  });
}

function mapMGSong(list) {
  return (list || []).map(function(item) {
    var qualitys = qualityFromFlags({});
    var rates = item.audioFormats || item.newRateFormats || item.rateFormats || [];
    rates.forEach(function(r) {
      var t = String((r && (r.formatType || r.qualityType || r.type)) || '').toUpperCase();
      if (t.indexOf('MASTER') >= 0) qualitys.master = true;
      if (t.indexOf('ZQ24') >= 0 || t.indexOf('HIRES') >= 0) qualitys.flac24bit = true;
      if (t.indexOf('SQ') >= 0 || t.indexOf('FLAC') >= 0) qualitys.flac = true;
      if (t.indexOf('HQ') >= 0 || t.indexOf('320') >= 0) qualitys['320k'] = true;
      if (t.indexOf('PQ') >= 0 || t.indexOf('128') >= 0) qualitys['128k'] = true;
    });
    var singers = item.singer || item.singerName || ((item.singerList || []).map(function(s) { return s && s.name; }).filter(Boolean).join('/'));
    var img = mapMGImg(item.pic || item.cover || item.img || item.img3 || item.img2 || item.img1 || '');
    return {
      id: String(item.copyrightId || item.songId || item.id || ''),
      name: item.name || item.songName || '',
      singer: singers || '',
      albumName: item.album || item.albumName || '',
      duration: toDurationMs(item.duration || item.length),
      img: img,
      source: 'mg',
      meta: item,
      qualitys: qualitys,
    };
  });
}

function fetchWYSongDetails(ids) {
  if (!ids.length) return Promise.resolve([]);
  var idsJson = '[' + ids.map(function(id) { return Number(id); }).join(',') + ']';
  var cJson = '[' + ids.map(function(id) { return '{"id":' + Number(id) + '}'; }).join(',') + ']';
  var url = 'https://music.163.com/api/v3/song/detail?c=' + encodeURIComponent(cJson) + '&ids=' + encodeURIComponent(idsJson);
  return makeRequest(url, {
    headers: {
      'User-Agent': 'Mozilla/5.0',
      'Referer': 'https://music.163.com/',
    },
  }).then(function(r) {
    if (r.statusCode !== 200 || !r.body || Number(r.body.code) !== 200) throw new Error('WY song detail failed');
    return mapWYSong(r.body.songs || []);
  });
}

function fetchWYDetail() {
  var url = 'https://music.163.com/api/v6/playlist/detail?id=' + encodeURIComponent(playlistId);
  return makeRequest(url, {
    headers: {
      'User-Agent': 'Mozilla/5.0',
      'Referer': 'https://music.163.com/',
    },
  }).then(function(r) {
    if (r.statusCode !== 200 || !r.body || Number(r.body.code) !== 200 || !r.body.playlist) throw new Error('WY detail failed');
    var playlist = r.body.playlist || {};
    var trackIds = (playlist.trackIds || []).map(function(item) {
      return item && item.id ? item.id : item;
    }).filter(Boolean);
    var total = Number(playlist.trackCount || trackIds.length || 0);
    var pageIds = slicePage(trackIds, page, limit);
    return fetchWYSongDetails(pageIds).then(function(list) {
      return {
        list: list,
        total: total,
        info: {
          name: playlist.name || '',
          img: playlist.coverImgUrl || '',
          desc: playlist.description || '',
          author: playlist.creator && playlist.creator.nickname ? playlist.creator.nickname : '',
          play_count: Number(playlist.playCount || 0),
        },
      };
    });
  });
}

function fetchTXDetail() {
  var url = 'https://c.y.qq.com/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg?type=1&json=1&utf8=1&onlysong=0&new_format=1&disstid=' + encodeURIComponent(playlistId) + '&loginUin=0&hostUin=0&format=json&inCharset=utf8&outCharset=utf-8&notice=0&platform=yqq.json&needNewCode=0';
  return makeRequest(url, {
    headers: {
      'User-Agent': 'Mozilla/5.0',
      'Origin': 'https://y.qq.com',
      'Referer': 'https://y.qq.com/n/yqq/playlist/' + playlistId + '.html',
    },
  }).then(function(r) {
    if (r.statusCode !== 200) throw new Error('TX detail failed');
    if (!r.body || Number(r.body.code) !== 0) {
      var numericId = Number(playlistId || 0);
      if (Number(r.body && r.body.code) === 10 && isFinite(numericId) && numericId > 0 && numericId < 1000000) {
        throw new Error('TX special playlist detail unsupported');
      }
      throw new Error('TX detail failed');
    }
    var cd = (r.body.cdlist || [])[0] || {};
    var fullList = mapTXSong(cd.songlist || []);
    return {
      list: slicePage(fullList, page, limit),
      total: Number(cd.songnum || fullList.length || 0),
      info: {
        name: cd.dissname || '',
        img: cd.logo || '',
        desc: cd.desc || '',
        author: cd.nickname || '',
        play_count: Number(cd.visitnum || 0),
      },
    };
  });
}

function fetchKGDetail() {
  var normalizedId = playlistId.replace(/^id_/, '');
  var url = 'http://www2.kugou.kugou.com/yueku/v9/special/single/' + encodeURIComponent(normalizedId) + '-5-9999.html';
  return makeRequest(url, {
    headers: { 'User-Agent': 'Mozilla/5.0' },
  }).then(function(r) {
    if (r.statusCode !== 200 || typeof r.body !== 'string') throw new Error('KG detail failed');
    var dataMatch = r.body.match(/global\.data = (\[.+?\]);/);
    if (!dataMatch) throw new Error('KG detail parse failed');
    var infoMatch = r.body.match(/global = \{[\s\S]+?name: "(.+?)"[\s\S]+?pic: "(.+?)"[\s\S]+?\};/);
    var list = [];
    try {
      list = JSON.parse(dataMatch[1]);
    } catch (e) {
      throw new Error('KG detail json failed');
    }
    var mapped = mapKGSong(list);
    return {
      list: slicePage(mapped, page, limit),
      total: mapped.length,
      info: {
        name: infoMatch ? infoMatch[1] : '',
        img: infoMatch ? infoMatch[2] : '',
      },
    };
  });
}

function fetchKWDetail() {
  var rawId = playlistId;
  if (/^digest-/.test(rawId)) {
    var parts = rawId.split('__');
    rawId = parts[1] || rawId;
  }
  rawId = rawId.replace(/^.+\/playlist(?:_detail)?\//, '').replace(/(?:\?.*|#.*)$/, '');
  var url = 'http://nplserver.kuwo.cn/pl.svc?op=getlistinfo&pid=' + encodeURIComponent(rawId) + '&pn=' + (page - 1) + '&rn=' + limit + '&encode=utf8&keyset=pl2012&identity=kuwo&pcmp4=1&vipver=MUSIC_9.0.5.0_W1&newver=1';
  return makeRequest(url, {
    headers: { 'User-Agent': 'Dalvik/2.1.0 (Linux; U; Android 9;)' },
  }).then(function(r) {
    if (r.statusCode !== 200 || !r.body || r.body.result !== 'ok') throw new Error('KW detail failed');
    return {
      list: mapKWSong(r.body.musiclist || []),
      total: Number(r.body.total || 0),
      info: {
        name: r.body.title || '',
        img: kwPicUrl(r.body.pic, '', r.body.pic || ''),
        desc: r.body.info || '',
        author: r.body.uname || '',
        play_count: Number(r.body.playnum || 0),
      },
    };
  });
}

function fetchMGDetail() {
  var rawId = playlistId;
  var m = /(?:playlistId|id)=(\d+)/.exec(rawId);
  if (m) rawId = m[1];
  var listUrl = 'https://app.c.nf.migu.cn/MIGUM3.0/resource/playlist/song/v2.0?pageNo=' + page + '&pageSize=' + limit + '&playlistId=' + encodeURIComponent(rawId);
  var infoUrl = 'https://c.musicapp.migu.cn/MIGUM3.0/resource/playlist/v2.0?playlistId=' + encodeURIComponent(rawId);
  var headers = {
    'User-Agent': 'Mozilla/5.0 (iPhone; CPU iPhone OS 13_2_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0.3 Mobile/15E148 Safari/604.1',
    'Referer': 'https://m.music.migu.cn/',
  };
  return Promise.all([
    makeRequest(listUrl, { headers: headers }),
    makeRequest(infoUrl, { headers: headers }),
  ]).then(function(results) {
    var listRes = results[0];
    var infoRes = results[1];
    if (listRes.statusCode !== 200 || !listRes.body || listRes.body.code !== '000000') throw new Error('MG detail failed');
    if (infoRes.statusCode !== 200 || !infoRes.body || infoRes.body.code !== '000000') throw new Error('MG info failed');
    var infoData = infoRes.body.data || {};
    return {
      list: mapMGSong((listRes.body.data && listRes.body.data.songList) || []),
      total: Number((listRes.body.data && listRes.body.data.totalCount) || 0),
      info: {
        name: infoData.title || '',
        img: infoData.imgItem && infoData.imgItem.img ? infoData.imgItem.img : '',
        desc: infoData.summary || '',
        author: infoData.ownerName || '',
        play_count: Number(infoData.opNumItem && infoData.opNumItem.playNum || 0),
      },
    };
  });
}

if (!playlistId) {
  process.stdout.write(JSON.stringify({ success: false, error: 'playlist id is required', list: [], total: 0 }));
} else {
  var fetchers = { wy: fetchWYDetail, tx: fetchTXDetail, kg: fetchKGDetail, kw: fetchKWDetail, mg: fetchMGDetail };
  var fn = fetchers[source];
  if (!fn) {
    process.stdout.write(JSON.stringify({ success: false, error: 'Unsupported source: ' + source, list: [], total: 0 }));
  } else {
    fn()
      .then(function(result) {
        process.stdout.write(JSON.stringify({
          success: true,
          list: (result && result.list) || [],
          total: Number((result && result.total) || 0),
          info: (result && result.info) || null,
        }));
      })
      .catch(function(err) {
        process.stdout.write(JSON.stringify({ success: false, error: (err && err.message) ? err.message : 'unknown error', list: [], total: 0 }));
      });
  }
}
`

const nodePlaylistBrowseScript = `
var https = require('https');
var http = require('http');

var source = process.env.ND_SOURCE || 'wy';
var mode = process.env.ND_BROWSE_MODE || 'list';
var sortId = String(process.env.ND_SORT_ID || '').trim();
var tagId = String(process.env.ND_TAG_ID || '').trim();
var page = Number(process.env.ND_PAGE || '1');
var limit = Number(process.env.ND_LIMIT || '30');
var timeoutMs = Number(process.env.ND_TIMEOUT_MS || '8000');

function makeRequest(targetUrl, opts) {
  opts = opts || {};
  return new Promise(function(resolve, reject) {
    var parsed;
    try { parsed = new URL(targetUrl); } catch (e) { return reject(e); }
    var lib = parsed.protocol === 'https:' ? https : http;
    var body = opts.body || null;
    var retries = Number(opts.retries == null ? 1 : opts.retries);
    var maxRedirects = Number(opts.maxRedirects == null ? 2 : opts.maxRedirects);
    if (body && typeof body !== 'string') body = JSON.stringify(body);
    var headers = Object.assign({}, opts.headers || {});
    if (body) headers['Content-Length'] = Buffer.byteLength(body);

    var req = lib.request({
      hostname: parsed.hostname,
      port: parsed.port ? Number(parsed.port) : (parsed.protocol === 'https:' ? 443 : 80),
      path: parsed.pathname + parsed.search,
      method: opts.method || 'GET',
      headers: headers,
      rejectUnauthorized: false,
    }, function(res) {
      var data = '';
      res.on('data', function(c) { data += c; });
      res.on('end', function() {
        if ((res.statusCode === 301 || res.statusCode === 302 || res.statusCode === 303 || res.statusCode === 307 || res.statusCode === 308)
          && res.headers && res.headers.location && maxRedirects > 0) {
          var redirectUrl = '';
          try {
            redirectUrl = new URL(res.headers.location, targetUrl).toString();
          } catch (_) {
            redirectUrl = res.headers.location;
          }
          resolve(makeRequest(redirectUrl, Object.assign({}, opts, { maxRedirects: maxRedirects - 1 })));
          return;
        }
        var parsedBody;
        try { parsedBody = JSON.parse(data); } catch (e) { parsedBody = data; }
        resolve({ statusCode: res.statusCode, body: parsedBody, headers: res.headers || {} });
      });
    });
    req.setTimeout(timeoutMs, function() { req.destroy(new Error('request timeout')); });
    req.on('error', function(err) {
      if (retries > 0) {
        resolve(makeRequest(targetUrl, Object.assign({}, opts, { retries: retries - 1 })));
        return;
      }
      reject(err);
    });
    if (body) req.write(body);
    req.end();
  });
}

function formatKWPic(url, size) {
  if (!url) return '';
  var t = String(size || 1000);
  return url
    .replace(/(\/star\/albumcover\/)\d+/, '$1' + t)
    .replace(/(pictype=)\d+/, '$1' + t)
    .replace(/(size=)\d+/, '$1' + t);
}

function kwPicUrl(probAlbumpic, webShortPath, fallback) {
  if (probAlbumpic && /^https?:\/\//.test(probAlbumpic)) return formatKWPic(probAlbumpic);
  if (webShortPath) return formatKWPic('https://img4.kuwo.cn/star/albumcover/1000' + webShortPath);
  if (fallback && /^https?:\/\//.test(fallback)) return formatKWPic(fallback);
  return '';
}

function kgImageUrl(item) {
  if (!item) return '';
  var raw = item.Image || item.union_cover || item.imgurl || item.img || item.pic || item.album_img || '';
  if (!raw) return '';
  if (raw.indexOf('{size}') === -1) return raw;
  return String(raw).replace('{size}', '240');
}

function mapMGImg(raw) {
  var img = raw || '';
  if (img && img.indexOf('http') !== 0) img = 'http://d.musicapp.migu.cn' + img;
  return img;
}

function decodeSimpleHtml(text) {
  return String(text || '')
    .replace(/<br\s*\/?>/gi, '\n')
    .replace(/&amp;/g, '&')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>');
}

function mapPlaylist(list, src) {
  return (list || []).map(function(item) {
    var id = '';
    var name = '';
    var author = '';
    var img = '';
    var time = '';
    var total = 0;
    var playCount = 0;

    if (src === 'wy') {
      id = String(item.id || '');
      name = item.name || '';
      author = (item.creator && item.creator.nickname) || '';
      img = item.coverImgUrl || '';
      time = item.createTime ? new Date(item.createTime).toISOString().slice(0, 10) : '';
      total = Number(item.trackCount || 0) || 0;
      playCount = Number(item.playCount || 0) || 0;
    } else if (src === 'tx') {
      id = String(item.tid || item.dissid || item.id || '');
      name = item.title || item.dissname || item.name || '';
      author = (item.creator_info && item.creator_info.nick) || (item.creator && item.creator.nick) || (item.creator && item.creator.name) || item.nickname || '';
      img = item.cover_url_medium || (item.cover && (item.cover.medium_url || item.cover.default_url)) || item.imgurl || '';
      time = item.modify_time ? new Date(Number(item.modify_time) * 1000).toISOString().slice(0, 10) : (item.createtime || item.create_time || '');
      total = Number(item.song_ids && item.song_ids.length || item.song_count || item.songnum || item.totalSongNum || 0) || 0;
      playCount = Number(item.access_num || item.play_cnt || item.listennum || item.play_num || 0) || 0;
    } else if (src === 'kg') {
      id = String(item.specialid || item.id || '').replace(/^id_/, '');
      if (id && !/^id_/.test(String(item.id || ''))) id = 'id_' + id;
      name = item.specialname || item.name || '';
      author = item.nickname || item.author_name || '';
      img = item.img || item.imgurl || kgImageUrl(item);
      time = item.publish_time || item.publishtime || item.pub_time || '';
      total = Number(item.songcount || item.song_count || 0) || 0;
      playCount = Number(item.total_play_count || item.play_count || item.playcount || 0) || 0;
    } else if (src === 'kw') {
      id = String(item.id || '').indexOf('digest-') === 0 ? String(item.id) : ('digest-' + String(item.digest || '10000') + '__' + String(item.id || item.playlistid || ''));
      name = item.name || item.title || '';
      author = item.uname || item.nickname || item.username || '';
      img = formatKWPic(item.img || item.pic || '');
      total = Number(item.total || item.musicNum || item.songnum || 0) || 0;
      playCount = Number(item.listencnt || item.playcnt || item.play_count || 0) || 0;
    } else if (src === 'mg') {
      id = String(item.logEvent && item.logEvent.contentId || item.resId || item.id || item.contentId || '');
      name = item.title || item.txt || item.name || '';
      author = item.userName || item.ownerName || '';
      img = mapMGImg(item.imageUrl || item.img || '');
      total = Number(item.contentCount || item.musicNum || item.songNum || item.songCount || 0) || 0;
      playCount = Number(item.playNum || item.playCount || 0) || 0;
    }

    return {
      id: id,
      name: name,
      author: author,
      img: img,
      time: time,
      total: total,
      play_count: playCount,
      desc: decodeSimpleHtml(item.desc || item.description || item.intro || item.txt2 || item.introduction || ''),
      source: src,
    };
  }).filter(function(item) { return item && item.id; });
}

function groupTags(items, getGroupName, getTag) {
  var groups = {};
  (items || []).forEach(function(item) {
    var groupName = getGroupName(item);
    if (!groupName) return;
    if (!groups[groupName]) groups[groupName] = [];
    var tag = getTag(item);
    if (tag && tag.id && tag.name) groups[groupName].push(tag);
  });
  return Object.keys(groups).map(function(name) {
    return { name: name, list: groups[name] };
  });
}

function wySortList() {
  return [{ id: 'hot', name: '最热' }];
}
function txSortList() {
  return [{ id: '5', name: '最热' }, { id: '2', name: '最新' }];
}
function kgSortList() {
  return [{ id: '5', name: '推荐' }, { id: '6', name: '最热' }, { id: '7', name: '最新' }, { id: '3', name: '热藏' }, { id: '8', name: '飙升' }];
}
function kwSortList() {
  return [{ id: 'new', name: '最新' }, { id: 'hot', name: '最热' }];
}
function mgSortList() {
  return [{ id: '15127315', name: '推荐' }];
}

function fetchWYTags() {
  return Promise.all([
    makeRequest('https://music.163.com/api/playlist/catalogue', { headers: { 'User-Agent': 'Mozilla/5.0', 'Referer': 'https://music.163.com/' } }),
    makeRequest('https://music.163.com/api/playlist/hottags', { headers: { 'User-Agent': 'Mozilla/5.0', 'Referer': 'https://music.163.com/' } }),
  ]).then(function(results) {
    var catalogue = results[0].body || {};
    var hot = results[1].body || {};
    var categories = catalogue.categories || {};
    var tags = groupTags(catalogue.sub || [], function(item) { return categories[item.category]; }, function(item) {
      return { id: item.name, name: item.name };
    });
    var hotTag = (hot.tags || []).map(function(item) { return { id: item.name, name: item.name }; });
    if (hotTag.length) tags.unshift({ name: '热门', list: hotTag });
    return { tags: tags, sortList: wySortList() };
  });
}

function fetchTXTags() {
  var tagsUrl = 'https://u.y.qq.com/cgi-bin/musicu.fcg?loginUin=0&hostUin=0&format=json&inCharset=utf-8&outCharset=utf-8&notice=0&platform=wk_v15.json&needNewCode=0&data=%7B%22tags%22%3A%7B%22method%22%3A%22get_all_categories%22%2C%22param%22%3A%7B%22qq%22%3A%22%22%7D%2C%22module%22%3A%22playlist.PlaylistAllCategoriesServer%22%7D%7D';
  var hotTagUrl = 'https://c.y.qq.com/node/pc/wk_v15/category_playlist.html';
  return Promise.all([
    makeRequest(tagsUrl, { headers: { 'User-Agent': 'Mozilla/5.0' } }),
    makeRequest(hotTagUrl, { headers: { 'User-Agent': 'Mozilla/5.0' } }),
  ]).then(function(results) {
    var body = results[0].body || {};
    var html = String(results[1].body || '');
    var tags = ((body.tags || {}).data || {}).v_group || [];
    var mapped = tags.map(function(group) {
      return {
        name: group.group_name,
        list: (group.v_item || []).map(function(item) {
          return { id: String(item.id), name: item.name };
        }),
      };
    });
    var hotTag = [];
    var m = html.match(/class="c_bg_link js_tag_item" data-id="\w+">.+?<\/a>/g) || [];
    m.forEach(function(tagHtml) {
      var result = tagHtml.match(/data-id="(\w+)">(.+?)<\/a>/);
      if (result) hotTag.push({ id: String(result[1]), name: result[2] });
    });
    if (hotTag.length) mapped.unshift({ name: '热门', list: hotTag });
    return { tags: mapped, sortList: txSortList() };
  });
}

function fetchKGTags() {
  var url = 'http://www2.kugou.kugou.com/yueku/v9/special/getSpecial?is_smarty=1&';
  return makeRequest(url, { headers: { 'User-Agent': 'Mozilla/5.0' } }).then(function(r) {
    var body = r.body || {};
    if (body.status !== 1 || !body.data) throw new Error('KG tags failed');
    var hotTag = [];
    Object.keys(body.data.hotTag || {}).forEach(function(key) {
      var item = body.data.hotTag[key];
      hotTag.push({ id: String(item.special_id), name: item.special_name });
    });
    var tags = Object.keys(body.data.tagids || {}).map(function(name) {
      return {
        name: name,
        list: ((body.data.tagids[name] || {}).data || []).map(function(tag) {
          return { id: String(tag.id), name: tag.name };
        }),
      };
    });
    if (hotTag.length) tags.unshift({ name: '热门', list: hotTag });
    return { tags: tags, sortList: kgSortList() };
  });
}

function fetchKWTags() {
  var tagsUrl = 'http://wapi.kuwo.cn/api/pc/classify/playlist/getTagList?cmd=rcm_keyword_playlist&user=0&prod=kwplayer_pc_9.0.5.0&vipver=9.0.5.0&source=kwplayer_pc_9.0.5.0&loginUid=0&loginSid=0&appUid=76039576';
  var hotTagUrl = 'http://wapi.kuwo.cn/api/pc/classify/playlist/getRcmTagList?loginUid=0&loginSid=0&appUid=76039576';
  return Promise.all([
    makeRequest(tagsUrl, { headers: { 'User-Agent': 'Mozilla/5.0' } }),
    makeRequest(hotTagUrl, { headers: { 'User-Agent': 'Mozilla/5.0' } }),
  ]).then(function(results) {
    var tagsBody = results[0].body || {};
    var hotBody = results[1].body || {};
    if (tagsBody.code !== 200) throw new Error('KW tags failed');
    var tags = (tagsBody.data || []).map(function(group) {
      return {
        name: group.name,
        list: (group.data || []).map(function(item) {
          return { id: String(item.id) + '-' + String(item.digest), name: item.name };
        }),
      };
    });
    var hotTag = (((hotBody.data || [])[0] || {}).data || []).map(function(item) {
      return { id: String(item.id) + '-' + String(item.digest), name: item.name };
    });
    if (hotTag.length) tags.unshift({ name: '热门', list: hotTag });
    return { tags: tags, sortList: kwSortList() };
  });
}

function fetchMGTags() {
  var url = 'https://app.c.nf.migu.cn/pc/v1.0/template/musiclistplaza-taglist/release';
  var headers = { 'User-Agent': 'Mozilla/5.0 (iPhone; CPU iPhone OS 13_2_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0.3 Mobile/15E148 Safari/604.1', 'Referer': 'https://m.music.migu.cn/' };
  return makeRequest(url, { headers: headers }).then(function(r) {
    var body = r.body || {};
    if (body.code !== '000000') throw new Error('MG tags failed');
    var raw = body.data || [];
    var tags = raw.slice(1).map(function(group) {
      return {
        name: group.header && group.header.title || '',
        list: (group.content || []).map(function(item) {
          return { id: String((item.texts || [])[1] || ''), name: String((item.texts || [])[0] || '') };
        }).filter(function(item) { return item.id && item.name; }),
      };
    });
    var hotTag = (((raw[0] || {}).content) || []).map(function(item) {
      return { id: String((item.texts || [])[1] || ''), name: String((item.texts || [])[0] || '') };
    }).filter(function(item) { return item.id && item.name; });
    if (hotTag.length) tags.unshift({ name: '热门', list: hotTag });
    return { tags: tags, sortList: mgSortList() };
  });
}

function fetchWYList() {
  var cat = tagId || '全部';
  var url = 'https://music.163.com/api/playlist/list?cat=' + encodeURIComponent(cat) + '&order=' + encodeURIComponent(sortId || 'hot') + '&limit=' + limit + '&offset=' + (limit * (page - 1)) + '&total=true';
  return makeRequest(url, { headers: { 'User-Agent': 'Mozilla/5.0', 'Referer': 'https://music.163.com/' } }).then(function(r) {
    var body = r.body || {};
    if (r.statusCode !== 200 || !Array.isArray(body.playlists)) throw new Error('WY list failed');
    return { list: mapPlaylist(body.playlists, 'wy'), total: Number(body.total || 0), limit: limit };
  });
}

function fetchTXList() {
  var url;
  if (tagId) {
    url = 'https://u.y.qq.com/cgi-bin/musicu.fcg?loginUin=0&hostUin=0&format=json&inCharset=utf-8&outCharset=utf-8&notice=0&platform=wk_v15.json&needNewCode=0&data=' + encodeURIComponent(JSON.stringify({
      comm: { cv: 1602, ct: 20 },
      playlist: {
        method: 'get_category_content',
        param: { titleid: parseInt(tagId, 10), caller: '0', category_id: parseInt(tagId, 10), size: limit, page: page - 1, use_page: 1 },
        module: 'playlist.PlayListCategoryServer',
      },
    }));
  } else {
    url = 'https://u.y.qq.com/cgi-bin/musicu.fcg?loginUin=0&hostUin=0&format=json&inCharset=utf-8&outCharset=utf-8&notice=0&platform=wk_v15.json&needNewCode=0&data=' + encodeURIComponent(JSON.stringify({
      comm: { cv: 1602, ct: 20 },
      playlist: {
        method: 'get_playlist_by_tag',
        param: { id: 10000000, sin: limit * (page - 1), size: limit, order: parseInt(sortId || '5', 10), cur_page: page },
        module: 'playlist.PlayListPlazaServer',
      },
    }));
  }
  return makeRequest(url, { headers: { 'User-Agent': 'Mozilla/5.0' } }).then(function(r) {
    var body = r.body || {};
    if (body.code !== 0 || !body.playlist || !body.playlist.data) throw new Error('TX list failed');
    if (tagId) {
      var content = ((body.playlist.data || {}).content || {}).v_item || [];
      var mapped = content.map(function(item) { return mapPlaylist([item.basic || {}], 'tx')[0]; }).filter(Boolean);
      return { list: mapped, total: Number((((body.playlist.data || {}).content) || {}).total_cnt || 0), limit: limit };
    }
    return { list: mapPlaylist((body.playlist.data || {}).v_playlist || [], 'tx'), total: Number((body.playlist.data || {}).total || 0), limit: limit };
  });
}

function fetchKGList() {
  var infoUrl = 'http://www2.kugou.kugou.com/yueku/v9/special/getSpecial?is_smarty=1&cdn=cdn&t=5&c=' + encodeURIComponent(tagId || '');
  var listUrl = 'http://www2.kugou.kugou.com/yueku/v9/special/getSpecial?is_ajax=1&cdn=cdn&t=' + encodeURIComponent(sortId || '5') + '&c=' + encodeURIComponent(tagId || '') + '&p=' + page;
  var tasks = [
    makeRequest(listUrl, { headers: { 'User-Agent': 'Mozilla/5.0' } }),
    makeRequest(infoUrl, { headers: { 'User-Agent': 'Mozilla/5.0' } }),
  ];
  if (!tagId && page === 1 && String(sortId || '5') === '5') {
    tasks.push(makeRequest('http://everydayrec.service.kugou.com/guess_special_recommend', {
      method: 'POST',
      headers: { 'User-Agent': 'KuGou2012-8275-web_browser_event_handler', 'Content-Type': 'application/json' },
      body: {
        appid: 1001,
        clienttime: 1566798337219,
        clientver: 8275,
        key: 'f1f93580115bb106680d2375f8032d96',
        mid: '21511157a05844bd085308bc76ef3343',
        platform: 'pc',
        userid: '262643156',
        return_min: 6,
        return_max: 15,
      },
    }));
  }
  return Promise.all(tasks).then(function(results) {
    var listBody = results[0].body || {};
    var infoBody = results[1].body || {};
    if (!listBody || listBody.status !== 1 || !infoBody || infoBody.status !== 1) throw new Error('KG list failed');
    var list = mapPlaylist(listBody.special_db || [], 'kg');
    if (results[2] && results[2].body && results[2].body.status === 1) {
      list = mapPlaylist((results[2].body.data || {}).special_list || [], 'kg').concat(list);
    }
    return { list: list, total: Number((((infoBody.data || {}).params) || {}).total || 0), limit: Number((((infoBody.data || {}).params) || {}).pagesize || limit) || limit };
  });
}

function fetchKWList() {
  if (!tagId) {
    var url = 'http://wapi.kuwo.cn/api/pc/classify/playlist/getRcmPlayList?loginUid=0&loginSid=0&appUid=76039576&&pn=' + page + '&rn=' + limit + '&order=' + encodeURIComponent(sortId || 'hot');
    return makeRequest(url, { headers: { 'User-Agent': 'Mozilla/5.0' } }).then(function(r) {
      var body = r.body || {};
      if (body.code !== 200) throw new Error('KW list failed');
      return { list: mapPlaylist(((((body.data || {}).data) || [])), 'kw'), total: Number((body.data || {}).total || 0), limit: Number((body.data || {}).rn || limit) || limit };
    });
  }
  var parts = String(tagId).split('-');
  var tagRawId = parts[0] || '';
  var tagType = parts[1] || '';
  var listUrl = tagType === '10000'
    ? ('http://wapi.kuwo.cn/api/pc/classify/playlist/getTagPlayList?loginUid=0&loginSid=0&appUid=76039576&pn=' + page + '&id=' + encodeURIComponent(tagRawId) + '&rn=' + limit)
    : ('http://mobileinterfaces.kuwo.cn/er.s?type=get_pc_qz_data&f=web&id=' + encodeURIComponent(tagRawId) + '&prod=pc');
  return makeRequest(listUrl, { headers: { 'User-Agent': 'Mozilla/5.0' } }).then(function(r) {
    var body = r.body || {};
    if (tagType === '10000') {
      if (body.code !== 200) throw new Error('KW list failed');
      return { list: mapPlaylist(((((body.data || {}).data) || [])), 'kw'), total: Number((body.data || {}).total || 0), limit: Number((body.data || {}).rn || limit) || limit };
    }
    if (!Array.isArray(body)) throw new Error('KW list failed');
    var list = [];
    body.forEach(function(group) {
      if (!group.label) return;
      list = list.concat(mapPlaylist(group.list || [], 'kw'));
    });
    return { list: list, total: 1000, limit: 1000 };
  });
}

function collectMGList(listData, list, ids) {
  list = list || [];
  ids = ids || {};
  (listData || []).forEach(function(item) {
    if (item.contents) collectMGList(item.contents, list, ids);
    else if (item.resType == '2021' && !ids[item.resId]) {
      ids[item.resId] = true;
      list.push({
        logEvent: { contentId: item.resId },
        title: item.txt,
        img: item.img,
        txt2: item.txt2,
      });
    }
  });
  return list;
}

function fetchMGList() {
  var headers = { 'User-Agent': 'Mozilla/5.0 (iPhone; CPU iPhone OS 13_2_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0.3 Mobile/15E148 Safari/604.1', 'Referer': 'https://m.music.migu.cn/' };
  var url = !tagId
    ? ('https://app.c.nf.migu.cn/pc/bmw/page-data/playlist-square-recommend/v1.0?templateVersion=2&pageNo=' + page)
    : ('https://app.c.nf.migu.cn/pc/v1.0/template/musiclistplaza-listbytag/release?pageNumber=' + page + '&templateVersion=2&tagId=' + encodeURIComponent(tagId));
  return makeRequest(url, { headers: headers }).then(function(r) {
    var body = r.body || {};
    if (body.code !== '000000') throw new Error('MG list failed');
    var rawList = body.data.contents ? collectMGList(body.data.contents) : (((body.data || {}).contentItemList || [])[1] || {}).itemList || [];
    return { list: mapPlaylist(rawList, 'mg'), total: 99999, limit: 30 };
  });
}

if (mode === 'tags') {
  var tagFetchers = { wy: fetchWYTags, tx: fetchTXTags, kg: fetchKGTags, kw: fetchKWTags, mg: fetchMGTags };
  var tf = tagFetchers[source];
  if (!tf) process.stdout.write(JSON.stringify({ success: false, error: 'Unsupported source: ' + source, tags: [], sortList: [] }));
  else tf().then(function(result) {
    process.stdout.write(JSON.stringify({ success: true, tags: result.tags || [], hotTag: result.hotTag || [], sortList: result.sortList || [] }));
  }).catch(function(err) {
    process.stdout.write(JSON.stringify({ success: false, error: err && err.message || 'unknown error', tags: [], sortList: [] }));
  });
} else {
  var listFetchers = { wy: fetchWYList, tx: fetchTXList, kg: fetchKGList, kw: fetchKWList, mg: fetchMGList };
  var lf = listFetchers[source];
  if (!lf) process.stdout.write(JSON.stringify({ success: false, error: 'Unsupported source: ' + source, list: [], total: 0, limit: limit }));
  else lf().then(function(result) {
    process.stdout.write(JSON.stringify({ success: true, list: result.list || [], total: Number(result.total || 0), limit: Number(result.limit || limit) || limit }));
  }).catch(function(err) {
    process.stdout.write(JSON.stringify({ success: false, error: err && err.message || 'unknown error', list: [], total: 0, limit: limit }));
  });
}
`

func (api *Router) addOnlineSearchRoutes(r chi.Router) {
	r.Get("/online/search/hot", api.onlineHotSearch)
	r.Get("/online/search", api.onlineSearch)
	r.Get("/online/playlist/tags", api.onlinePlaylistTags)
	r.Get("/online/playlist/list", api.onlinePlaylistList)
	r.Get("/online/playlist/search", api.onlinePlaylistSearch)
	r.Get("/online/playlist/detail", api.onlinePlaylistDetail)
}

type onlinePlaylistTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type playlistTagGroup struct {
	Name string              `json:"name"`
	List []onlinePlaylistTag `json:"list"`
}

func (api *Router) onlinePlaylistTags(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "wy"
	}

	validSources := map[string]bool{"wy": true, "tx": true, "kg": true, "kw": true, "mg": true}
	if !validSources[source] {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	meta, err := fetchOnlinePlaylistMeta(ctx, source)
	if err != nil {
		log.Warn(r.Context(), "Online playlist tags failed", "source", source, "err", err)
		writeJSON(w, map[string]any{
			"source":   source,
			"tags":     []playlistTagGroup{},
			"sortList": []map[string]string{},
			"error":    err.Error(),
		})
		return
	}

	writeJSON(w, map[string]any{
		"source":   source,
		"tags":     meta.Tags,
		"sortList": meta.SortList,
	})
}

func (api *Router) onlinePlaylistList(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "wy"
	}

	validSources := map[string]bool{"wy": true, "tx": true, "kg": true, "kw": true, "mg": true}
	if !validSources[source] {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	sortID := strings.TrimSpace(r.URL.Query().Get("sortId"))
	tagID := strings.TrimSpace(r.URL.Query().Get("tagId"))

	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	result, err := fetchOnlinePlaylistPlazaList(ctx, source, sortID, tagID, page, 30)
	if err != nil {
		log.Warn(r.Context(), "Online playlist list failed", "source", source, "tagId", tagID, "sortId", sortID, "err", err)
		writeJSON(w, map[string]any{
			"source": source,
			"tagId":  tagID,
			"sortId": sortID,
			"page":   page,
			"total":  0,
			"list":   []map[string]any{},
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, map[string]any{
		"source": source,
		"tagId":  tagID,
		"sortId": sortID,
		"page":   page,
		"total":  result.Total,
		"list":   result.List,
		"limit":  result.Limit,
	})
}

func (api *Router) onlinePlaylistSearch(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "wy"
	}

	validSources := map[string]bool{"wy": true, "tx": true, "kg": true, "kw": true, "mg": true}
	if !validSources[source] {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	if keyword == "" {
		http.Error(w, "keyword is required", http.StatusBadRequest)
		return
	}

	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	list, total, err := fetchOnlineSearchList(ctx, source, "playlist", keyword, page, 30, "")
	if err != nil {
		log.Warn(r.Context(), "Online playlist search failed", "source", source, "keyword", keyword, "err", err)
		writeJSON(w, map[string]any{
			"source":  source,
			"keyword": keyword,
			"page":    page,
			"total":   0,
			"list":    []map[string]any{},
			"error":   err.Error(),
		})
		return
	}

	writeJSON(w, map[string]any{
		"source":  source,
		"keyword": keyword,
		"page":    page,
		"total":   total,
		"list":    list,
	})
}

func (api *Router) onlinePlaylistDetail(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "wy"
	}

	validSources := map[string]bool{"wy": true, "tx": true, "kg": true, "kw": true, "mg": true}
	if !validSources[source] {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	playlistID := strings.TrimSpace(r.URL.Query().Get("id"))
	if playlistID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	limit := 30
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	detail, err := fetchOnlinePlaylistDetail(ctx, source, playlistID, page, limit)
	if err != nil {
		log.Warn(r.Context(), "Online playlist detail failed", "source", source, "id", playlistID, "err", err)
		writeJSON(w, map[string]any{
			"source": source,
			"id":     playlistID,
			"list":   []map[string]any{},
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, map[string]any{
		"source": source,
		"id":     playlistID,
		"total":  detail.Total,
		"list":   detail.List,
		"info":   detail.Info,
	})
}

func (api *Router) onlineHotSearch(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "wy"
	}

	validSources := map[string]bool{"wy": true, "tx": true, "kg": true, "kw": true, "mg": true}
	if !validSources[source] {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	// Check if user is forcing a refresh
	forceRefresh := r.URL.Query().Get("refresh") == "true"

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	list, err := fetchHotSearchList(ctx, source, forceRefresh)
	if err != nil {
		log.Warn(r.Context(), "Hot search failed", "source", source, "err", err)
		writeJSON(w, map[string]any{"source": source, "list": []string{}})
		return
	}

	writeJSON(w, map[string]any{"source": source, "list": list})
}

func (api *Router) onlineSearch(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	source := r.URL.Query().Get("source")
	if source == "" {
		source = "wy"
	}

	searchType := r.URL.Query().Get("type")
	if searchType == "" {
		searchType = "song"
	}
	if searchType != "song" && searchType != "singer" && searchType != "album" && searchType != "playlist" {
		http.Error(w, "invalid type", http.StatusBadRequest)
		return
	}

	validSources := map[string]bool{"wy": true, "tx": true, "kg": true, "kw": true, "mg": true}
	if !validSources[source] {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}

	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	list, total, err := fetchOnlineSearchList(ctx, source, searchType, name, page, limit, "")
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(strings.ToLower(errMsg), "econnreset") {
			if source == "kg" {
				errMsg = "当前网络无法连接酷狗搜索服务，请稍后重试或切换到网易/QQ/酷我源"
			} else {
				errMsg = "当前网络连接不稳定，搜索服务暂时不可用，请稍后重试"
			}
		}
		log.Warn(r.Context(), "Online search failed", "source", source, "type", searchType, "name", name, "err", err)
		writeJSON(w, map[string]any{
			"source": source,
			"type":   searchType,
			"name":   name,
			"page":   page,
			"limit":  limit,
			"total":  0,
			"list":   []map[string]any{},
			"error":  errMsg,
		})
		return
	}

	writeJSON(w, map[string]any{
		"source": source,
		"type":   searchType,
		"name":   name,
		"page":   page,
		"limit":  limit,
		"total":  total,
		"list":   list,
	})
}

func fetchHotSearchList(ctx context.Context, source string, forceRefresh bool) ([]string, error) {
	// Check cache first if not forcing refresh
	if !forceRefresh {
		if cachedList, ok := loadHotSearchCache(source); ok {
			log.Debug(ctx, "Using cached hot search", "source", source, "count", len(cachedList))
			return cachedList, nil
		}
	}

	// Fetch from API
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("node not available")
	}

	cmd := exec.CommandContext(ctx, "node", "-e", nodeHotSearchScript)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ND_SOURCE=%s", source),
		"ND_TIMEOUT_MS=8000",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("node error: %s", bytes.TrimSpace(stderr.Bytes()))
		}
		return nil, err
	}

	var result hotSearchNodeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("invalid node response: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("%s", result.Error)
	}

	// Save to cache
	if err := saveHotSearchCache(source, result.List); err != nil {
		log.Debug(ctx, "Failed to save hot search cache", "source", source, "err", err)
	} else {
		log.Debug(ctx, "Saved hot search cache", "source", source, "count", len(result.List))
	}

	return result.List, nil
}

func fetchOnlineSearchList(ctx context.Context, source, searchType, keyword string, page, limit int, sortID string) ([]map[string]any, int, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return nil, 0, fmt.Errorf("node not available")
	}

	encodedKeyword := base64.StdEncoding.EncodeToString([]byte(keyword))

	cmd := exec.CommandContext(ctx, "node", "-e", nodeSearchScript)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ND_SOURCE=%s", source),
		fmt.Sprintf("ND_TYPE=%s", searchType),
		fmt.Sprintf("ND_PAGE=%d", page),
		fmt.Sprintf("ND_LIMIT=%d", limit),
		fmt.Sprintf("ND_SORT_ID=%s", sortID),
		fmt.Sprintf("ND_KEYWORD_B64=%s", encodedKeyword),
		"ND_TIMEOUT_MS=8000",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, 0, fmt.Errorf("node error: %s", bytes.TrimSpace(stderr.Bytes()))
		}
		return nil, 0, err
	}

	var result onlineSearchNodeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, 0, fmt.Errorf("invalid node response: %w", err)
	}

	if !result.Success {
		return nil, 0, fmt.Errorf("%s", result.Error)
	}

	if result.List == nil {
		return []map[string]any{}, 0, nil
	}

	total := result.Total
	if total < 0 {
		total = 0
	}
	if total == 0 {
		total = len(result.List)
	}

	return result.List, total, nil
}

func fetchOnlinePlaylistDetail(ctx context.Context, source, playlistID string, page, limit int) (onlineSearchNodeResult, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return onlineSearchNodeResult{}, fmt.Errorf("node not available")
	}

	cmd := exec.CommandContext(ctx, "node", "-e", nodePlaylistDetailScript)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ND_SOURCE=%s", source),
		fmt.Sprintf("ND_PLAYLIST_ID=%s", playlistID),
		fmt.Sprintf("ND_PAGE=%d", page),
		fmt.Sprintf("ND_LIMIT=%d", limit),
		"ND_TIMEOUT_MS=10000",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return onlineSearchNodeResult{}, fmt.Errorf("node error: %s", bytes.TrimSpace(stderr.Bytes()))
		}
		return onlineSearchNodeResult{}, err
	}

	var result onlineSearchNodeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return onlineSearchNodeResult{}, fmt.Errorf("invalid node response: %w", err)
	}

	if !result.Success {
		return onlineSearchNodeResult{}, fmt.Errorf("%s", result.Error)
	}

	if result.List == nil {
		result.List = []map[string]any{}
	}
	if result.Total < 0 {
		result.Total = 0
	}
	if result.Total == 0 {
		result.Total = len(result.List)
	}

	return result, nil
}

func fetchOnlinePlaylistMeta(ctx context.Context, source string) (onlinePlaylistTagsNodeResult, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return onlinePlaylistTagsNodeResult{}, fmt.Errorf("node not available")
	}

	cmd := exec.CommandContext(ctx, "node", "-e", nodePlaylistBrowseScript)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ND_SOURCE=%s", source),
		"ND_BROWSE_MODE=tags",
		"ND_TIMEOUT_MS=10000",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return onlinePlaylistTagsNodeResult{}, fmt.Errorf("node error: %s", bytes.TrimSpace(stderr.Bytes()))
		}
		return onlinePlaylistTagsNodeResult{}, err
	}

	var result onlinePlaylistTagsNodeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return onlinePlaylistTagsNodeResult{}, fmt.Errorf("invalid node response: %w", err)
	}
	if !result.Success {
		return onlinePlaylistTagsNodeResult{}, fmt.Errorf("%s", result.Error)
	}
	if result.Tags == nil {
		result.Tags = []playlistTagGroup{}
	}
	if result.SortList == nil {
		result.SortList = []map[string]string{}
	}
	return result, nil
}

func fetchOnlinePlaylistPlazaList(ctx context.Context, source, sortID, tagID string, page, limit int) (onlinePlaylistPlazaNodeResult, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return onlinePlaylistPlazaNodeResult{}, fmt.Errorf("node not available")
	}

	cmd := exec.CommandContext(ctx, "node", "-e", nodePlaylistBrowseScript)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ND_SOURCE=%s", source),
		"ND_BROWSE_MODE=list",
		fmt.Sprintf("ND_SORT_ID=%s", sortID),
		fmt.Sprintf("ND_TAG_ID=%s", tagID),
		fmt.Sprintf("ND_PAGE=%d", page),
		fmt.Sprintf("ND_LIMIT=%d", limit),
		"ND_TIMEOUT_MS=12000",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return onlinePlaylistPlazaNodeResult{}, fmt.Errorf("node error: %s", bytes.TrimSpace(stderr.Bytes()))
		}
		return onlinePlaylistPlazaNodeResult{}, err
	}

	var nodeResult onlinePlaylistPlazaNodeResult
	if err := json.Unmarshal(stdout.Bytes(), &nodeResult); err != nil {
		return onlinePlaylistPlazaNodeResult{}, fmt.Errorf("invalid node response: %w", err)
	}
	if !nodeResult.Success {
		return onlinePlaylistPlazaNodeResult{}, fmt.Errorf("%s", nodeResult.Error)
	}
	if nodeResult.List == nil {
		nodeResult.List = []map[string]any{}
	}
	if nodeResult.Total < 0 {
		nodeResult.Total = 0
	}
	if nodeResult.Limit <= 0 {
		nodeResult.Limit = limit
	}
	return nodeResult, nil
}
