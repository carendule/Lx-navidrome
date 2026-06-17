import React, { useCallback, useEffect, useState } from 'react'
import {
    Card,
    CardContent,
    Typography,
    Button,
    TextField,
    CircularProgress,
    Chip,
    Avatar,
    IconButton,
    Select,
    MenuItem,
    Dialog,
    DialogContent,
    DialogTitle,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import RefreshIcon from '@material-ui/icons/Refresh'
import SearchIcon from '@material-ui/icons/Search'
import ArrowBackIcon from '@material-ui/icons/ArrowBack'
import GetAppIcon from '@material-ui/icons/GetApp'
import SyncIcon from '@material-ui/icons/Sync'
import { InputAdornment } from '@material-ui/core'
import { httpClient } from '../dataProvider'
import {
    SOURCE_BADGE,
    QUALITY_META,
    getSourceBadge,
    getQualityKeys,
    formatCompactCount,
    formatDuration,
} from './Online_constants'

const SOURCES = [
    { key: 'wy', label: '网易云' },
    { key: 'tx', label: 'QQ音乐' },
    { key: 'kg', label: '酷狗' },
    { key: 'kw', label: '酷我' },
    { key: 'mg', label: '咪咕' },
]

const darkenHexColor = (hex, amount = 8) => {
    if (typeof hex !== 'string' || !hex.startsWith('#')) return hex
    const value = hex.slice(1)
    if (value.length !== 6) return hex

    const num = Number.parseInt(value, 16)
    if (Number.isNaN(num)) return hex

    const clamp = (n) => Math.max(0, Math.min(255, n))
    const r = clamp(((num >> 16) & 0xff) - amount)
    const g = clamp(((num >> 8) & 0xff) - amount)
    const b = clamp((num & 0xff) - amount)

    return `#${((1 << 24) | (r << 16) | (g << 8) | b)
        .toString(16)
        .slice(1)}`
}

const useStyles = makeStyles((theme) => ({
    root: {
        padding: theme.spacing(2),
    },
    detailSongListContainer: {
        maxHeight: '600px',
        overflowY: 'auto',
        '&::-webkit-scrollbar': {
            width: '8px',
        },
        '&::-webkit-scrollbar-track': {
            backgroundColor: 'transparent',
        },
        '&::-webkit-scrollbar-thumb': {
            backgroundColor: theme.palette.action.disabled,
            borderRadius: '4px',
            '&:hover': {
                backgroundColor: theme.palette.text.secondary,
            },
        },
    },
    loadingBox: {
        display: 'flex',
        justifyContent: 'center',
        padding: theme.spacing(5),
    },
    emptyBox: {
        textAlign: 'center',
        padding: theme.spacing(4),
        color: theme.palette.text.secondary,
    },
    refreshRow: {
        display: 'flex',
        justifyContent: 'center',
        marginTop: theme.spacing(2),
    },
    refreshBtn: {
        borderRadius: 999,
        textTransform: 'none',
        color: theme.palette.text.secondary,
    },
    resultCard: {
        marginTop: theme.spacing(2),
        borderRadius: 12,
        border: `1px solid ${theme.palette.divider}`,
        overflow: 'hidden',
    },
    resultStatus: {
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: theme.spacing(1),
        marginBottom: theme.spacing(1.2),
    },
    paginationRow: {
        marginTop: theme.spacing(1.5),
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: theme.spacing(1),
        flexWrap: 'wrap',
    },
    paginationInfo: {
        color: theme.palette.text.secondary,
        fontSize: '0.86rem',
    },
    paginationControls: {
        display: 'flex',
        alignItems: 'center',
        gap: theme.spacing(1),
        flexWrap: 'wrap',
    },
    jumpInput: {
        width: 88,
        '& .MuiOutlinedInput-input': {
            paddingTop: 8,
            paddingBottom: 8,
            textAlign: 'center',
        },
    },
    playlistTagsRow: {
        display: 'flex',
        flexDirection: 'column',
        gap: theme.spacing(1),
        marginBottom: theme.spacing(1.5),
    },
    playlistTagGroup: {
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        gap: theme.spacing(0.8),
        width: '100%',
        paddingLeft: 48,
    },
    playlistTagGroupLabel: {
        fontSize: '0.75rem',
        fontWeight: 600,
        color: theme.palette.text.secondary,
        minWidth: 40,
        flexShrink: 0,
        marginLeft: -48,
    },
    playlistTagButton: {
        height: 28,
        fontSize: '0.8rem',
        textTransform: 'none',
        border: `1px solid ${theme.palette.divider}`,
        borderRadius: 6,
        padding: theme.spacing(0.5, 1.2),
        backgroundColor: 'transparent',
        color: theme.palette.text.secondary,
        transition: 'all 0.2s ease',
        display: 'flex',
        alignItems: 'center',
        '&:hover': {
            backgroundColor: theme.palette.action.hover,
        },
        '&.selected': {
            backgroundColor: theme.palette.primary.main,
            color: '#fff',
            border: `1px solid ${theme.palette.primary.main}`,
        },
    },
    playlistGridCard: {
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        borderRadius: 14,
        border: 'none',
        overflow: 'hidden',
        backgroundColor: theme.palette.background.paper,
        transition: 'transform 0.18s ease, box-shadow 0.18s ease',
        boxShadow: '0 1px 3px rgba(0, 0, 0, 0.08)',
        cursor: 'pointer',
        '&:hover': {
            transform: 'translateY(-2px)',
            boxShadow: '0 8px 20px rgba(0, 0, 0, 0.12)',
        },
    },
    playlistGrid: {
        display: 'grid',
        gridTemplateColumns: 'repeat(auto-fill, minmax(140px, 1fr))',
        gap: theme.spacing(1.2),
    },
    playlistCover: {
        aspectRatio: '1 / 1',
        position: 'relative',
        overflow: 'hidden',
    },
    playlistCoverOverlay: {
        position: 'absolute',
        inset: 0,
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'space-between',
        padding: theme.spacing(0.95),
        color: '#fff',
        background: 'linear-gradient(180deg, rgba(0,0,0,0.03) 0%, rgba(0,0,0,0.12) 100%)',
    },
    playlistCoverStats: {
        display: 'flex',
        justifyContent: 'flex-end',
        alignItems: 'center',
        gap: theme.spacing(1),
        fontSize: '0.7rem',
        fontWeight: 600,
        opacity: 0.95,
    },
    playlistCardBody: {
        flex: 1,
        minHeight: 0,
        padding: theme.spacing(0.95, 1, 1.05),
        display: 'flex',
        flexDirection: 'column',
        gap: theme.spacing(0.4),
    },
    playlistCardTitle: {
        fontSize: '0.82rem',
        lineHeight: 1.3,
        fontWeight: 700,
        color: theme.palette.text.primary,
        display: '-webkit-box',
        overflow: 'hidden',
        WebkitBoxOrient: 'vertical',
        WebkitLineClamp: 2,
    },
    playlistMetaRow: {
        display: 'flex',
        justifyContent: 'space-between',
        gap: theme.spacing(1),
        flexWrap: 'nowrap',
        color: theme.palette.text.secondary,
        fontSize: '0.72rem',
        '& > span': {
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
        },
    },
    playlistMetricRow: {
        display: 'flex',
        justifyContent: 'space-between',
        gap: theme.spacing(1),
        color: theme.palette.text.secondary,
        fontSize: '0.7rem',
        marginTop: 'auto',
        paddingTop: theme.spacing(0.1),
    },
    searchRow: {
        display: 'flex',
        alignItems: 'center',
        gap: theme.spacing(1),
        marginBottom: theme.spacing(2),
        flexWrap: 'wrap',
        justifyContent: 'flex-start',
    },
    searchInput: {
        width: 320,
        flexShrink: 0,
    },
    selectControl: {
        '& .MuiOutlinedInput-input': {
            paddingTop: 10,
            paddingBottom: 10,
        },
    },
    searchBtn: {
        height: 40,
        minWidth: 80,
        flexShrink: 0,
        textTransform: 'none',
    },
    sourceSelect: {
        minWidth: 110,
    },
    sortSelect: {
        minWidth: 100,
    },
    detailBackRow: {
        marginBottom: theme.spacing(1.2),
    },
    detailBackBtn: {
        textTransform: 'none',
        borderRadius: 999,
        paddingLeft: theme.spacing(1),
        paddingRight: theme.spacing(1.5),
    },
    detailCard: {
        borderRadius: 12,
        border: `1px solid ${theme.palette.divider}`,
        marginBottom: theme.spacing(1.5),
        overflow: 'hidden',
    },
    detailHeader: {
        display: 'flex',
        gap: theme.spacing(2),
        alignItems: 'flex-start',
        [theme.breakpoints.down('sm')]: {
            flexDirection: 'column',
        },
    },
    detailCover: {
        width: 132,
        height: 132,
        borderRadius: 12,
        flexShrink: 0,
        backgroundColor: theme.palette.action.hover,
    },
    detailMeta: {
        minWidth: 0,
        flex: 1,
        display: 'flex',
        flexDirection: 'column',
        gap: theme.spacing(0.8),
    },
    detailMetaRow: {
        minWidth: 0,
        flex: 1,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: theme.spacing(2),
        [theme.breakpoints.down('sm')]: {
            width: '100%',
            flexDirection: 'column',
            alignItems: 'stretch',
            gap: theme.spacing(1.2),
        },
    },
    detailSyncBtn: {
        flexShrink: 0,
        width: 106,
        minWidth: 106,
        height: 52,
        borderRadius: 10,
        padding: theme.spacing(0.3, 0.8),
        fontSize: '0.74rem',
        fontWeight: 700,
        textTransform: 'none',
        display: 'flex',
        flexDirection: 'row',
        justifyContent: 'center',
        alignItems: 'center',
        gap: theme.spacing(0.45),
        lineHeight: 1.2,
        boxShadow: '0 8px 20px rgba(25, 118, 210, 0.25)',
        '& .MuiSvgIcon-root': {
            fontSize: '1rem',
        },
        [theme.breakpoints.down('sm')]: {
            width: '100%',
            minWidth: 0,
            height: 44,
            gap: theme.spacing(0.8),
        },
    },
    detailTitle: {
        fontSize: '1.7rem',
        fontWeight: 700,
        lineHeight: 1.2,
        color: theme.palette.text.primary,
        [theme.breakpoints.down('sm')]: {
            fontSize: '1.25rem',
        },
    },
    detailSubMeta: {
        display: 'flex',
        alignItems: 'center',
        gap: theme.spacing(1.2),
        flexWrap: 'wrap',
        color: theme.palette.text.secondary,
    },
    detailDesc: {
        marginTop: theme.spacing(0.2),
        color: theme.palette.text.secondary,
        lineHeight: 1.7,
    },
    detailIdText: {
        color: theme.palette.text.secondary,
        fontSize: '0.78rem',
        textDecoration: 'none',
        '&:hover': {
            color: theme.palette.text.primary,
            textDecoration: 'underline',
        },
    },
    detailSongHeader: {
        display: 'grid',
        gridTemplateColumns:
            '56px minmax(260px, 2fr) minmax(160px, 1.2fr) minmax(160px, 1.2fr) 90px 80px',
        gap: theme.spacing(1),
        alignItems: 'center',
        padding: theme.spacing(1.2, 2),
        borderBottom: `1px solid ${theme.palette.divider}`,
        color: theme.palette.text.secondary,
        fontSize: '0.82rem',
        fontWeight: 600,
        [theme.breakpoints.down('sm')]: {
            gridTemplateColumns: '42px minmax(170px, 2fr) minmax(110px, 1fr) 66px',
            padding: theme.spacing(1, 1.2),
        },
    },
    detailSongRow: {
        display: 'grid',
        gridTemplateColumns:
            '56px minmax(260px, 2fr) minmax(160px, 1.2fr) minmax(160px, 1.2fr) 90px 80px',
        gap: theme.spacing(1),
        alignItems: 'center',
        padding: theme.spacing(1.2, 2),
        borderBottom: `1px solid ${theme.palette.divider}`,
        [theme.breakpoints.down('sm')]: {
            gridTemplateColumns: '42px minmax(170px, 2fr) minmax(110px, 1fr) 66px',
            padding: theme.spacing(1, 1.2),
        },
    },
    detailIdx: {
        textAlign: 'center',
        color: theme.palette.text.secondary,
        fontVariantNumeric: 'tabular-nums',
    },
    detailSongCell: {
        display: 'flex',
        alignItems: 'center',
        minWidth: 0,
        gap: theme.spacing(1.1),
    },
    detailSongCover: {
        width: 46,
        height: 46,
        borderRadius: 8,
        background: theme.palette.action.hover,
        flexShrink: 0,
        [theme.breakpoints.down('sm')]: {
            width: 38,
            height: 38,
        },
    },
    detailSongMain: {
        minWidth: 0,
        display: 'flex',
        flexDirection: 'column',
        gap: theme.spacing(0.45),
    },
    detailSongName: {
        fontWeight: 600,
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
    },
    detailTagRow: {
        display: 'flex',
        alignItems: 'center',
        gap: theme.spacing(0.6),
        minHeight: 20,
        flexWrap: 'wrap',
    },
    detailTag: {
        height: 18,
        borderRadius: 5,
        fontSize: '0.66rem',
        fontWeight: 700,
    },
    detailTextCell: {
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
    },
    detailDurationCell: {
        display: 'block',
        width: '100%',
        justifySelf: 'start',
        textAlign: 'left',
        fontVariantNumeric: 'tabular-nums',
        color: theme.palette.text.secondary,
    },
    detailHeaderLeftCell: {
        display: 'block',
        width: '100%',
        textAlign: 'left',
    },
    detailHeaderCenterCell: {
        display: 'block',
        width: '100%',
        justifySelf: 'center',
        textAlign: 'center',
    },
    detailActionCell: {
        display: 'grid',
        justifySelf: 'center',
        placeItems: 'center',
    },
    detailDownloadBtn: {
        width: 34,
        height: 34,
        borderRadius: 10,
        color: theme.palette.text.secondary,
        border: `1px solid ${theme.palette.divider}`,
        backgroundColor: 'transparent',
        '&:hover': {
            backgroundColor: theme.palette.action.hover,
        },
    },
    mobileHidden: {
        [theme.breakpoints.down('sm')]: {
            display: 'none',
        },
    },
}))

const normalizePlaylistTagGroups = (raw) => {
    const fallback = []
    if (!raw || !Array.isArray(raw.tags)) return fallback
    return raw.tags || []
}

const normalizePlaylistSortOptions = (raw, source) => {
    const PLAYLIST_SORT_OPTIONS_BY_SOURCE = {
        wy: [
            { key: 'hot', label: '最热' },
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
    const getPlaylistSortOptions = (src) =>
        PLAYLIST_SORT_OPTIONS_BY_SOURCE[src] || PLAYLIST_SORT_OPTIONS_BY_SOURCE.wy

    const fallback = getPlaylistSortOptions(source)
    const list = Array.isArray(raw?.sortList) ? raw.sortList : []
    if (!list.length) return fallback
    const mapped = list
        .map((item) => ({
            key: String(item?.id ?? '').trim(),
            label: String(item?.name ?? '').trim(),
        }))
        .filter((item) => item.key && item.label)
    return mapped.length ? mapped : fallback
}

const normalizePlaylistItem = (item, fallbackSource) => {
    const playCountRaw = item?.play_count
    let playCountText = ''
    if (typeof playCountRaw === 'number' && Number.isFinite(playCountRaw)) {
        playCountText = formatCompactCount(playCountRaw)
    } else if (typeof playCountRaw === 'string' && playCountRaw.trim()) {
        playCountText = playCountRaw.trim()
    } else {
        playCountText = '0'
    }

    return {
        id: String(item?.id || `${item?.name || 'playlist'}-${Math.random()}`),
        name: item?.name || '未命名歌单',
        author: item?.author || '--',
        date: item?.time || '--',
        songCount: Number(item?.total) || 0,
        playCountText,
        source: item?.source || fallbackSource,
        cover: item?.img || '',
        desc: item?.desc || item?.description || item?.intro || '',
    }
}

const normalizeDetailSongItem = (item, fallbackSource) => {
    const source = item?.source || fallbackSource
    const normalized = {
        ...(item || {}),
        id: String(item?.id || `${item?.name || 'song'}-${Math.random()}`),
        name: item?.name || '未知标题',
        singer: item?.singer || item?.artist || '--',
        albumName: item?.albumName || item?.album || '--',
        duration: item?.duration,
        interval: item?.interval,
        img: item?.img || '',
        source,
        meta: item?.meta || {},
    }

    // Keep quality/type aliases aligned with song search payload so
    // Online_search can reuse the same quality picker and download parser.
    if (!normalized.types && item?._types) normalized.types = item._types
    if (!normalized._types && item?.types) normalized._types = item.types
    if (!normalized.qualitys && item?._qualitys) normalized.qualitys = item._qualitys
    if (!normalized._qualitys && item?.qualitys) normalized._qualitys = item.qualitys

    return normalized
}

const normalizeDetailInfo = (info, fallbackPlaylist) => {
    const playCountRaw = info?.play_count
    let playCountText = fallbackPlaylist?.playCountText || '0'
    if (typeof playCountRaw === 'number' && Number.isFinite(playCountRaw)) {
        playCountText = formatCompactCount(playCountRaw)
    } else if (typeof playCountRaw === 'string' && playCountRaw.trim()) {
        playCountText = playCountRaw.trim()
    }

    return {
        name: info?.name || fallbackPlaylist?.name || '未命名歌单',
        author: info?.author || fallbackPlaylist?.author || '--',
        desc: info?.desc || fallbackPlaylist?.desc || '',
        cover: info?.img || fallbackPlaylist?.cover || '',
        playCountText,
    }
}

const getPlaylistExternalUrl = (playlistId, source) => {
    const id = String(playlistId || '').trim()
    if (!id) return ''

    switch (source) {
        case 'wy':
            return `https://music.163.com/#/playlist?id=${encodeURIComponent(id)}`
        case 'tx':
            return `https://y.qq.com/n/ryqq/playlist/${encodeURIComponent(id)}`
        case 'kg': {
            const cleanId = id.replace(/^id_/, '')
            return `https://www.kugou.com/yy/special/single/${encodeURIComponent(cleanId)}.html`
        }
        case 'kw': {
            const cleanId = id.includes('__') ? id.split('__')[1] : id
            return `https://www.kuwo.cn/playlist_detail/${encodeURIComponent(cleanId)}`
        }
        case 'mg':
            return `https://music.migu.cn/v3/music/playlist/${encodeURIComponent(id)}`
        default:
            return ''
    }
}

const OnlinePlaylistSearch = ({ active = true, onOpenDownloadDialog, onCreatePlaylistSyncTask }) => {
    const classes = useStyles()

    const [source, setSource] = useState('wy')
    const [playlistQuery, setPlaylistQuery] = useState('')
    const [playlistAppliedQuery, setPlaylistAppliedQuery] = useState('')
    const [playlistSort, setPlaylistSort] = useState('hot')
    const [playlistTagGroups, setPlaylistTagGroups] = useState([])
    const [playlistSelectedTags, setPlaylistSelectedTags] = useState({})
    const [playlistSortOptions, setPlaylistSortOptions] = useState([
        { key: 'hot', label: '最热' },
    ])
    const [playlistRecommendRaw, setPlaylistRecommendRaw] = useState([])
    const [playlistLoading, setPlaylistLoading] = useState(false)
    const [playlistError, setPlaylistError] = useState('')
    const [playlistPage, setPlaylistPage] = useState(1)
    const [playlistTotal, setPlaylistTotal] = useState(0)
    const [playlistJumpPageInput, setPlaylistJumpPageInput] = useState('1')
    const [playlistMetaSource, setPlaylistMetaSource] = useState('')
    const [playlistLoadedKey, setPlaylistLoadedKey] = useState('')
    const [detailPlaylist, setDetailPlaylist] = useState(null)
    const [detailInfo, setDetailInfo] = useState(null)
    const [detailSongs, setDetailSongs] = useState([])
    const [detailLoading, setDetailLoading] = useState(false)
    const [detailLoadingMore, setDetailLoadingMore] = useState(false)
    const [detailError, setDetailError] = useState('')
    const [detailSongCache, setDetailSongCache] = useState({})
    const [detailPage, setDetailPage] = useState(1)
    const [detailTotal, setDetailTotal] = useState(0)
    const [detailLoadedKey, setDetailLoadedKey] = useState('')
    const [detailScrollContainer, setDetailScrollContainer] = useState(null)
    const [syncButtonLoading, setSyncButtonLoading] = useState(false)
    const [syncQualityDialogOpen, setSyncQualityDialogOpen] = useState(false)
    const [availableQualities, setAvailableQualities] = useState([])
    const [selectedSyncQuality, setSelectedSyncQuality] = useState('')
    const [pendingSyncData, setPendingSyncData] = useState(null)

    const badge = SOURCE_BADGE[source] || {}
    const playlistItems = playlistRecommendRaw
    const playlistTotalPages = Math.max(
        1,
        Math.ceil((Number(playlistTotal) || 0) / 30),
    )
    const detailPlaylistUrl = getPlaylistExternalUrl(
        detailPlaylist?.id,
        detailPlaylist?.source || source,
    )

    const getActivePlaylistCategoryLabel = () => {
        const selectedCount = Object.values(playlistSelectedTags).filter(
            (v) => v && String(v).trim(),
        ).length
        if (selectedCount === 0) return '全部'
        if (selectedCount === 1) {
            for (const group of playlistTagGroups) {
                for (const tag of group.list || []) {
                    for (const [groupName, selectedId] of Object.entries(
                        playlistSelectedTags,
                    )) {
                        if (groupName === group.name && selectedId === tag.id) {
                            return tag.name
                        }
                    }
                }
            }
        }
        return `${selectedCount}个`
    }

    // Load playlist metadata (tags & sort options)
    useEffect(() => {
        if (!active) return
        if (playlistMetaSource === source && playlistTagGroups.length > 0) return
        let cancelled = false

        const loadPlaylistMeta = async () => {
            try {
                const { json } = await httpClient(
                    `/api/online/playlist/tags?source=${encodeURIComponent(source)}`,
                )
                if (cancelled) return
                const tagGroups = normalizePlaylistTagGroups(json)
                const nextSortOptions = normalizePlaylistSortOptions(json, source)
                setPlaylistTagGroups(tagGroups)
                setPlaylistSortOptions(nextSortOptions)
                setPlaylistMetaSource(source)
                setPlaylistSelectedTags({})
                setPlaylistPage(1)
                setPlaylistJumpPageInput('1')
                setPlaylistLoadedKey('')
                setPlaylistSort((current) =>
                    nextSortOptions.some((item) => item.key === current)
                        ? current
                        : nextSortOptions[0]?.key || 'hot',
                )
            } catch (e) {
                if (cancelled) return
                setPlaylistTagGroups([])
                setPlaylistSelectedTags({})
                setPlaylistSortOptions([
                    { key: 'hot', label: '最热' },
                ])
                setPlaylistMetaSource('')
                setPlaylistSort('hot')
                setPlaylistTotal(0)
                setPlaylistError('歌单分类加载失败，请稍后重试')
            }
        }

        loadPlaylistMeta()
        return () => {
            cancelled = true
        }
    }, [active, source, playlistMetaSource, playlistTagGroups.length])

    // Load playlist list
    useEffect(() => {
        if (!active) return
        if (playlistMetaSource !== source) return
        if (!playlistSortOptions.some((item) => item.key === playlistSort)) return

        let cancelled = false

        const keyword = playlistAppliedQuery.trim()
        const selectedTagIds = Object.values(playlistSelectedTags)
            .filter((tid) => tid && String(tid).trim())
            .map((tid) => String(tid).trim())
        const activeTagId = selectedTagIds[0] || ''

        const requestKey = JSON.stringify({
            source,
            sort: playlistSort,
            keyword,
            tagId: activeTagId,
            page: playlistPage,
        })
        if (playlistLoadedKey === requestKey) return

        setPlaylistLoading(true)
        setPlaylistError('')

        const listEndpoint = keyword
            ? `/api/online/playlist/search?source=${encodeURIComponent(source)}&keyword=${encodeURIComponent(keyword)}&page=${encodeURIComponent(playlistPage)}`
            : `/api/online/playlist/list?source=${encodeURIComponent(source)}&sortId=${encodeURIComponent(playlistSort)}&tagId=${encodeURIComponent(activeTagId)}&page=${encodeURIComponent(playlistPage)}`

        httpClient(listEndpoint)
            .then(({ json }) => {
                if (cancelled) return
                const list = Array.isArray(json?.list) ? json.list : []
                const totalCount = Number(json?.total)
                const currentPage = Number(json?.page)
                setPlaylistRecommendRaw(list.map((item) => normalizePlaylistItem(item, source)))
                setPlaylistTotal(
                    Number.isFinite(totalCount) && totalCount >= 0 ? totalCount : list.length,
                )
                if (currentPage > 0) {
                    setPlaylistPage(currentPage)
                    setPlaylistJumpPageInput(String(currentPage))
                }
                setPlaylistLoadedKey(requestKey)
                if (json?.error) setPlaylistError(String(json.error))
            })
            .catch(() => {
                if (cancelled) return
                setPlaylistError('歌单推荐加载失败，请稍后重试')
                setPlaylistRecommendRaw([])
                setPlaylistTotal(0)
            })
            .finally(() => {
                if (!cancelled) setPlaylistLoading(false)
            })

        return () => {
            cancelled = true
        }
    }, [
        active,
        playlistMetaSource,
        playlistSelectedTags,
        playlistSort,
        playlistSortOptions,
        playlistAppliedQuery,
        playlistPage,
        playlistLoadedKey,
        source,
    ])

    const handleSearch = useCallback(() => {
        setPlaylistPage(1)
        setPlaylistJumpPageInput('1')
        setPlaylistLoadedKey('')
        setPlaylistAppliedQuery(playlistQuery.trim())
    }, [playlistQuery])

    const handlePrevPage = useCallback(() => {
        if (playlistLoading || playlistPage <= 1) return
        setPlaylistPage((current) => current - 1)
    }, [playlistLoading, playlistPage])

    const handleNextPage = useCallback(() => {
        if (playlistLoading || playlistPage >= playlistTotalPages) return
        setPlaylistPage((current) => current + 1)
    }, [playlistLoading, playlistPage, playlistTotalPages])

    const handleJumpPage = useCallback(() => {
        const target = Math.min(
            playlistTotalPages,
            Math.max(1, Number(playlistJumpPageInput) || 1),
        )
        setPlaylistPage(target)
        setPlaylistJumpPageInput(String(target))
    }, [playlistJumpPageInput, playlistTotalPages])

    const handleLoadDetailSongs = useCallback(
        (item, pageNum = 1, isLoadMore = false) => {
            if (!item) return
            const currentSource = item.source || source
            const currentPlaylistId = String(item.id || '').trim()

            if (!currentPlaylistId) {
                setDetailError('歌单缺少可用的 ID，无法加载详情')
                return
            }

            if (!isLoadMore) {
                setDetailLoading(true)
                setDetailSongs([])
                setDetailInfo(normalizeDetailInfo(null, item))
                setDetailPage(1)
                setDetailTotal(0)
                setDetailLoadedKey('')
            } else {
                setDetailLoadingMore(true)
            }
            setDetailError('')

            httpClient(
                `/api/online/playlist/detail?source=${encodeURIComponent(currentSource)}&id=${encodeURIComponent(currentPlaylistId)}&limit=30&page=${encodeURIComponent(pageNum)}`,
            )
                .then(({ json }) => {
                    const list = Array.isArray(json?.list) ? json.list : []
                    const totalCount = Number(json?.total) || Number(item.songCount) || 0
                    const normalized = list.map((song) =>
                        normalizeDetailSongItem(song, currentSource),
                    )
                    if (json?.info) {
                        setDetailInfo(normalizeDetailInfo(json.info, item))
                    }

                    if (isLoadMore) {
                        setDetailSongs((prev) => [...prev, ...normalized])
                    } else {
                        setDetailSongs(normalized)
                    }
                    setDetailTotal(totalCount)
                    setDetailPage(pageNum)
                    if (json?.error) setDetailError(String(json.error))
                })
                .catch(() => {
                    if (!isLoadMore) {
                        setDetailSongs([])
                    }
                    setDetailError('歌单详情加载失败，请稍后重试')
                })
                .finally(() => {
                    if (isLoadMore) {
                        setDetailLoadingMore(false)
                    } else {
                        setDetailLoading(false)
                    }
                })
        },
        [source],
    )

    const handleOpenPlaylistDetail = useCallback(
        (item) => {
            if (!item) return
            setDetailPlaylist(item)
            setDetailInfo(normalizeDetailInfo(null, item))
            handleLoadDetailSongs(item, 1, false)
        },
        [handleLoadDetailSongs],
    )

    const handleLoadMoreDetailSongs = useCallback(() => {
        if (!detailPlaylist || detailLoadingMore || detailLoading) return
        // 检查是否已经加载了所有歌曲
        if (detailTotal > 0 && detailSongs.length >= detailTotal) return
        const nextPage = detailPage + 1
        const totalPages = Math.ceil(detailTotal / 30)
        if (nextPage > totalPages) return

        handleLoadDetailSongs(detailPlaylist, nextPage, true)
    }, [detailPlaylist, detailPage, detailTotal, detailLoadingMore, detailLoading, detailSongs.length, handleLoadDetailSongs])

    useEffect(() => {
        if (!detailScrollContainer) return

        const handleScroll = () => {
            const { scrollTop, scrollHeight, clientHeight } = detailScrollContainer
            // 当滚动距离底部 < 500px 时触发加载
            if (scrollHeight - scrollTop - clientHeight < 500) {
                handleLoadMoreDetailSongs()
            }
        }

        const container = detailScrollContainer
        container.addEventListener('scroll', handleScroll)
        return () => {
            container.removeEventListener('scroll', handleScroll)
        }
    }, [detailScrollContainer, handleLoadMoreDetailSongs])

    const handleStartPlaylistSync = useCallback(async () => {
        if (!detailPlaylist || syncButtonLoading) return

        setSyncButtonLoading(true)
        try {
            // Analyze all available qualities from songs.
            // Reuse getQualityKeys to stay consistent with song list rendering.
            const qualitiesSet = new Set()
            detailSongs.forEach((song) => {
                getQualityKeys(song).forEach((q) => {
                    if (q) qualitiesSet.add(String(q))
                })
            })

            const qualityOrder = ['master', 'flac24bit', 'flac', 'ape', '320k', '128k']
            const fallbackQualities = ['320k', '128k']
            const rawQualities = Array.from(qualitiesSet)
            const qualities = rawQualities.length > 0 ? rawQualities : fallbackQualities

            // Known quality first, unknown quality goes to the tail and stays stable.
            const sortedQualities = [...qualities].sort((a, b) => {
                const ai = qualityOrder.indexOf(a)
                const bi = qualityOrder.indexOf(b)
                if (ai === -1 && bi === -1) return String(a).localeCompare(String(b))
                if (ai === -1) return 1
                if (bi === -1) return -1
                return ai - bi
            })

            setAvailableQualities(sortedQualities)
            setSelectedSyncQuality(sortedQualities[0]) // Default to best quality

            // Store sync data for later use
            const playlistName = detailInfo?.name || detailPlaylist.name || '未命名歌单'
            const playlistDesc = detailInfo?.desc || detailPlaylist.desc || ''
            const playlistCover = detailInfo?.cover || detailPlaylist.cover || ''
            const currentSource = detailPlaylist.source || source
            const currentPlaylistId = detailPlaylist.id

            setPendingSyncData({
                playlistName,
                playlistDesc,
                playlistCover,
                currentSource,
                currentPlaylistId,
            })

            // Open quality selection dialog
            setSyncQualityDialogOpen(true)
            setSyncButtonLoading(false)
        } catch (error) {
            console.error('准备歌单同步失败:', error)
            setDetailError('无法解析歌单音质，请稍后重试')
            setSyncButtonLoading(false)
        }
    }, [detailPlaylist, detailInfo, source, detailSongs, syncButtonLoading])

    const handleConfirmSyncQuality = useCallback(async () => {
        if (!pendingSyncData || !selectedSyncQuality) return

        setSyncQualityDialogOpen(false)
        setSyncButtonLoading(true)

        try {
            console.log('[playlist-sync] confirm sync quality', {
                pendingSyncData,
                selectedSyncQuality,
                detailSongsCount: detailSongs.length,
                detailTotal,
                selectedQualityMeta: QUALITY_META[selectedSyncQuality] || null,
            })

            // Create sync task immediately so it appears in download list first.
            const syncTask = {
                id: `sync-${Date.now()}-${Math.random()}`,
                taskType: 'playlist_sync',
                title: pendingSyncData.playlistName,
                cover: pendingSyncData.playlistCover,
                source: pendingSyncData.currentSource,
                sourceName: pendingSyncData.currentSource,
                status: 'syncing',
                progress: 0,
                remainingCount: detailTotal || detailSongs.length,
                currentSongTitle: '创建歌单中',
                playlistId: pendingSyncData.currentPlaylistId,
                navidromPlaylistId: '',
                sourceType: pendingSyncData.currentSource,
                selectedQuality: selectedSyncQuality,
                playlistComment: pendingSyncData.playlistDesc || '',
            }

            if (onCreatePlaylistSyncTask) {
                console.log('[playlist-sync] emit create task', {
                    syncTask,
                    detailSongsCount: detailSongs.length,
                })
                onCreatePlaylistSyncTask(syncTask, detailSongs)
                console.log('[playlist-sync] create task callback returned', {
                    taskId: syncTask.id,
                })
            } else {
                console.warn('[playlist-sync] onCreatePlaylistSyncTask missing', {
                    syncTask,
                })
            }

            setSyncButtonLoading(false)
            setPendingSyncData(null)
        } catch (error) {
            console.error('[playlist-sync] confirm sync failed', {
                error,
                message: error?.message,
                stack: error?.stack,
                pendingSyncData,
                selectedSyncQuality,
            })
            setSyncButtonLoading(false)
        }
    }, [pendingSyncData, selectedSyncQuality, detailTotal, detailSongs, onCreatePlaylistSyncTask])

    const handleCloseSyncQualityDialog = useCallback(() => {
        setSyncQualityDialogOpen(false)
        setPendingSyncData(null)
    }, [])

    const handleBackFromDetail = useCallback(() => {
        setDetailPlaylist(null)
        setDetailInfo(null)
        setDetailError('')
        setDetailSongs([])
        setDetailPage(1)
        setDetailTotal(0)
        setDetailLoadedKey('')
        setDetailScrollContainer(null)
    }, [])

    return (
        <div className={classes.root}>
            {detailPlaylist ? (
                <>
                    <div className={classes.detailBackRow}>
                        <Button
                            variant="outlined"
                            size="small"
                            startIcon={<ArrowBackIcon />}
                            onClick={handleBackFromDetail}
                            className={classes.detailBackBtn}
                        >
                            后退
                        </Button>
                    </div>

                    <Card className={classes.detailCard} variant="outlined">
                        <CardContent>
                            <div className={classes.detailHeader}>
                                <Avatar
                                    variant="rounded"
                                    src={(detailInfo?.cover || detailPlaylist.cover) || undefined}
                                    className={classes.detailCover}
                                />
                                <div className={classes.detailMetaRow}>
                                    <div className={classes.detailMeta}>
                                        <Typography className={classes.detailTitle}>
                                            {detailInfo?.name || detailPlaylist.name || '未命名歌单'}
                                        </Typography>
                                        <div className={classes.detailSubMeta}>
                                            <Typography variant="subtitle2" color="textSecondary">
                                                {detailInfo?.author || detailPlaylist.author || '--'}
                                            </Typography>
                                            <Typography variant="body2" color="textSecondary">
                                                {`${detailTotal || detailPlaylist.songCount || 0} 首歌曲`}
                                            </Typography>
                                            <Typography variant="body2" color="textSecondary">
                                                {`${detailInfo?.playCountText || detailPlaylist.playCountText || '0'} 次收听`}
                                            </Typography>
                                        </div>
                                        <Typography
                                            className={classes.detailIdText}
                                            variant="caption"
                                            component={detailPlaylistUrl ? 'a' : 'span'}
                                            href={detailPlaylistUrl || undefined}
                                            target={detailPlaylistUrl ? '_blank' : undefined}
                                            rel={detailPlaylistUrl ? 'noreferrer noopener' : undefined}
                                            title={detailPlaylistUrl ? '打开官方歌单页面' : undefined}
                                        >
                                            {`歌单ID：${detailPlaylist.id || '--'} (${(SOURCE_BADGE[detailPlaylist.source || source] || {}).name || (detailPlaylist.source || source || '').toUpperCase()})`}
                                        </Typography>
                                        <Typography className={classes.detailDesc} variant="body2">
                                            {detailInfo?.desc || detailPlaylist.desc || '该歌单暂无简介'}
                                        </Typography>
                                    </div>
                                    <Button
                                        variant="contained"
                                        color="primary"
                                        className={classes.detailSyncBtn}
                                        onClick={handleStartPlaylistSync}
                                        disabled={syncButtonLoading}
                                    >
                                        <SyncIcon />
                                        同步到Navidrome
                                    </Button>
                                </div>
                            </div>
                        </CardContent>
                    </Card>

                    <Card className={classes.resultCard} variant="outlined">
                        <CardContent>
                            <div className={classes.resultStatus}>
                                <Typography variant="subtitle2">
                                    {`${detailInfo?.name || detailPlaylist.name || '歌单'} · ${detailSongs.length}/${detailTotal || detailPlaylist.songCount || 0} 首`}
                                </Typography>
                                <Chip
                                    size="small"
                                    label={(SOURCE_BADGE[detailPlaylist.source || source] || {}).name}
                                    style={{
                                        backgroundColor: (SOURCE_BADGE[detailPlaylist.source || source] || {}).bg,
                                        color: (SOURCE_BADGE[detailPlaylist.source || source] || {}).color,
                                        fontWeight: 600,
                                        fontSize: '0.7rem',
                                        height: 20,
                                    }}
                                />
                            </div>

                            <div
                                className={classes.detailSongListContainer}
                                ref={setDetailScrollContainer}
                            >
                                <div className={classes.detailSongHeader}>
                                    <span className={classes.detailIdx}>#</span>
                                    <span>歌曲</span>
                                    <span className={classes.mobileHidden}>歌手</span>
                                    <span className={classes.mobileHidden}>专辑</span>
                                    <span className={`${classes.detailHeaderLeftCell} ${classes.mobileHidden}`}>时长</span>
                                    <span className={classes.detailHeaderCenterCell}>操作</span>
                                </div>

                                {detailLoading ? (
                                    <div className={classes.loadingBox}>
                                        <CircularProgress size={30} />
                                    </div>
                                ) : detailSongs.length === 0 ? (
                                    <div className={classes.emptyBox}>
                                        <Typography variant="body2">
                                            {detailError || '暂无歌单歌曲'}
                                        </Typography>
                                    </div>
                                ) : (
                                    <div>
                                        {detailSongs.map((song, idx) => {
                                            const sourceInfo = getSourceBadge(song.source || source)
                                            const qualityKeys = getQualityKeys(song)
                                            return (
                                                <div
                                                    key={`${song.id || song.name || 'detail-song'}-${idx}`}
                                                    className={classes.detailSongRow}
                                                >
                                                    <span className={classes.detailIdx}>{idx + 1}</span>
                                                    <div className={classes.detailSongCell}>
                                                        <Avatar
                                                            variant="rounded"
                                                            src={song.img || undefined}
                                                            className={classes.detailSongCover}
                                                        />
                                                        <div className={classes.detailSongMain}>
                                                            <Typography
                                                                className={classes.detailSongName}
                                                                title={song.name || ''}
                                                            >
                                                                {song.name || '未知标题'}
                                                            </Typography>
                                                            <div className={classes.detailTagRow}>
                                                                <Chip
                                                                    size="small"
                                                                    label={sourceInfo.name}
                                                                    className={classes.detailTag}
                                                                    style={{
                                                                        backgroundColor: sourceInfo.bg,
                                                                        color: sourceInfo.color,
                                                                    }}
                                                                />
                                                                {qualityKeys.map((qualityKey) => {
                                                                    const qualityMeta = QUALITY_META[qualityKey] || {
                                                                        label: qualityKey,
                                                                        bg: '#ececec',
                                                                        color: '#555',
                                                                    }
                                                                    return (
                                                                        <Chip
                                                                            size="small"
                                                                            key={`${song.id || song.name || idx}-${qualityKey}`}
                                                                            label={qualityMeta.label}
                                                                            className={classes.detailTag}
                                                                            style={{
                                                                                backgroundColor: qualityMeta.bg,
                                                                                color: qualityMeta.color,
                                                                            }}
                                                                        />
                                                                    )
                                                                })}
                                                            </div>
                                                        </div>
                                                    </div>
                                                    <Typography
                                                        className={`${classes.detailTextCell} ${classes.mobileHidden}`}
                                                        title={song.singer || ''}
                                                    >
                                                        {song.singer || '--'}
                                                    </Typography>
                                                    <Typography
                                                        className={`${classes.detailTextCell} ${classes.mobileHidden}`}
                                                        title={song.albumName || ''}
                                                    >
                                                        {song.albumName || '--'}
                                                    </Typography>
                                                    <Typography
                                                        className={`${classes.detailDurationCell} ${classes.mobileHidden}`}
                                                    >
                                                        {formatDuration(song.duration || song.interval)}
                                                    </Typography>
                                                    <div className={classes.detailActionCell}>
                                                        <IconButton
                                                            size="small"
                                                            className={classes.detailDownloadBtn}
                                                            onClick={() =>
                                                                onOpenDownloadDialog && onOpenDownloadDialog(song)
                                                            }
                                                            aria-label="下载"
                                                            title="下载"
                                                        >
                                                            <GetAppIcon fontSize="small" />
                                                        </IconButton>
                                                    </div>
                                                </div>
                                            )
                                        })}
                                        {detailLoadingMore && (
                                            <div
                                                style={{
                                                    display: 'flex',
                                                    justifyContent: 'center',
                                                    padding: '16px',
                                                }}
                                            >
                                                <CircularProgress size={20} />
                                            </div>
                                        )}
                                    </div>
                                )}
                            </div>
                        </CardContent>
                    </Card>
                </>
            ) : (
                <>
                    {/* ── Row 1: Search and Source ── */}
                    <div className={classes.searchRow}>
                        <TextField
                            className={classes.searchInput}
                            variant="outlined"
                            size="small"
                            placeholder="搜索歌单..."
                            value={playlistQuery}
                            onChange={(e) => setPlaylistQuery(e.target.value)}
                            onKeyDown={(e) => {
                                if (e.key === 'Enter') handleSearch()
                            }}
                            InputProps={{
                                startAdornment: (
                                    <InputAdornment position="start">
                                        <SearchIcon color="action" fontSize="small" />
                                    </InputAdornment>
                                ),
                            }}
                        />
                        <Button
                            variant="contained"
                            color="primary"
                            className={classes.searchBtn}
                            onClick={handleSearch}
                        >
                            搜索
                        </Button>
                        <Select
                            className={`${classes.sortSelect} ${classes.selectControl}`}
                            variant="outlined"
                            value={playlistSort}
                            onChange={(e) => {
                                setPlaylistPage(1)
                                setPlaylistJumpPageInput('1')
                                setPlaylistLoadedKey('')
                                setPlaylistSort(e.target.value)
                            }}
                        >
                            {playlistSortOptions.map((opt) => (
                                <MenuItem key={opt.key} value={opt.key}>
                                    {opt.label}
                                </MenuItem>
                            ))}
                        </Select>
                        <Select
                            className={`${classes.sourceSelect} ${classes.selectControl}`}
                            variant="outlined"
                            value={source}
                            onChange={(e) => setSource(e.target.value)}
                        >
                            {SOURCES.map((s) => (
                                <MenuItem key={s.key} value={s.key}>
                                    {s.label}
                                </MenuItem>
                            ))}
                        </Select>
                    </div>

                    {/* Tag selectors */}
                    {playlistTagGroups.length > 0 && (
                        <div className={classes.playlistTagsRow}>
                            {playlistTagGroups.map((group) => (
                                <div key={group.name} className={classes.playlistTagGroup}>
                                    <Typography className={classes.playlistTagGroupLabel}>
                                        {group.name}
                                    </Typography>
                                    {(group.list || []).map((tag) => (
                                        <Button
                                            key={tag.id}
                                            size="small"
                                            className={`${classes.playlistTagButton} ${playlistSelectedTags[group.name] === tag.id ? 'selected' : ''
                                                }`}
                                            onClick={() => {
                                                setPlaylistPage(1)
                                                setPlaylistJumpPageInput('1')
                                                setPlaylistLoadedKey('')
                                                setPlaylistSelectedTags((prev) => ({
                                                    ...prev,
                                                    [group.name]:
                                                        prev[group.name] === tag.id ? '' : tag.id,
                                                }))
                                            }}
                                        >
                                            {tag.name}
                                        </Button>
                                    ))}
                                </div>
                            ))}
                        </div>
                    )}

                    {/* Playlist list */}
                    <Card className={classes.resultCard} variant="outlined">
                        <CardContent>
                            <div className={classes.resultStatus}>
                                <Typography variant="subtitle2">
                                    {playlistAppliedQuery
                                        ? `${playlistAppliedQuery} · ${playlistTotal || playlistItems.length} 个歌单`
                                        : `${getActivePlaylistCategoryLabel()}`}
                                </Typography>
                                <Chip
                                    size="small"
                                    label={badge.name}
                                    style={{
                                        backgroundColor: badge.bg,
                                        color: badge.color,
                                        fontWeight: 600,
                                        fontSize: '0.7rem',
                                        height: 20,
                                    }}
                                />
                            </div>

                            {playlistLoading ? (
                                <div className={classes.loadingBox}>
                                    <CircularProgress size={30} />
                                </div>
                            ) : playlistItems.length === 0 ? (
                                <div className={classes.emptyBox}>
                                    <Typography variant="body2">
                                        {playlistError || '暂无推荐歌单'}
                                    </Typography>
                                </div>
                            ) : (
                                <div className={classes.playlistGrid}>
                                    {playlistItems.map((item) => (
                                        <Card
                                            key={item.id}
                                            className={classes.playlistGridCard}
                                            elevation={0}
                                            onClick={() => handleOpenPlaylistDetail(item)}
                                        >
                                            <div
                                                className={classes.playlistCover}
                                                style={
                                                    item.cover
                                                        ? /^linear-gradient/i.test(String(item.cover))
                                                            ? { background: item.cover }
                                                            : {
                                                                backgroundImage: `url(${item.cover})`,
                                                                backgroundSize: 'cover',
                                                                backgroundPosition: 'center',
                                                                backgroundRepeat: 'no-repeat',
                                                            }
                                                        : { background: '#d9d9d9' }
                                                }
                                            >
                                                <div className={classes.playlistCoverOverlay}>
                                                    <div className={classes.playlistCoverStats}>
                                                        <span>{item.date}</span>
                                                    </div>
                                                </div>
                                            </div>
                                            <div className={classes.playlistCardBody}>
                                                <Typography
                                                    className={classes.playlistCardTitle}
                                                    title={item.name}
                                                >
                                                    {item.name}
                                                </Typography>
                                                <div className={classes.playlistMetaRow}>
                                                    <span title={item.author}>{item.author}</span>
                                                    <span>{item.date}</span>
                                                </div>
                                                <div className={classes.playlistMetricRow}>
                                                    <span>{`${item.songCount} 首`}</span>
                                                    <span>{`${item.playCountText || formatCompactCount(item.playCount)} 次收听`}</span>
                                                </div>
                                            </div>
                                        </Card>
                                    ))}
                                </div>
                            )}

                            {playlistError && playlistItems.length > 0 && (
                                <Typography variant="caption" color="error">
                                    {playlistError}
                                </Typography>
                            )}

                            <div className={classes.paginationRow}>
                                <Typography className={classes.paginationInfo}>
                                    {`共 ${playlistTotal} 条 · 第 ${playlistPage} / ${playlistTotalPages} 页`}
                                </Typography>
                                <div className={classes.paginationControls}>
                                    <Button
                                        size="small"
                                        variant="outlined"
                                        onClick={handlePrevPage}
                                        disabled={playlistLoading || playlistPage <= 1}
                                    >
                                        上一页
                                    </Button>
                                    <Button
                                        size="small"
                                        variant="outlined"
                                        onClick={handleNextPage}
                                        disabled={playlistLoading || playlistPage >= playlistTotalPages}
                                    >
                                        下一页
                                    </Button>
                                    <TextField
                                        value={playlistJumpPageInput}
                                        onChange={(e) =>
                                            setPlaylistJumpPageInput(e.target.value.replace(/[^0-9]/g, ''))
                                        }
                                        variant="outlined"
                                        size="small"
                                        className={classes.jumpInput}
                                        placeholder="页码"
                                        onKeyDown={(e) => {
                                            if (e.key === 'Enter') handleJumpPage()
                                        }}
                                    />
                                    <Button
                                        size="small"
                                        variant="contained"
                                        color="primary"
                                        onClick={handleJumpPage}
                                        disabled={playlistLoading}
                                    >
                                        跳转
                                    </Button>
                                </div>
                            </div>

                            <div className={classes.refreshRow}>
                                <Button
                                    className={classes.refreshBtn}
                                    startIcon={<RefreshIcon />}
                                    onClick={handleSearch}
                                    size="small"
                                >
                                    刷新歌单
                                </Button>
                            </div>
                        </CardContent>
                    </Card>
                </>
            )}

            {/* Quality Selection Dialog */}
            <Dialog
                open={syncQualityDialogOpen}
                onClose={handleCloseSyncQualityDialog}
                maxWidth="sm"
                fullWidth
            >
                <DialogTitle>选择同步音质</DialogTitle>
                <DialogContent style={{ paddingTop: 16 }}>
                    <Typography variant="body2" color="textSecondary" style={{ marginBottom: 16 }}>
                        该歌单支持以下音质，建议优先选择较高音质
                    </Typography>

                    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                        {availableQualities.map((quality) => {
                            const qualityMeta = QUALITY_META[quality] || {}
                            const isSelected = selectedSyncQuality === quality
                            const chipBg = qualityMeta.bg || '#dce8ff'
                            const chipFg = qualityMeta.color || '#2c4ca3'
                            const selectedBg = darkenHexColor(chipBg, 8)
                            const selectedFg = darkenHexColor(chipFg, 8)
                            const selectedChipBg = darkenHexColor(chipBg, 58)
                            const selectedChipFg = darkenHexColor(chipFg, 54)

                            return (
                                <Button
                                    key={quality}
                                    variant={isSelected ? 'contained' : 'outlined'}
                                    color={isSelected ? 'primary' : 'default'}
                                    onClick={() => setSelectedSyncQuality(quality)}
                                    style={{
                                        justifyContent: 'flex-start',
                                        padding: '12px 16px',
                                        backgroundColor: isSelected ? selectedBg : 'transparent',
                                        color: isSelected ? selectedFg : '#5f5f5f',
                                        borderColor: isSelected ? selectedFg : '#9a9a9a',
                                    }}
                                >
                                    <Chip
                                        label={qualityMeta.label || quality}
                                        size="small"
                                        style={{
                                            backgroundColor: isSelected ? selectedChipBg : chipBg,
                                            color: isSelected ? selectedChipFg : chipFg,
                                            border: 'none',
                                            marginRight: 8,
                                        }}
                                    />
                                    <span>{qualityMeta.label || quality}</span>
                                </Button>
                            )
                        })}
                    </div>

                    <Typography
                        variant="caption"
                        color="textSecondary"
                        style={{ display: 'block', marginTop: 16 }}
                    >
                        如果选定音质下载失败，系统将自动降级至其他可用音质
                    </Typography>

                    <div style={{ display: 'flex', gap: 8, marginTop: 24, justifyContent: 'flex-end' }}>
                        <Button
                            variant="outlined"
                            onClick={handleCloseSyncQualityDialog}
                            disabled={syncButtonLoading}
                        >
                            取消
                        </Button>
                        <Button
                            variant="contained"
                            color="primary"
                            onClick={handleConfirmSyncQuality}
                            disabled={syncButtonLoading || !selectedSyncQuality}
                        >
                            {syncButtonLoading ? <CircularProgress size={20} /> : '确认同步'}
                        </Button>
                    </div>
                </DialogContent>
            </Dialog>
        </div>
    )
}

export { OnlinePlaylistSearch }
export default OnlinePlaylistSearch
