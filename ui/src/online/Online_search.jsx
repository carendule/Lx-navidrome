import React, { useCallback, useEffect, useState } from 'react'
import { Title, useNotify, useTranslate } from 'react-admin'
import {
  Avatar,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Dialog,
  DialogContent,
  DialogTitle,
  Grid,
  IconButton,
  InputAdornment,
  MenuItem,
  Select,
  TextField,
  Typography,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import GetAppIcon from '@material-ui/icons/GetApp'
import SearchIcon from '@material-ui/icons/Search'
import RefreshIcon from '@material-ui/icons/Refresh'
import {
  clientUniqueId,
  clientUniqueIdHeader,
  httpClient,
} from '../dataProvider'
import { baseUrl } from '../utils'
import { fetchOnlineNameTemplate } from './online_source_settings_api'

const ONLINE_DOWNLOAD_TASK_CHANGED_EVENT = 'nd:online-download-task-changed'

const VIEW_MODES = {
  song: 'song',
  playlist: 'playlist',
}

const SOURCES = [
  { key: 'wy', label: '网易云' },
  { key: 'tx', label: 'QQ音乐' },
  { key: 'kg', label: '酷狗' },
  { key: 'kw', label: '酷我' },
  { key: 'mg', label: '咪咕' },
]

const TYPES = [
  { key: 'song', label: '歌曲' },
  { key: 'singer', label: '歌手' },
  { key: 'album', label: '专辑' },
]

const SOURCE_BADGE = {
  wy: { bg: '#fde2e2', color: '#a13030', name: '网易' },
  tx: { bg: '#d7f6e8', color: '#1f7a53', name: 'QQ' },
  kg: { bg: '#dfe8ff', color: '#2c4ca3', name: '酷狗' },
  kw: { bg: '#fdeccf', color: '#935b00', name: '酷我' },
  mg: { bg: '#ffe1ea', color: '#a3335d', name: '咪咕' },
}

const RANK_COLORS = [
  { bg: '#f5483b', color: '#fff' },
  { bg: '#f97c3c', color: '#fff' },
  { bg: '#ffb03a', color: '#fff' },
]

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

const getPlaylistSortOptions = (source) =>
  PLAYLIST_SORT_OPTIONS_BY_SOURCE[source] || PLAYLIST_SORT_OPTIONS_BY_SOURCE.wy

const normalizePlaylistTagGroups = (raw) => {
  const fallback = []
  if (!raw || !Array.isArray(raw.tags)) return fallback
  return raw.tags || []
}

const normalizePlaylistSortOptions = (raw, source) => {
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

const formatCompactCount = (value) => {
  if (typeof value === 'string' && value.trim()) return value.trim()
  const num = Number(value)
  if (!Number.isFinite(num) || num <= 0) return '0'
  if (num < 10000) return String(Math.round(num))
  return `${(num / 10000).toFixed(num >= 100000 ? 0 : 1)}万`
}

const useStyles = makeStyles((theme) => ({
  root: {
    padding: theme.spacing(2),
  },
  searchRow: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    marginBottom: theme.spacing(3),
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
    gap: theme.spacing(0.6),
    minHeight: 20,
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
    textAlign: 'right',
    fontVariantNumeric: 'tabular-nums',
    color: theme.palette.text.secondary,
  },
  actionCell: {
    display: 'flex',
    justifyContent: 'flex-end',
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
  downloadDialogPaper: {
    borderRadius: 18,
    minWidth: 360,
  },
  downloadDialogTitle: {
    paddingBottom: 0,
  },
  downloadDialogSubtitle: {
    color: theme.palette.text.secondary,
    marginTop: theme.spacing(0.5),
  },
  downloadOptionList: {
    display: 'grid',
    gap: theme.spacing(1.2),
    paddingTop: theme.spacing(1),
    paddingBottom: theme.spacing(1),
  },
  downloadOptionBtn: {
    justifyContent: 'flex-start',
    borderRadius: 14,
    textTransform: 'none',
    padding: theme.spacing(1.4, 1.8),
    fontWeight: 600,
  },
  modeSwitchWrap: {
    marginLeft: 'auto',
    display: 'flex',
    alignItems: 'center',
    flexShrink: 0,
  },
  modeSwitch: {
    position: 'relative',
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    width: 136,
    height: 36,
    padding: 0,
    borderRadius: 18,
    border: '1px solid rgba(25, 118, 210, 0.22)',
    backgroundColor: 'rgba(25, 118, 210, 0.08)',
    color: theme.palette.text.secondary,
    textTransform: 'none',
    boxShadow: 'none',
    cursor: 'pointer',
    overflow: 'hidden',
    transition:
      'background-color 0.2s ease, border-color 0.2s ease, transform 0.2s ease',
    '&:hover': {
      backgroundColor: 'rgba(25, 118, 210, 0.12)',
      borderColor: 'rgba(25, 118, 210, 0.32)',
      boxShadow: 'none',
    },
    '&:active': {
      transform: 'scale(0.99)',
    },
  },
  modeSwitchThumb: {
    position: 'absolute',
    top: 2,
    left: 2,
    width: 66,
    height: 32,
    borderRadius: 16,
    backgroundColor: theme.palette.primary.main,
    boxShadow: 'none',
    transition: 'transform 0.24s cubic-bezier(0.2, 0.8, 0.2, 1)',
  },
  modeSwitchThumbPlaylist: {
    transform: 'translateX(66px)',
  },
  modeSwitchText: {
    position: 'relative',
    zIndex: 1,
    flex: 1,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    textAlign: 'center',
    fontSize: '0.82rem',
    fontWeight: 700,
    lineHeight: '1',
    pointerEvents: 'none',
    transition: 'color 0.2s ease',
  },
  modeSwitchTextActive: {
    color: '#111',
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
}))

const QUALITY_META = {
  master: { label: 'Master', bg: '#f0e4ff', color: '#6d35b2' },
  flac24bit: { label: 'Hi-Res', bg: '#fff2cc', color: '#8d5f00' },
  ape: { label: 'APE', bg: '#ffe4cc', color: '#9a4d00' },
  flac: { label: 'FLAC', bg: '#dff6e7', color: '#1f7a53' },
  '320k': { label: '320k', bg: '#dce8ff', color: '#2c4ca3' },
  '128k': { label: '128k', bg: '#ececec', color: '#555' },
}

const QUALITY_ORDER = ['master', 'flac24bit', 'ape', 'flac', '320k', '128k']

const getSourceBadge = (src) =>
  SOURCE_BADGE[src] || { bg: '#e7e7e7', color: '#666', name: src || '未知' }

// Truncate a source name to a maximum of 5 visual characters, appending '…'
// if the original was longer. Used by the download-option buttons to show
// which source is currently resolving/downloading without overflowing.
const truncateSourceName = (name, max = 5) => {
  if (!name) return ''
  if (name.length <= max) return name
  return `${name.slice(0, max)}…`
}

const getQualityKeys = (item) => {
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

const formatDuration = (value) => {
  const num = Number(value)
  if (!Number.isFinite(num) || num <= 0) return '--:--'
  const totalSeconds = Math.floor(num >= 1000 ? num / 1000 : num)
  const mm = String(Math.floor(totalSeconds / 60)).padStart(2, '0')
  const ss = String(totalSeconds % 60).padStart(2, '0')
  return `${mm}:${ss}`
}

const formatFileSize = (input) => {
  if (typeof input === 'string') {
    const text = input.trim()
    if (!text) return '大小未知'
    // Already a formatted string like "12.34 MB" or "12.34M"
    if (/^\d+(\.\d+)?\s*(B|KB|MB|GB|TB)$/i.test(text)) return text.toUpperCase()
    // KW N_MINFO size field: "12.34M" without space
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

const getRawQualitySizeMap = (item) => {
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

const getQualitySizeMap = (item) => {
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
    // NetEase quality objects: hr(24bit), sq(flac), h(320k), m(192k), l(128k) each have .size in bytes
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
    // MG audioFormats use asize/isize (bytes); matches lxserver-main mg/musicSearch.js
    const rates =
      meta.audioFormats || meta.newRateFormats || meta.rateFormats || []
    rates.forEach((rate) => {
      const t = String(
        (rate && (rate.formatType || rate.qualityType || rate.type)) || '',
      ).toUpperCase()
      // asize = Android size, isize = iOS size (bytes); fall back to size/fileSize/androidSize
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
      else if (t.includes('MASTER')) assignIfMissing('master', size)
    })
    return result
  }

  if (source === 'kw') {
    // KW N_MINFO format: "level:xxx,bitrate:4000,format:flac,size:12.34M;level:xxx,bitrate:2000,..."
    // Matches lxserver-main kw/musicSearch.js: /level:(\w+),bitrate:(\d+),format:(\w+),size:([\w.]+)/
    // size is already a formatted string ("12.34M"), NOT bytes, so bypass assignIfMissing (which requires Number)
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

const getQualityOptions = (item) => {
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

const parseDownloadFileName = (contentDisposition) => {
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

const OnlineSearch = () => {
  const classes = useStyles()
  const translate = useTranslate()
  const notify = useNotify()

  const [viewMode, setViewMode] = useState(VIEW_MODES.song)
  const [query, setQuery] = useState('')
  const [playlistQuery, setPlaylistQuery] = useState('')
  const [playlistAppliedQuery, setPlaylistAppliedQuery] = useState('')
  const [type, setType] = useState('song')
  const [source, setSource] = useState('wy')
  const [playlistSort, setPlaylistSort] = useState('hot')
  const [playlistTagGroups, setPlaylistTagGroups] = useState([])
  const [playlistSelectedTags, setPlaylistSelectedTags] = useState({})
  const [playlistSortOptions, setPlaylistSortOptions] = useState(
    getPlaylistSortOptions('wy'),
  )
  const [playlistRecommendRaw, setPlaylistRecommendRaw] = useState([])
  const [playlistLoading, setPlaylistLoading] = useState(false)
  const [playlistError, setPlaylistError] = useState('')
  const [hotList, setHotList] = useState([])
  const [hotLoading, setHotLoading] = useState(false)
  const [hotDebug, setHotDebug] = useState('')
  const [searchLoading, setSearchLoading] = useState(false)
  const [searchError, setSearchError] = useState('')
  const [results, setResults] = useState([])
  const [lastKeyword, setLastKeyword] = useState('')
  const [hasSearched, setHasSearched] = useState(false)
  const [page, setPage] = useState(1)
  const limit = 20
  const [total, setTotal] = useState(0)
  const [jumpPageInput, setJumpPageInput] = useState('1')
  const [qualityDialogOpen, setQualityDialogOpen] = useState(false)
  const [downloadDialogOpen, setDownloadDialogOpen] = useState(false)
  const [selectedItem, setSelectedItem] = useState(null)
  const [selectedQuality, setSelectedQuality] = useState('')
  const [downloadErrorOpen, setDownloadErrorOpen] = useState(false)
  const [browserDownloadLoading, setBrowserDownloadLoading] = useState(false)
  const [browserDownloadProgress, setBrowserDownloadProgress] = useState(0)
  const [browserDownloadStatus, setBrowserDownloadStatus] = useState('idle')
  // Human-readable source name reported by the backend once the resolve
  // script has identified itself (e.g. "ikun[赞助][永久]"). For built-in
  // sources (wy/tx/kg/kw/mg) this stays empty and we fall back to the
  // selectedItem.source id. Reset whenever a new download kicks off.
  const [browserDownloadSourceName, setBrowserDownloadSourceName] = useState('')
  const [serverDownloadLoading, setServerDownloadLoading] = useState(false)
  const [serverDownloadStatus, setServerDownloadStatus] = useState('idle')

  const playlistItems = playlistRecommendRaw

  useEffect(() => {
    if (!playlistSortOptions.some((option) => option.key === playlistSort)) {
      setPlaylistSort(playlistSortOptions[0]?.key || 'hot')
    }
  }, [playlistSort, playlistSortOptions])

  const loadHotSearch = useCallback((src, forceRefresh = false) => {
    setHotLoading(true)
    setHotList([])
    setHotDebug('')
    const url = forceRefresh
      ? `/api/online/search/hot?source=${src}&refresh=true`
      : `/api/online/search/hot?source=${src}`
    httpClient(url)
      .then(({ json }) => {
        // console.log(`[HOT SEARCH DEBUG ${src}]`, json)
        if (json?.debug) {
          // console.log(`  DEBUG Info: ${json.debug}`)
          setHotDebug(json.debug)
        }
        setHotList(Array.isArray(json?.list) ? json.list : [])
      })
      .catch((err) => {
        // console.error(`[HOT SEARCH ERROR ${src}]`, err)
        setHotList([])
      })
      .finally(() => {
        setHotLoading(false)
      })
  }, [])

  useEffect(() => {
    if (viewMode === VIEW_MODES.song) loadHotSearch(source)
  }, [source, loadHotSearch, viewMode])

  useEffect(() => {
    if (viewMode !== VIEW_MODES.song) return
    if (!String(query || '').trim()) {
      setHasSearched(false)
      setResults([])
      setLastKeyword('')
      setSearchError('')
      setPage(1)
      setTotal(0)
      setJumpPageInput('1')
    }
  }, [query, viewMode])

  useEffect(() => {
    if (viewMode !== VIEW_MODES.playlist) return
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
        setPlaylistSelectedTags({})
        setPlaylistSort((current) =>
          nextSortOptions.some((item) => item.key === current)
            ? current
            : nextSortOptions[0]?.key || 'hot',
        )
      } catch (e) {
        if (cancelled) return
        setPlaylistTagGroups([])
        setPlaylistSelectedTags({})
        setPlaylistSortOptions(getPlaylistSortOptions(source))
        setPlaylistSort('hot')
        setPlaylistError('歌单分类加载失败，请稍后重试')
      }
    }

    loadPlaylistMeta()
    return () => {
      cancelled = true
    }
  }, [source, viewMode])

  useEffect(() => {
    if (viewMode !== VIEW_MODES.playlist) return
    if (!playlistSortOptions.some((item) => item.key === playlistSort)) return

    let cancelled = false
    setPlaylistLoading(true)
    setPlaylistError('')

    // Use search query if provided, otherwise build from selected tags
    let keyword = playlistAppliedQuery.trim()
    if (!keyword) {
      const selectedTagIds = Object.values(playlistSelectedTags)
        .filter((tid) => tid && String(tid).trim())
        .map((tid) => String(tid).trim())
      keyword = selectedTagIds.length > 0 ? selectedTagIds.join(' ') : '热门'
    }

    httpClient(
      `/api/online/playlist/list?source=${encodeURIComponent(source)}&sortId=${encodeURIComponent(playlistSort)}&keyword=${encodeURIComponent(keyword)}&page=1`,
    )
      .then(({ json }) => {
        if (cancelled) return
        const list = Array.isArray(json?.list) ? json.list : []
        setPlaylistRecommendRaw(list.map((item) => normalizePlaylistItem(item, source)))
        if (json?.error) setPlaylistError(String(json.error))
      })
      .catch(() => {
        if (cancelled) return
        setPlaylistError('歌单推荐加载失败，请稍后重试')
        setPlaylistRecommendRaw([])
      })
      .finally(() => {
        if (!cancelled) setPlaylistLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [
    playlistSelectedTags,
    playlistSort,
    playlistSortOptions,
    playlistAppliedQuery,
    source,
    viewMode,
  ])

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
          setSearchError('搜索失败，请稍后重试')
        })
        .finally(() => {
          setSearchLoading(false)
        })
    },
    [source, type, limit],
  )

  const handleSearch = useCallback(() => {
    if (viewMode === VIEW_MODES.song) {
      runSearch(query, 1)
      return
    }
    setPlaylistAppliedQuery(playlistQuery.trim())
  }, [playlistQuery, query, runSearch, viewMode])

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

  const handleToggleMode = useCallback(() => {
    setViewMode((current) =>
      current === VIEW_MODES.song ? VIEW_MODES.playlist : VIEW_MODES.song,
    )
  }, [])

  const totalPages = Math.max(1, Math.ceil((Number(total) || 0) / limit))

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

  const handleOpenDownloadDialog = useCallback((item) => {
    setSelectedItem(item)
    setSelectedQuality('')
    setQualityDialogOpen(true)
  }, [])

  const handleCloseQualityDialog = useCallback(() => {
    setQualityDialogOpen(false)
    setSelectedItem(null)
    setSelectedQuality('')
  }, [])

  const handlePickQuality = useCallback((qualityKey) => {
    setSelectedQuality(qualityKey)
    setQualityDialogOpen(false)
    setDownloadDialogOpen(true)
  }, [])

  const handleCloseDownloadDialog = useCallback(() => {
    setDownloadDialogOpen(false)
    setSelectedItem(null)
    setSelectedQuality('')
    setBrowserDownloadProgress(0)
    setBrowserDownloadStatus('idle')
    setBrowserDownloadSourceName('')
    setServerDownloadStatus('idle')
  }, [])
  const handleCloseDownloadErrorDialog = useCallback(() => {
    setDownloadErrorOpen(false)
  }, [])

  const qualityOptions = selectedItem ? getQualityOptions(selectedItem) : []

  const handleBrowserDownload = useCallback(async () => {
    if (!selectedItem || !selectedQuality) return

    const token = localStorage.getItem('token')
    const headers = new Headers({
      'Content-Type': 'application/json',
      Accept: 'application/octet-stream',
    })
    headers.set(clientUniqueIdHeader, clientUniqueId)
    if (token) headers.set('X-ND-Authorization', `Bearer ${token}`)

    setBrowserDownloadLoading(true)
    setBrowserDownloadProgress(0)
    setBrowserDownloadStatus('resolving')
    setBrowserDownloadSourceName('')
    try {
      // Pull the user-configured chip template from settings so the
      // server can name the downloaded file with the same order
      // they see in the settings panel. Fetching right before
      // starting the request means a chip reorder in another tab
      // is picked up on the next download without a refresh.
      const nameTemplate = await fetchOnlineNameTemplate()
      const startResponse = await fetch(
        baseUrl('/api/online/download/browser/start'),
        {
          method: 'POST',
          headers,
          body: JSON.stringify({
            songInfo: selectedItem,
            quality: selectedQuality,
            nameTemplate,
          }),
        },
      )

      if (!startResponse.ok) {
        throw new Error('resolve_failed')
      }

      const startData = await startResponse.json()
      const taskId = startData?.taskId
      if (!taskId) {
        throw new Error('task_start_failed')
      }

      let statusData = null
      const maxPoll = 60 * 8
      for (let i = 0; i < maxPoll; i += 1) {
        const progressResponse = await fetch(
          baseUrl(
            `/api/online/download/browser/progress/${encodeURIComponent(taskId)}`,
          ),
          {
            method: 'GET',
            headers,
          },
        )
        if (!progressResponse.ok) {
          throw new Error('task_progress_failed')
        }
        statusData = await progressResponse.json()
        const p = Number(statusData?.progress)
        const status = String(statusData?.status || '')
        setBrowserDownloadStatus(status || 'resolving')
        if (Number.isFinite(p)) {
          setBrowserDownloadProgress(Math.max(0, Math.min(100, p)))
        }
        if (typeof statusData?.sourceName === 'string') {
          setBrowserDownloadSourceName(statusData.sourceName)
        }
        if (statusData?.status === 'failed') {
          throw new Error(statusData?.error || 'task_failed')
        }
        if (statusData?.status === 'completed' && statusData?.fileReady) {
          setBrowserDownloadStatus('completed')
          setBrowserDownloadProgress(100)
          break
        }
        await new Promise((resolve) => setTimeout(resolve, 800))
      }

      if (!statusData || statusData?.status !== 'completed') {
        throw new Error('task_timeout')
      }

      const response = await fetch(
        baseUrl(
          `/api/online/download/browser/file/${encodeURIComponent(taskId)}`,
        ),
        {
          method: 'GET',
          headers,
        },
      )
      if (!response.ok) {
        throw new Error('file_fetch_failed')
      }

      const resolvedUrl = response.headers.get('x-online-resolved-url')
      const resolvedBy = response.headers.get('x-online-resolver-source')
      const downloadMode = response.headers.get('x-online-download-mode')
      // console.log('[OnlineDownload] Resolved URL:', resolvedUrl || '(empty)')
      // console.log('[OnlineDownload] Resolved By:', resolvedBy || '(unknown)')
      // console.log('[OnlineDownload] Backend Mode:', downloadMode || '(stream)')

      const blob = await response.blob()
      const objectUrl = window.URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = objectUrl
      anchor.download = parseDownloadFileName(
        response.headers.get('content-disposition'),
      )
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      window.URL.revokeObjectURL(objectUrl)
      handleCloseDownloadDialog()
    } catch (error) {
      handleCloseDownloadDialog()
      setDownloadErrorOpen(true)
    } finally {
      setBrowserDownloadLoading(false)
      setBrowserDownloadProgress(0)
      setBrowserDownloadStatus('idle')
      setBrowserDownloadSourceName('')
    }
  }, [handleCloseDownloadDialog, selectedItem, selectedQuality])

  const handleServerDownload = useCallback(async () => {
    if (!selectedItem || !selectedQuality) return

    const token = localStorage.getItem('token')
    const headers = new Headers({
      'Content-Type': 'application/json',
      Accept: 'application/json',
    })
    headers.set(clientUniqueIdHeader, clientUniqueId)
    if (token) headers.set('X-ND-Authorization', `Bearer ${token}`)

    setServerDownloadLoading(true)
    setServerDownloadStatus('resolving')
    try {
      // Persist the chip order alongside the request so the
      // server can use it when building the file path on disk.
      // Stored on the task itself, so even a mid-download
      // settings change doesn't break the in-flight task.
      const nameTemplate = await fetchOnlineNameTemplate()
      const response = await fetch(
        baseUrl('/api/online/download/server/start'),
        {
          method: 'POST',
          headers,
          body: JSON.stringify({
            songInfo: selectedItem,
            quality: selectedQuality,
            nameTemplate,
          }),
        },
      )
      if (!response.ok) {
        throw new Error('resolve_failed')
      }

      const data = await response.json()
      if (!data?.taskId) {
        throw new Error('task_start_failed')
      }

      window.dispatchEvent(new Event(ONLINE_DOWNLOAD_TASK_CHANGED_EVENT))
      notify('已加入服务器下载任务', 'info')
      handleCloseDownloadDialog()
    } catch (error) {
      setDownloadErrorOpen(true)
    } finally {
      setServerDownloadLoading(false)
      setServerDownloadStatus('idle')
    }
  }, [handleCloseDownloadDialog, notify, selectedItem, selectedQuality])

  const badge = SOURCE_BADGE[source] || {}
  const currentSearchValue =
    viewMode === VIEW_MODES.song ? query : playlistQuery
  const searchPlaceholder =
    viewMode === VIEW_MODES.song
      ? translate('online.search.placeholder', { _: '搜索歌曲、歌手...' })
      : '搜索歌单...'
  const activePlaylistSortLabel =
    playlistSortOptions.find((option) => option.key === playlistSort)?.label ||
    '最热'
  // Build category label from selected tags
  const getActivePlaylistCategoryLabel = () => {
    const selectedCount = Object.values(playlistSelectedTags).filter(
      (v) => v && String(v).trim(),
    ).length
    if (selectedCount === 0) return '全部'
    if (selectedCount === 1) {
      // Find the tag name
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

  return (
    <div className={classes.root}>
      <Title
        title={
          'Navidrome - ' +
          translate('menu.onlineSearch', { _: 'Online Search' })
        }
      />

      {/* ── Row 1: search controls ── */}
      <div className={classes.searchRow}>
        <TextField
          className={classes.searchInput}
          variant="outlined"
          size="small"
          placeholder={searchPlaceholder}
          value={currentSearchValue}
          onChange={(e) =>
            viewMode === VIEW_MODES.song
              ? setQuery(e.target.value)
              : setPlaylistQuery(e.target.value)
          }
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
        {viewMode === VIEW_MODES.song && (
          <Select
            className={`${classes.typeSelect} ${classes.selectControl}`}
            variant="outlined"
            value={type}
            onChange={(e) => setType(e.target.value)}
          >
            {TYPES.map((t) => (
              <MenuItem key={t.key} value={t.key}>
                {t.label}
              </MenuItem>
            ))}
          </Select>
        )}
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
        {viewMode === VIEW_MODES.playlist && (
          <Select
            className={`${classes.typeSelect} ${classes.selectControl}`}
            variant="outlined"
            value={playlistSort}
            onChange={(e) => setPlaylistSort(e.target.value)}
          >
            {playlistSortOptions.map((item) => (
              <MenuItem key={item.key} value={item.key}>
                {item.label}
              </MenuItem>
            ))}
          </Select>
        )}
        <div className={classes.modeSwitchWrap}>
          <Button
            className={classes.modeSwitch}
            onClick={handleToggleMode}
            role="switch"
            aria-checked={viewMode === VIEW_MODES.playlist}
          >
            <span
              className={`${classes.modeSwitchThumb} ${viewMode === VIEW_MODES.playlist
                ? classes.modeSwitchThumbPlaylist
                : ''
                }`}
            />
            <span
              className={`${classes.modeSwitchText} ${viewMode === VIEW_MODES.song ? classes.modeSwitchTextActive : ''
                }`}
            >
              搜歌
            </span>
            <span
              className={`${classes.modeSwitchText} ${viewMode === VIEW_MODES.playlist
                ? classes.modeSwitchTextActive
                : ''
                }`}
            >
              歌单
            </span>
          </Button>
        </div>
      </div>

      {/* ── Row 1.5: playlist tag selectors ── */}
      {viewMode === VIEW_MODES.playlist && playlistTagGroups.length > 0 && (
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

      {/* ── Row 2: hot search (shown only before search / when input is empty) ── */}
      {viewMode === VIEW_MODES.song && !hasSearched && (
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

      {viewMode === VIEW_MODES.song && hasSearched && (
        <Card className={classes.resultCard} variant="outlined">
          <CardContent>
            <div className={classes.resultStatus}>
              <Typography variant="subtitle2">
                {lastKeyword
                  ? `${lastKeyword} · ${results.length} 条结果`
                  : '搜索结果'}
              </Typography>
              {searchLoading && <CircularProgress size={18} />}
            </div>

            <div className={classes.tableHeader}>
              <span className={classes.colIdx}>#</span>
              <span>歌曲标题</span>
              <span className={classes.mobileHidden}>歌手</span>
              <span className={classes.mobileHidden}>专辑</span>
              <span className={classes.mobileHidden}>时长</span>
              <span className={classes.durationCell}>操作</span>
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
                  {lastKeyword ? '未找到匹配结果' : '输入关键词开始搜索'}
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
                            {item.name || '未知标题'}
                          </Typography>
                          <div className={classes.tagRow}>
                            <Chip
                              size="small"
                              label={sourceInfo.name}
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
                          onClick={() => handleOpenDownloadDialog(item)}
                          aria-label="下载"
                          title="下载"
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
                {`共 ${total} 条 · 第 ${page} / ${totalPages} 页`}
              </Typography>
              <div className={classes.paginationControls}>
                <Button
                  size="small"
                  variant="outlined"
                  onClick={handlePrevPage}
                  disabled={searchLoading || page <= 1}
                >
                  上一页
                </Button>
                <Button
                  size="small"
                  variant="outlined"
                  onClick={handleNextPage}
                  disabled={searchLoading || page >= totalPages}
                >
                  下一页
                </Button>
                <TextField
                  value={jumpPageInput}
                  onChange={(e) =>
                    setJumpPageInput(e.target.value.replace(/[^0-9]/g, ''))
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
                  disabled={searchLoading}
                >
                  跳转
                </Button>
              </div>
            </div>
          </CardContent>
        </Card>
      )}

      {viewMode === VIEW_MODES.playlist && (
        <Card className={classes.resultCard} variant="outlined">
          <CardContent>
            <div className={classes.resultStatus}>
              <Typography variant="subtitle2">
                {playlistAppliedQuery
                  ? `${playlistAppliedQuery} · ${playlistItems.length} 个歌单`
                  : `${getActivePlaylistCategoryLabel()} · ${activePlaylistSortLabel}`}
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

            <div className={classes.refreshRow}>
              <Button className={classes.refreshBtn} startIcon={<RefreshIcon />} onClick={handleSearch} size="small">
                {translate('online.search.refreshHot', { _: '刷新歌单' })}
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      <Dialog
        open={qualityDialogOpen}
        onClose={handleCloseQualityDialog}
        PaperProps={{ className: classes.downloadDialogPaper }}
      >
        <DialogTitle className={classes.downloadDialogTitle}>
          选择下载音质
          <Typography
            variant="body2"
            className={classes.downloadDialogSubtitle}
          >
            {selectedItem?.name || '当前歌曲'}
          </Typography>
        </DialogTitle>
        <DialogContent>
          <div className={classes.downloadOptionList}>
            {qualityOptions.length === 0 ? (
              <Typography variant="body2" color="textSecondary">
                当前歌曲没有可用音质信息
              </Typography>
            ) : (
              qualityOptions.map((option) => (
                <Button
                  key={option.key}
                  fullWidth
                  variant="outlined"
                  className={classes.downloadOptionBtn}
                  onClick={() => handlePickQuality(option.key)}
                >
                  {`${option.label} · ${option.sizeText}`}
                </Button>
              ))
            )}
          </div>
        </DialogContent>
      </Dialog>

      <Dialog
        open={downloadDialogOpen}
        onClose={handleCloseDownloadDialog}
        PaperProps={{ className: classes.downloadDialogPaper }}
      >
        <DialogTitle className={classes.downloadDialogTitle}>
          选择下载方式
          <Typography
            variant="body2"
            className={classes.downloadDialogSubtitle}
          >
            {selectedItem?.name || '当前歌曲'}
            {selectedQuality
              ? ` · ${QUALITY_META[selectedQuality]?.label || selectedQuality}`
              : ''}
          </Typography>
        </DialogTitle>
        <DialogContent>
          <div className={classes.downloadOptionList}>
            <Button
              fullWidth
              variant="outlined"
              className={classes.downloadOptionBtn}
              onClick={handleBrowserDownload}
              disabled={browserDownloadLoading}
              endIcon={
                browserDownloadLoading ? <CircularProgress size={16} /> : null
              }
            >
              {(() => {
                // Prefer the resolver's own display name (e.g. "ikun[赞助]…")
                // when the backend has reported it; otherwise fall back to
                // the source code selected in the search filter ("wy"/"kg"/…).
                const displayName =
                  browserDownloadSourceName || selectedItem?.source || ''
                const srcName = truncateSourceName(displayName)
                if (!browserDownloadLoading) return '浏览器下载'
                if (browserDownloadStatus === 'resolving')
                  return srcName ? `${srcName} 解析中...` : '解析中...'
                if (browserDownloadStatus === 'downloading')
                  return browserDownloadProgress > 0
                    ? `${srcName} 下载中 ${browserDownloadProgress}%`
                    : `${srcName} 下载中...`
                if (browserDownloadStatus === 'completed')
                  return `${srcName} 完成`
                return `${srcName} 处理中...`
              })()}
            </Button>
            <Button
              fullWidth
              variant="outlined"
              className={classes.downloadOptionBtn}
              onClick={handleServerDownload}
              disabled={serverDownloadLoading}
              endIcon={
                serverDownloadLoading ? <CircularProgress size={16} /> : null
              }
            >
              {(() => {
                // Server-mode runs resolveOnlineDownloadURL synchronously
                // before returning a taskId, so the source name isn't
                // streamed via the progress endpoint. Show the user-selected
                // source id ("wy"/"kg"/…) up front; once the SSE stream in
                // the global download panel reports the task, the name
                // there is already populated by the backend.
                const srcName = truncateSourceName(selectedItem?.source || '')
                if (!serverDownloadLoading) return '服务器下载'
                if (serverDownloadStatus === 'resolving')
                  return srcName ? `${srcName} 解析中...` : '解析中...'
                return `${srcName} 处理中...`
              })()}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog
        open={downloadErrorOpen}
        onClose={handleCloseDownloadErrorDialog}
        PaperProps={{ className: classes.downloadDialogPaper }}
      >
        <DialogTitle className={classes.downloadDialogTitle}>
          解析失败
        </DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            当前启用的音源无法解析出可用的直链信息
          </Typography>
          <div className={classes.downloadOptionList}>
            <Button
              fullWidth
              variant="outlined"
              className={classes.downloadOptionBtn}
              onClick={handleCloseDownloadErrorDialog}
            >
              确定
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

export default OnlineSearch
