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
    Select,
    MenuItem,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import RefreshIcon from '@material-ui/icons/Refresh'
import SearchIcon from '@material-ui/icons/Search'
import { InputAdornment } from '@material-ui/core'
import { httpClient } from '../dataProvider'
import { SOURCE_BADGE, formatCompactCount } from './Online_constants'

const SOURCES = [
    { key: 'wy', label: '网易云' },
    { key: 'tx', label: 'QQ音乐' },
    { key: 'kg', label: '酷狗' },
    { key: 'kw', label: '酷我' },
    { key: 'mg', label: '咪咕' },
]

const useStyles = makeStyles((theme) => ({
    root: {
        padding: theme.spacing(2),
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
    }
}

const OnlinePlaylistSearch = ({ active = true, onOpenDownloadDialog }) => {
    const classes = useStyles()

    const [source, setSource] = useState('wy')
    const [playlistQuery, setPlaylistQuery] = useState('')
    const [playlistAppliedQuery, setPlaylistAppliedQuery] = useState('')
    const [playlistSort, setPlaylistSort] = useState('hot')
    const [playlistTagGroups, setPlaylistTagGroups] = useState([])
    const [playlistSelectedTags, setPlaylistSelectedTags] = useState({})
    const [playlistSortOptions, setPlaylistSortOptions] = useState([
        { key: 'hot', label: '最热' },
        { key: 'new', label: '最新' },
    ])
    const [playlistRecommendRaw, setPlaylistRecommendRaw] = useState([])
    const [playlistLoading, setPlaylistLoading] = useState(false)
    const [playlistError, setPlaylistError] = useState('')
    const [playlistPage, setPlaylistPage] = useState(1)
    const [playlistTotal, setPlaylistTotal] = useState(0)
    const [playlistJumpPageInput, setPlaylistJumpPageInput] = useState('1')
    const [playlistMetaSource, setPlaylistMetaSource] = useState('')
    const [playlistLoadedKey, setPlaylistLoadedKey] = useState('')

    const badge = SOURCE_BADGE[source] || {}
    const playlistItems = playlistRecommendRaw
    const playlistTotalPages = Math.max(
        1,
        Math.ceil((Number(playlistTotal) || 0) / 30),
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
                    { key: 'new', label: '最新' },
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

        let keyword = playlistAppliedQuery.trim()
        const selectedTagIds = Object.values(playlistSelectedTags)
            .filter((tid) => tid && String(tid).trim())
            .map((tid) => String(tid).trim())
        if (!keyword) {
            keyword = selectedTagIds.length > 0 ? selectedTagIds.join(' ') : '热门'
        }

        const requestKey = JSON.stringify({
            source,
            sort: playlistSort,
            keyword,
            tags: selectedTagIds,
            page: playlistPage,
        })
        if (playlistLoadedKey === requestKey) return

        setPlaylistLoading(true)
        setPlaylistError('')

        httpClient(
            `/api/online/playlist/list?source=${encodeURIComponent(source)}&sortId=${encodeURIComponent(playlistSort)}&keyword=${encodeURIComponent(keyword)}&page=${encodeURIComponent(playlistPage)}`,
        )
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

    return (
        <div className={classes.root}>
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
        </div>
    )
}

export { OnlinePlaylistSearch }
export default OnlinePlaylistSearch
