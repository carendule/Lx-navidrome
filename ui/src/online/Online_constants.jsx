// View modes
export const VIEW_MODES = {
    song: 'song',
    playlist: 'playlist',
}

// Data sources
export const SOURCES = [
    { key: 'wy', label: '网易云' },
    { key: 'tx', label: 'QQ音乐' },
    { key: 'kg', label: '酷狗' },
    { key: 'kw', label: '酷我' },
    { key: 'mg', label: '咪咕' },
]

// Search types
export const TYPES = [
    { key: 'song', label: '歌曲' },
    { key: 'singer', label: '歌手' },
    { key: 'album', label: '专辑' },
]

// Source badge styles
export const SOURCE_BADGE = {
    wy: { bg: '#fde2e2', color: '#a13030', name: '网易' },
    tx: { bg: '#d7f6e8', color: '#1f7a53', name: 'QQ' },
    kg: { bg: '#dfe8ff', color: '#2c4ca3', name: '酷狗' },
    kw: { bg: '#fdeccf', color: '#935b00', name: '酷我' },
    mg: { bg: '#ffe1ea', color: '#a3335d', name: '咪咕' },
}

// Rank colors for hot search
export const RANK_COLORS = [
    { bg: '#f5483b', color: '#fff' },
    { bg: '#f97c3c', color: '#fff' },
    { bg: '#ffb03a', color: '#fff' },
]

// Playlist sort options by source
export const PLAYLIST_SORT_OPTIONS_BY_SOURCE = {
    wy: [
        { key: 'hot', label: '最热' },
        { key: 'new', label: '最新' },
    ],
    tx: [
        { key: 'hot', label: '最热' },
        { key: 'new', label: '最新' },
    ],
    kg: [
        { key: '5', label: '推荐' },
        { key: '6', label: '最热' },
        { key: '7', label: '最新' },
        { key: '3', label: '热藏' },
        { key: '8', label: '飙升' },
    ],
    kw: [
        { key: 'new', label: '最新' },
        { key: 'hot', label: '最热' },
    ],
    bd: [
        { key: 'hot', label: '最热' },
        { key: 'new', label: '最新' },
    ],
}

// Quality metadata
export const QUALITY_META = {
    master: { label: 'Master', bg: '#f0e4ff', color: '#6d35b2' },
    flac24bit: { label: 'Hi-Res', bg: '#fff2cc', color: '#8d5f00' },
    ape: { label: 'APE', bg: '#ffe4cc', color: '#9a4d00' },
    flac: { label: 'FLAC', bg: '#dff6e7', color: '#1f7a53' },
    '320k': { label: '320k', bg: '#dce8ff', color: '#2c4ca3' },
    '128k': { label: '128k', bg: '#ececec', color: '#555' },
}

export const QUALITY_ORDER = ['master', 'flac24bit', 'ape', 'flac', '320k', '128k']

// Utility functions
export const getSourceBadge = (src) =>
    SOURCE_BADGE[src] || { bg: '#e7e7e7', color: '#666', name: src || '未知' }

export const getPlaylistSortOptions = (source) =>
    PLAYLIST_SORT_OPTIONS_BY_SOURCE[source] || PLAYLIST_SORT_OPTIONS_BY_SOURCE.wy

export const truncateSourceName = (name, max = 5) => {
    if (!name) return ''
    if (name.length <= max) return name
    return `${name.slice(0, max)}…`
}

export const formatCompactCount = (value) => {
    if (typeof value === 'string' && value.trim()) return value.trim()
    const num = Number(value)
    if (!Number.isFinite(num) || num <= 0) return '0'
    if (num < 10000) return String(Math.round(num))
    return `${(num / 10000).toFixed(num >= 100000 ? 0 : 1)}万`
}

export const formatDuration = (value) => {
    const num = Number(value)
    if (!Number.isFinite(num) || num <= 0) return '--:--'
    const totalSeconds = Math.floor(num >= 1000 ? num / 1000 : num)
    const mm = String(Math.floor(totalSeconds / 60)).padStart(2, '0')
    const ss = String(totalSeconds % 60).padStart(2, '0')
    return `${mm}:${ss}`
}

export const formatFileSize = (input) => {
    if (typeof input === 'string') {
        const text = input.trim()
        if (!text) return '大小未知'
        if (/^\d+(\.\d+)?\s*(B|KB|MB|GB|TB)$/i.test(text)) return text.toUpperCase()
        if (/^(\d+\.?\d*)([KMGT])$/i.test(text)) {
            const m = text.match(/^(\d+\.?\d*)([KMGT])$/i)
            const num = parseFloat(m[1])
            const unit = m[2].toUpperCase()
            const map = { K: 'KB', M: 'MB', G: 'GB', T: 'TB' }
            return `${num.toFixed(2)} ${map[unit] || unit}`
        }
        const asNumber = Number(text)
        if (Number.isFinite(asNumber) && asNumber > 0) {
            input = asNumber
        } else {
            return text
        }
    }

    const num = Number(input)
    if (!Number.isFinite(num) || num <= 0) return '大小未知'
    if (num < 1024) return `${num} B`
    if (num < 1024 * 1024) return `${(num / 1024).toFixed(1)} KB`
    if (num < 1024 * 1024 * 1024) return `${(num / 1024 / 1024).toFixed(1)} MB`
    return `${(num / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export const getQualityKeys = (item) => {
    const raw =
        item?.qualitys || item?._qualitys || item?.types || item?._types || {}
    const explicit = item?.quality || item?.type
    const has = (key) => {
        if (Array.isArray(raw)) return raw.some((v) => v?.type === key)
        return Boolean(raw?.[key])
    }

    const keys = QUALITY_ORDER.filter((key) => has(key))
    if (explicit && QUALITY_META[explicit] && !keys.includes(explicit)) {
        keys.unshift(explicit)
    }
    return keys
}

export const getRawQualitySizeMap = (item) => {
    const rawTypes =
        item?.types ||
        item?._types ||
        item?.qualitys ||
        item?._qualitys ||
        (item?.meta &&
            (item.meta.types ||
                item.meta._types ||
                item.meta.qualitys ||
                item.meta._qualitys)) ||
        {}
    const result = {}

    if (Array.isArray(rawTypes)) {
        rawTypes.forEach((entry) => {
            const type = entry?.type
            if (!type) return
            if (entry?.size != null && entry.size !== '') result[type] = entry.size
        })
        return result
    }

    if (rawTypes && typeof rawTypes === 'object') {
        Object.entries(rawTypes).forEach(([key, value]) => {
            if (value && typeof value === 'object') {
                if (value.size != null && value.size !== '') result[key] = value.size
                return
            }
            if (typeof value === 'number' && value > 0) result[key] = value
            if (typeof value === 'string' && value.trim()) result[key] = value.trim()
        })
    }

    return result
}

export const getQualitySizeMap = (item) => {
    const meta = item?.meta || {}
    const source = item?.source || ''
    const result = getRawQualitySizeMap(item)

    const assignIfMissing = (key, value) => {
        if (result[key] != null && result[key] !== '' && Number(result[key]) > 0)
            return
        if (value == null || value === '') return
        const num = Number(value)
        if (Number.isFinite(num) && num > 0) result[key] = num
    }

    if (source === 'wy') {
        assignIfMissing('flac24bit', meta.hr?.size)
        assignIfMissing('flac', meta.sq?.size)
        assignIfMissing('320k', meta.h?.size)
        assignIfMissing('128k', meta.l?.size || meta.m?.size || meta.h?.size)
        return result
    }

    if (source === 'tx') {
        const file = meta.file || {}
        assignIfMissing('flac24bit', file.size_hires)
        assignIfMissing('flac', file.size_flac)
        assignIfMissing('320k', file.size_320mp3)
        assignIfMissing('128k', file.size_128mp3)
        return result
    }

    if (source === 'kg') {
        assignIfMissing('flac24bit', meta.ResFileSize)
        assignIfMissing('ape', meta.filesize_ape || meta.APEFileSize)
        assignIfMissing('flac', meta.sqfilesize || meta.SQFileSize)
        assignIfMissing('320k', meta['320filesize'] || meta.HQFileSize)
        assignIfMissing('128k', meta.FileSize || meta.filesize)
        return result
    }

    if (source === 'mg') {
        const rates =
            meta.audioFormats || meta.newRateFormats || meta.rateFormats || []
        rates.forEach((rate) => {
            const t = String(
                (rate && (rate.formatType || rate.qualityType || rate.type)) || '',
            ).toUpperCase()
            const rawSize =
                rate?.asize ??
                rate?.isize ??
                rate?.size ??
                rate?.fileSize ??
                rate?.androidSize ??
                rate?.pcSize ??
                0
            const size = Number(rawSize)
            if (!size) return
            if (t === 'ZQ24' || t.includes('HIRES'))
                assignIfMissing('flac24bit', size)
            else if (t === 'SQ' || t.includes('FLAC')) assignIfMissing('flac', size)
            else if (t === 'HQ' || t.includes('320')) assignIfMissing('320k', size)
            else if (t === 'PQ' || t.includes('128')) assignIfMissing('128k', size)
        })
        return result
    }

    if (source === 'kw') {
        const nminfo = String(meta.N_MINFO || meta.n_minfo || '')
        if (nminfo) {
            const setKWSize = (key, size) => {
                if (result[key] == null || result[key] === '') result[key] = size
            }
            const re = /level:(\w+),bitrate:(\d+),format:(\w+),size:([\w.]+)/gi
            let m
            while ((m = re.exec(nminfo)) !== null) {
                const bitrate = Number(m[2])
                const fmt = m[3].toLowerCase()
                const size = m[4]
                if (!size || size === '0') continue
                if (bitrate === 4000 || /24bit|hi.?res/.test(fmt))
                    setKWSize('flac24bit', size)
                else if (bitrate === 2000 || fmt === 'flac') setKWSize('flac', size)
                else if (fmt === 'ape') setKWSize('ape', size)
                else if (bitrate >= 320) setKWSize('320k', size)
                else setKWSize('128k', size)
            }
        }
        return result
    }

    return result
}

export const getQualityOptions = (item) => {
    const keys = getQualityKeys(item)
    const sizeMap = getQualitySizeMap(item)
    return keys.map((key) => {
        const label = QUALITY_META[key]?.label || key
        const size = sizeMap[key]
        return {
            key,
            label,
            size,
            sizeText: formatFileSize(size),
        }
    })
}

export const parseDownloadFileName = (contentDisposition) => {
    if (!contentDisposition) return 'download'
    const utf8Match = contentDisposition.match(/filename\*=UTF-8''([^;]+)/i)
    if (utf8Match?.[1]) {
        try {
            return decodeURIComponent(utf8Match[1])
        } catch (e) {
            return utf8Match[1]
        }
    }
    const basicMatch = contentDisposition.match(/filename="?([^";]+)"?/i)
    return basicMatch?.[1] || 'download'
}
