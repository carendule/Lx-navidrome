import React, { useCallback, useEffect, useState } from 'react'
import { Title, useNotify } from 'react-admin'
import {
  Button,
  CircularProgress,
  Dialog,
  DialogContent,
  DialogTitle,
  Typography,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import { clientUniqueId, clientUniqueIdHeader, httpClient } from '../dataProvider'
import { baseUrl } from '../utils'
import { fetchOnlineNameTemplate } from './Online_source_settings_api'
import OnlineSongSearch from './Online_song_search'
import OnlinePlaylistSearch from './Online_playlist_search'

const ONLINE_DOWNLOAD_TASK_CHANGED_EVENT = 'nd:online-download-task-changed'

const VIEW_MODES = {
  song: 'song',
  playlist: 'playlist',
}

const QUALITY_META = {
  master: { label: 'Master', bg: '#f0e4ff', color: '#6d35b2' },
  flac24bit: { label: 'Hi-Res', bg: '#fff2cc', color: '#8d5f00' },
  ape: { label: 'APE', bg: '#ffe4cc', color: '#9a4d00' },
  flac: { label: 'FLAC', bg: '#dff6e7', color: '#1f7a53' },
  '320k': { label: '320k', bg: '#dce8ff', color: '#2c4ca3' },
  '128k': { label: '128k', bg: '#ececec', color: '#555' },
}

const useStyles = makeStyles((theme) => ({
  root: {
    padding: theme.spacing(2),
    display: 'flex',
    flexDirection: 'column',
  },
  modeSwitchWrap: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingLeft: theme.spacing(2),
    paddingRight: theme.spacing(2),
    marginBottom: theme.spacing(2),
    flexShrink: 0,
  },
  '@keyframes modeTitleFadeIn': {
    from: {
      opacity: 0,
      transform: 'translateY(8px) scale(0.985)',
      filter: 'blur(2px)',
    },
    to: {
      opacity: 1,
      transform: 'translateY(0) scale(1)',
      filter: 'blur(0)',
    },
  },
  modeTitle: {
    fontSize: '1.25rem',
    fontWeight: 700,
    color: theme.palette.text.primary,
    height: 36,
    display: 'flex',
    alignItems: 'center',
    willChange: 'opacity, transform, filter',
    animation: '$modeTitleFadeIn 420ms cubic-bezier(0.22, 1, 0.36, 1)',
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
}))

const getQualityKeys = (item) => {
  const QUALITY_ORDER = ['master', 'flac24bit', 'ape', 'flac', '320k', '128k']
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

const getQualitySizeMap = (item) => {
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

  const formatFileSize = (input) => {
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
      else if (t.includes('MASTER')) assignIfMissing('master', size)
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

const getQualityOptions = (item) => {
  const keys = getQualityKeys(item)
  const sizeMap = getQualitySizeMap(item)
  const formatFileSize = (input) => {
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

const truncateSourceName = (name, max = 5) => {
  if (!name) return ''
  if (name.length <= max) return name
  return `${name.slice(0, max)}…`
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
  const notify = useNotify()

  const [viewMode, setViewMode] = useState(VIEW_MODES.song)
  const [hasOpenedPlaylistView, setHasOpenedPlaylistView] = useState(false)
  const [qualityDialogOpen, setQualityDialogOpen] = useState(false)
  const [downloadDialogOpen, setDownloadDialogOpen] = useState(false)
  const [selectedItem, setSelectedItem] = useState(null)
  const [selectedQuality, setSelectedQuality] = useState('')
  const [downloadErrorOpen, setDownloadErrorOpen] = useState(false)
  const [browserDownloadLoading, setBrowserDownloadLoading] = useState(false)
  const [browserDownloadProgress, setBrowserDownloadProgress] = useState(0)
  const [browserDownloadStatus, setBrowserDownloadStatus] = useState('idle')
  const [browserDownloadSourceName, setBrowserDownloadSourceName] = useState('')
  const [serverDownloadLoading, setServerDownloadLoading] = useState(false)
  const [serverDownloadStatus, setServerDownloadStatus] = useState('idle')

  useEffect(() => {
    if (viewMode === VIEW_MODES.playlist) {
      setHasOpenedPlaylistView(true)
    }
  }, [viewMode])

  const handleToggleMode = useCallback(() => {
    setViewMode((current) =>
      current === VIEW_MODES.song ? VIEW_MODES.playlist : VIEW_MODES.song,
    )
  }, [])

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

  return (
    <div className={classes.root}>
      <Title
        title={
          'Navidrome - Online Search'
        }
      />

      {/* ── Mode switch bar with title ── */}
      <div className={classes.modeSwitchWrap}>
        <Typography key={viewMode} className={classes.modeTitle}>
          {viewMode === VIEW_MODES.song ? '在线歌曲' : '在线歌单'}
        </Typography>
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
            歌曲
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

      {/* ── Song Search Component ── */}
      <div style={{ display: viewMode === VIEW_MODES.song ? 'block' : 'none' }}>
        <OnlineSongSearch
          limit={20}
          onOpenDownloadDialog={handleOpenDownloadDialog}
        />
      </div>

      {/* ── Playlist Search Component ── */}
      {hasOpenedPlaylistView && (
        <div style={{ display: viewMode === VIEW_MODES.playlist ? 'block' : 'none' }}>
          <OnlinePlaylistSearch
            active={viewMode === VIEW_MODES.playlist}
            onOpenDownloadDialog={handleOpenDownloadDialog}
          />
        </div>
      )}

      {/* ── Download Dialogs ── */}
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
