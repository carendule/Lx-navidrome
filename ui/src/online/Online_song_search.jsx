import React, { useCallback, useEffect, useState } from 'react'
import {
    Card,
    CardContent,
    Typography,
    Button,
    TextField,
    CircularProgress,
    Box,
    Grid,
    Chip,
    Avatar,
    IconButton,
    InputAdornment,
    MenuItem,
    Select,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import SearchIcon from '@material-ui/icons/Search'
import RefreshIcon from '@material-ui/icons/Refresh'
import GetAppIcon from '@material-ui/icons/GetApp'
import { useTranslate } from 'react-admin'
import { httpClient } from '../dataProvider'
import {
    SOURCES,
    TYPES,
    RANK_COLORS,
    QUALITY_META,
    getSourceBadge,
    getQualityKeys,
    formatDuration,
} from './Online_constants'

const sourceName = (source, translate) => {
    const fallback = getSourceBadge(source).name || String(source || '').toUpperCase()
    return translate(`online.sources.${source}`, { _: fallback })
}

const searchTypeLabel = (typeKey, translate) => {
    const fallback =
        typeKey === 'song' ? 'Song' : typeKey === 'singer' ? 'Artist' : typeKey === 'album' ? 'Album' : typeKey
    return translate(`online.search.types.${typeKey}`, { _: fallback })
}

const useStyles = makeStyles((theme) => ({
    root: {
        padding: theme.spacing(2),
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
    typeSelect: {
        minWidth: 95,
    },
    sourceSelect: {
        minWidth: 110,
    },
    hotCard: {
        borderRadius: 12,
    },
    hotHeader: {
        display: 'flex',
        alignItems: 'center',
        gap: theme.spacing(1),
        marginBottom: theme.spacing(2),
    },
    hotTitle: {
        fontWeight: 700,
        fontSize: '1.05rem',
    },
    hotGrid: {
        marginTop: theme.spacing(0.5),
    },
    hotItem: {
        display: 'flex',
        alignItems: 'center',
        gap: theme.spacing(1),
        padding: theme.spacing(0.9, 1.5),
        borderRadius: 8,
        cursor: 'pointer',
        transition: 'background-color 0.15s',
        '&:hover': {
            backgroundColor: theme.palette.action.hover,
        },
        '&:hover $hotSearchHint': {
            opacity: 1,
        },
    },
    rankBadge: {
        width: 24,
        height: 24,
        borderRadius: '50%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontSize: '0.7rem',
        fontWeight: 700,
        flexShrink: 0,
        backgroundColor: theme.palette.action.selected,
        color: theme.palette.text.secondary,
    },
    hotWord: {
        flex: 1,
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
        fontSize: '0.9rem',
        color: theme.palette.text.primary,
    },
    hotSearchHint: {
        opacity: 0,
        color: theme.palette.text.disabled,
        fontSize: '0.9rem',
        transition: 'opacity 0.15s',
        flexShrink: 0,
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
    tableHeader: {
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
    resultRow: {
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
    colIdx: {
        textAlign: 'center',
        color: theme.palette.text.secondary,
        fontVariantNumeric: 'tabular-nums',
    },
    songCell: {
        display: 'flex',
        alignItems: 'center',
        minWidth: 0,
        gap: theme.spacing(1.1),
    },
    cover: {
        width: 54,
        height: 54,
        borderRadius: 8,
        background: theme.palette.action.hover,
        flexShrink: 0,
        [theme.breakpoints.down('sm')]: {
            width: 42,
            height: 42,
        },
    },
    songMain: {
        minWidth: 0,
        display: 'flex',
        flexDirection: 'column',
        gap: theme.spacing(0.45),
    },
    songName: {
        fontWeight: 600,
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
    },
    tagRow: {
        display: 'flex',
        alignItems: 'center',
        flexWrap: 'wrap',
        columnGap: theme.spacing(0.6),
        rowGap: theme.spacing(0.45),
        minHeight: 18,
        maxWidth: '100%',
    },
    sourceTag: {
        height: 18,
        borderRadius: 5,
        fontSize: '0.66rem',
        fontWeight: 700,
    },
    qualityTag: {
        height: 18,
        borderRadius: 5,
        fontSize: '0.66rem',
        fontWeight: 700,
    },
    textCell: {
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
    },
    durationCell: {
        textAlign: 'left',
        fontVariantNumeric: 'tabular-nums',
        color: theme.palette.text.secondary,
    },
    headerCenterCell: {
        display: 'block',
        width: '100%',
        textAlign: 'center',
    },
    actionCell: {
        display: 'flex',
        justifyContent: 'center',
    },
    downloadBtn: {
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
}))

const OnlineSongSearch = ({
    limit = 20,
    onOpenDownloadDialog,
}) => {
    const classes = useStyles()
    const translate = useTranslate()

    // Manage source and type locally
    const [source, setSource] = useState('wy')
    const [type, setType] = useState('song')
    const [query, setQuery] = useState('')
    const [results, setResults] = useState([])
    const [searchLoading, setSearchLoading] = useState(false)
    const [searchError, setSearchError] = useState('')
    const [lastKeyword, setLastKeyword] = useState('')
    const [hasSearched, setHasSearched] = useState(false)
    const [page, setPage] = useState(1)
    const [total, setTotal] = useState(0)
    const [jumpPageInput, setJumpPageInput] = useState('1')
    const [hotList, setHotList] = useState([])
    const [hotLoading, setHotLoading] = useState(false)
    const [hotDebug, setHotDebug] = useState('')

    const badge = getSourceBadge(source)
    const totalPages = Math.max(1, Math.ceil((Number(total) || 0) / limit))

    const loadHotSearch = useCallback((src, forceRefresh = false) => {
        setHotLoading(true)
        setHotList([])
        setHotDebug('')
        const url = forceRefresh
            ? `/api/online/search/hot?source=${src}&refresh=true`
            : `/api/online/search/hot?source=${src}`
        httpClient(url)
            .then(({ json }) => {
                if (json?.debug) {
                    setHotDebug(json.debug)
                }
                setHotList(Array.isArray(json?.list) ? json.list : [])
            })
            .catch(() => {
                setHotList([])
            })
            .finally(() => {
                setHotLoading(false)
            })
    }, [])

    useEffect(() => {
        loadHotSearch(source)
    }, [source, loadHotSearch])

    useEffect(() => {
        if (!String(query || '').trim()) {
            setHasSearched(false)
            setResults([])
            setLastKeyword('')
            setSearchError('')
            setPage(1)
            setTotal(0)
            setJumpPageInput('1')
        }
    }, [query])

    const runSearch = useCallback(
        (rawKeyword, targetPage = 1) => {
            const keyword = String(rawKeyword || '').trim()
            if (!keyword) {
                setHasSearched(false)
                setResults([])
                setLastKeyword('')
                setSearchError('')
                setPage(1)
                setTotal(0)
                setJumpPageInput('1')
                return
            }

            const normalizedPage = Math.max(1, Number(targetPage) || 1)
            setHasSearched(true)
            setSearchLoading(true)
            setSearchError('')
            setLastKeyword(keyword)
            setPage(normalizedPage)
            setJumpPageInput(String(normalizedPage))

            httpClient(
                `/api/online/search?source=${encodeURIComponent(source)}&type=${encodeURIComponent(type)}&name=${encodeURIComponent(keyword)}&limit=${encodeURIComponent(limit)}&page=${encodeURIComponent(normalizedPage)}`,
            )
                .then(({ json }) => {
                    const list = Array.isArray(json?.list) ? json.list : []
                    const totalCount = Number(json?.total)
                    setResults(list)
                    setTotal(
                        Number.isFinite(totalCount) && totalCount >= 0
                            ? totalCount
                            : list.length,
                    )
                    if (Number(json?.page) > 0) {
                        setPage(Number(json.page))
                        setJumpPageInput(String(json.page))
                    }
                    if (json?.error) {
                        setSearchError(String(json.error))
                    }
                })
                .catch(() => {
                    setResults([])
                    setTotal(0)
                    setSearchError(translate('online.search.error', { _: 'Search failed, please try again later' }))
                })
                .finally(() => {
                    setSearchLoading(false)
                })
        },
        [source, type, limit, translate],
    )

    const handleSearch = useCallback(() => {
        runSearch(query, 1)
    }, [query, runSearch])

    const handleKeyDown = useCallback(
        (e) => {
            if (e.key === 'Enter') handleSearch()
        },
        [handleSearch],
    )

    const handleHotItemClick = useCallback(
        (word) => {
            setQuery(word)
            runSearch(word, 1)
        },
        [runSearch],
    )

    const handlePrevPage = useCallback(() => {
        if (searchLoading || page <= 1) return
        runSearch(lastKeyword || query, page - 1)
    }, [searchLoading, page, runSearch, lastKeyword, query])

    const handleNextPage = useCallback(() => {
        if (searchLoading || page >= totalPages) return
        runSearch(lastKeyword || query, page + 1)
    }, [searchLoading, page, totalPages, runSearch, lastKeyword, query])

    const handleJumpPage = useCallback(() => {
        const target = Math.min(totalPages, Math.max(1, Number(jumpPageInput) || 1))
        runSearch(lastKeyword || query, target)
    }, [jumpPageInput, runSearch, lastKeyword, query, totalPages])

    const searchPlaceholder = translate('online.search.placeholder', { _: 'Search songs or artists...' })

    return (
        <div className={classes.root}>
            {/* ── Row 1: search controls ── */}
            <div className={classes.searchRow}>
                <TextField
                    className={classes.searchInput}
                    variant="outlined"
                    size="small"
                    placeholder={searchPlaceholder}
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    onKeyDown={handleKeyDown}
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
                    {translate('online.search.button', { _: 'Search' })}
                </Button>
                <Select
                    className={`${classes.typeSelect} ${classes.selectControl}`}
                    variant="outlined"
                    value={type}
                    onChange={(e) => setType(e.target.value)}
                >
                    {TYPES.map((t) => (
                        <MenuItem key={t.key} value={t.key}>
                            {searchTypeLabel(t.key, translate)}
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
                            {sourceName(s.key, translate)}
                        </MenuItem>
                    ))}
                </Select>
            </div>

            {/* ── Hot search ── */}
            {!hasSearched && (
                <Card className={classes.hotCard} variant="outlined">
                    <CardContent>
                        <div className={classes.hotHeader}>
                            <Typography className={classes.hotTitle}>
                                {translate('online.search.hotSearch', {
                                    _: 'Trending Searches',
                                })}
                            </Typography>
                            <Chip
                                size="small"
                                label={sourceName(source, translate)}
                                style={{
                                    backgroundColor: badge.bg,
                                    color: badge.color,
                                    fontWeight: 600,
                                    fontSize: '0.7rem',
                                    height: 20,
                                }}
                            />
                        </div>

                        {hotLoading ? (
                            <div className={classes.loadingBox}>
                                <CircularProgress size={32} />
                            </div>
                        ) : hotList.length === 0 ? (
                            <div className={classes.emptyBox}>
                                <Typography variant="body2">
                                    {translate('online.search.hotEmpty', {
                                        _: 'No trending data',
                                    })}
                                </Typography>
                            </div>
                        ) : (
                            <Grid container className={classes.hotGrid}>
                                {hotList.map((word, idx) => {
                                    const rankStyle = RANK_COLORS[idx] || null
                                    return (
                                        <Grid item xs={12} sm={6} md={4} lg={3} key={idx}>
                                            <div
                                                className={classes.hotItem}
                                                onClick={() => handleHotItemClick(word)}
                                            >
                                                <Box
                                                    className={classes.rankBadge}
                                                    style={
                                                        rankStyle
                                                            ? {
                                                                backgroundColor: rankStyle.bg,
                                                                color: rankStyle.color,
                                                            }
                                                            : {}
                                                    }
                                                >
                                                    {idx + 1}
                                                </Box>
                                                <Typography className={classes.hotWord} title={word}>
                                                    {word}
                                                </Typography>
                                                <SearchIcon
                                                    className={classes.hotSearchHint}
                                                    fontSize="small"
                                                />
                                            </div>
                                        </Grid>
                                    )
                                })}
                            </Grid>
                        )}

                        <div className={classes.refreshRow}>
                            <Button
                                className={classes.refreshBtn}
                                startIcon={<RefreshIcon />}
                                onClick={() => loadHotSearch(source, true)}
                                disabled={hotLoading}
                                size="small"
                            >
                                {translate('online.search.refreshHot', {
                                    _: 'Refresh Trending',
                                })}
                            </Button>
                        </div>

                        {hotDebug && (
                            <div
                                style={{
                                    marginTop: 12,
                                    padding: 8,
                                    backgroundColor: '#f5f5f5',
                                    borderRadius: 4,
                                }}
                            >
                                <Typography
                                    variant="caption"
                                    style={{
                                        fontFamily: 'monospace',
                                        fontSize: '0.7rem',
                                        color: '#666',
                                    }}
                                >
                                    Debug: {hotDebug}
                                </Typography>
                            </div>
                        )}
                    </CardContent>
                </Card>
            )}

            {/* ── Search results ── */}
            {hasSearched && (
                <Card className={classes.resultCard} variant="outlined">
                    <CardContent>
                        <div className={classes.resultStatus}>
                            <Typography variant="subtitle2">
                                {lastKeyword
                                    ? `${lastKeyword} · ${results.length} ${translate('online.search.results', { _: 'results' })}`
                                    : translate('online.search.resultsTitle', { _: 'Search Results' })}
                            </Typography>
                            {searchLoading && <CircularProgress size={18} />}
                        </div>

                        <div className={classes.tableHeader}>
                            <span className={classes.colIdx}>#</span>
                            <span>{translate('online.songTable.title', { _: 'Title' })}</span>
                            <span className={classes.mobileHidden}>{translate('online.songTable.artist', { _: 'Artist' })}</span>
                            <span className={classes.mobileHidden}>{translate('online.songTable.album', { _: 'Album' })}</span>
                            <span className={classes.mobileHidden}>{translate('online.songTable.duration', { _: 'Duration' })}</span>
                            <span className={classes.headerCenterCell}>{translate('online.songTable.action', { _: 'Action' })}</span>
                        </div>

                        {searchLoading ? (
                            <div className={classes.loadingBox}>
                                <CircularProgress size={32} />
                            </div>
                        ) : searchError ? (
                            <div className={classes.emptyBox}>
                                <Typography variant="body2">{searchError}</Typography>
                            </div>
                        ) : results.length === 0 ? (
                            <div className={classes.emptyBox}>
                                <Typography variant="body2">
                                    {lastKeyword
                                        ? translate('online.search.noMatch', { _: 'No matching results' })
                                        : translate('online.search.inputHint', { _: 'Enter a keyword to start searching' })}
                                </Typography>
                            </div>
                        ) : (
                            <div>
                                {results.map((item, idx) => {
                                    const sourceInfo = getSourceBadge(item.source || source)
                                    const qualityKeys = getQualityKeys(item)
                                    return (
                                        <div
                                            key={`${item.id || item.name || 'row'}-${idx}`}
                                            className={classes.resultRow}
                                        >
                                            <span className={classes.colIdx}>
                                                {(page - 1) * limit + idx + 1}
                                            </span>

                                            <div className={classes.songCell}>
                                                <Avatar
                                                    variant="rounded"
                                                    src={item.img || undefined}
                                                    className={classes.cover}
                                                />
                                                <div className={classes.songMain}>
                                                    <Typography
                                                        className={classes.songName}
                                                        title={item.name || ''}
                                                    >
                                                        {item.name || translate('online.common.unknownTitle', { _: 'Unknown title' })}
                                                    </Typography>
                                                    <div className={classes.tagRow}>
                                                        <Chip
                                                            size="small"
                                                            label={sourceName(item.source || source, translate)}
                                                            className={classes.sourceTag}
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
                                                                    key={`${item.id || item.name || idx}-${qualityKey}`}
                                                                    label={qualityMeta.label}
                                                                    className={classes.qualityTag}
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
                                                className={`${classes.textCell} ${classes.mobileHidden}`}
                                                title={item.singer || ''}
                                            >
                                                {item.singer || '--'}
                                            </Typography>
                                            <Typography
                                                className={`${classes.textCell} ${classes.mobileHidden}`}
                                                title={item.albumName || ''}
                                            >
                                                {item.albumName || '--'}
                                            </Typography>
                                            <Typography
                                                className={`${classes.durationCell} ${classes.mobileHidden}`}
                                            >
                                                {formatDuration(item.duration || item.interval)}
                                            </Typography>
                                            <div className={classes.actionCell}>
                                                <IconButton
                                                    size="small"
                                                    className={classes.downloadBtn}
                                                    onClick={() => onOpenDownloadDialog(item)}
                                                    aria-label={translate('online.download.action', { _: 'Download' })}
                                                    title={translate('online.download.action', { _: 'Download' })}
                                                >
                                                    <GetAppIcon fontSize="small" />
                                                </IconButton>
                                            </div>
                                        </div>
                                    )
                                })}
                            </div>
                        )}

                        <div className={classes.paginationRow}>
                            <Typography className={classes.paginationInfo}>
                                {`${translate('online.pagination.total', { _: 'Total' })} ${total} · ${translate('online.pagination.page', { _: 'Page' })} ${page} / ${totalPages}`}
                            </Typography>
                            <div className={classes.paginationControls}>
                                <Button
                                    size="small"
                                    variant="outlined"
                                    onClick={handlePrevPage}
                                    disabled={searchLoading || page <= 1}
                                >
                                    {translate('online.pagination.prev', { _: 'Previous' })}
                                </Button>
                                <Button
                                    size="small"
                                    variant="outlined"
                                    onClick={handleNextPage}
                                    disabled={searchLoading || page >= totalPages}
                                >
                                    {translate('online.pagination.next', { _: 'Next' })}
                                </Button>
                                <TextField
                                    value={jumpPageInput}
                                    onChange={(e) =>
                                        setJumpPageInput(e.target.value.replace(/[^0-9]/g, ''))
                                    }
                                    variant="outlined"
                                    size="small"
                                    className={classes.jumpInput}
                                    placeholder={translate('online.pagination.pageInput', { _: 'Page' })}
                                    onKeyDown={(e) => {
                                        if (e.key === 'Enter') handleJumpPage()
                                    }}
                                />
                                <Button
                                    size="small"
                                    variant="contained"
                                    color="primary"
                                    onClick={handleJumpPage}
                                    disabled={searchLoading}
                                >
                                    {translate('online.pagination.go', { _: 'Go' })}
                                </Button>
                            </div>
                        </div>
                    </CardContent>
                </Card>
            )}
        </div>
    )
}

export default OnlineSongSearch
