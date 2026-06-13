import React from 'react'
import { Title, useNotify, useTranslate } from 'react-admin'
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Divider,
  IconButton,
  TextField,
  Typography,
} from '@material-ui/core'
import { makeStyles, useTheme } from '@material-ui/core/styles'
import {
  MdAdd,
  MdCheckCircle,
  MdDeleteOutline,
  MdDragIndicator,
  MdFolder,
  MdSettings,
} from 'react-icons/md'
import { httpClient } from '../dataProvider'

const ONLINE_SOURCE_STATUS_CHANGED_EVENT = 'nd:online-source-status-changed'

const useStyles = makeStyles((theme) => ({
  root: {
    marginTop: theme.spacing(2),
  },
  pageTitle: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    marginBottom: theme.spacing(0.5),
  },
  titleIcon: {
    color: theme.palette.primary.main,
  },
  subtitle: {
    color: theme.palette.text.secondary,
    marginBottom: theme.spacing(2),
  },
  badge: {
    fontSize: '0.72rem',
    height: 22,
    marginLeft: theme.spacing(1),
  },
  card: {
    borderRadius: 14,
    border: `1px solid ${theme.palette.divider}`,
    marginBottom: theme.spacing(2),
  },
  row: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: theme.spacing(1),
  },
  titleRow: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    minWidth: 0,
  },
  left: {
    display: 'flex',
    alignItems: 'flex-start',
    gap: theme.spacing(1),
    minWidth: 0,
    flex: 1,
  },
  dragHandle: {
    color: theme.palette.text.disabled,
    marginTop: 2,
    cursor: 'grab',
    touchAction: 'none',
  },
  dragHandleDisabled: {
    cursor: 'not-allowed',
    opacity: 0.35,
  },
  cardDragging: {
    opacity: 0.6,
  },
  cardDropTarget: {
    borderColor: theme.palette.primary.main,
  },
  sourceTitle: {
    fontWeight: 600,
    lineHeight: 1.2,
  },
  sourceMeta: {
    color: theme.palette.text.secondary,
    marginTop: theme.spacing(0.5),
    display: 'flex',
    flexWrap: 'wrap',
    gap: theme.spacing(1),
  },
  statusEnabled: {
    backgroundColor: theme.palette.success.light,
    color: theme.palette.success.contrastText,
    fontWeight: 600,
  },
  statusHealthy: {
    marginLeft: theme.spacing(1),
    backgroundColor: theme.palette.success.main,
    color: theme.palette.common.white,
    fontWeight: 600,
  },
  tags: {
    marginTop: theme.spacing(1.2),
    display: 'flex',
    flexWrap: 'wrap',
    gap: theme.spacing(0.8),
  },
  tag: {
    height: 22,
    fontSize: '0.72rem',
    borderRadius: 7,
  },
  deleteBtn: {
    color: theme.palette.text.secondary,
  },
  manageCardTitle: {
    fontWeight: 600,
  },
  manageCardSubtitle: {
    color: theme.palette.text.secondary,
    marginTop: theme.spacing(0.4),
  },
  manageButton: {
    borderRadius: 999,
    textTransform: 'none',
    fontWeight: 600,
  },
  addButtonWrap: {
    marginBottom: theme.spacing(2),
  },
  settingsSection: {
    marginBottom: theme.spacing(3),
  },
  settingsTitle: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    marginBottom: theme.spacing(1),
  },
  settingsField: {
    width: '100%',
    marginBottom: theme.spacing(1.5),
  },
  saveButtonWrap: {
    marginBottom: theme.spacing(2),
  },
}))

const tagColor = (theme, tag) => {
  const map = {
    网易: { bg: '#fde2e2', color: '#a13030' },
    QQ: { bg: '#d7f6e8', color: '#1f7a53' },
    酷我: { bg: '#fdeccf', color: '#935b00' },
    酷狗: { bg: '#dfe8ff', color: '#2c4ca3' },
    咪咕: { bg: '#ffe1ea', color: '#a3335d' },
    git: {
      bg: theme.palette.action.selected,
      color: theme.palette.text.primary,
    },
    qsvip: {
      bg: theme.palette.action.hover,
      color: theme.palette.text.primary,
    },
  }
  return (
    map[tag] || {
      bg: theme.palette.action.hover,
      color: theme.palette.text.secondary,
    }
  )
}

const sourceCodeMap = {
  kg: '网易',
  tx: 'QQ',
  wy: '酷我',
  kw: '酷狗',
  mg: '咪咕',
}

const OnlineSetting = () => {
  const classes = useStyles()
  const theme = useTheme()
  const notify = useNotify()
  const translate = useTranslate()
  const [sources, setSources] = React.useState([])
  const [downloadPath, setDownloadPath] = React.useState('')
  const [savingDownloadPath, setSavingDownloadPath] = React.useState(false)
  const [draggingID, setDraggingID] = React.useState('')
  const [dragOverID, setDragOverID] = React.useState('')
  const [touchDraggingID, setTouchDraggingID] = React.useState('')
  const touchLongPressTimerRef = React.useRef(null)
  const touchPointRef = React.useRef({ x: 0, y: 0 })
  const draggingIDRef = React.useRef('')
  const dragOverIDRef = React.useRef('')
  const dragPlaceAfterRef = React.useRef(false)
  const dragModeRef = React.useRef('')
  const uploadInputRef = React.useRef(null)

  const loadSources = React.useCallback(() => {
    httpClient('/api/online/source')
      .then(({ json }) => {
        setSources(Array.isArray(json) ? json : [])
      })
      .catch(() => {
        setSources([])
      })
  }, [])

  const loadSettings = React.useCallback(() => {
    httpClient('/api/online/source/settings')
      .then(({ json }) => {
        setDownloadPath(
          typeof json?.downloadPath === 'string' ? json.downloadPath : '',
        )
      })
      .catch(() => {
        setDownloadPath('')
      })
  }, [])

  React.useEffect(() => {
    loadSources()
    loadSettings()
  }, [loadSources, loadSettings])

  const notifyOnlineSourceStatusChanged = React.useCallback(() => {
    window.dispatchEvent(new Event(ONLINE_SOURCE_STATUS_CHANGED_EVENT))
  }, [])

  const handleToggle = React.useCallback(
    (item) => {
      httpClient(`/api/online/source/${encodeURIComponent(item.id)}/toggle`, {
        method: 'POST',
        body: JSON.stringify({ enabled: !item.enabled }),
        headers: new Headers({ 'Content-Type': 'application/json' }),
      }).then(() => {
        loadSources()
        notifyOnlineSourceStatusChanged()
      })
    },
    [loadSources, notifyOnlineSourceStatusChanged],
  )

  const handleDelete = React.useCallback(
    (item) => {
      httpClient(`/api/online/source/${encodeURIComponent(item.id)}`, {
        method: 'DELETE',
      }).then(() => {
        loadSources()
        notifyOnlineSourceStatusChanged()
      })
    },
    [loadSources, notifyOnlineSourceStatusChanged],
  )

  const handlePickUpload = React.useCallback(() => {
    if (uploadInputRef.current) {
      uploadInputRef.current.click()
    }
  }, [])

  const handleSaveDownloadPath = React.useCallback(() => {
    setSavingDownloadPath(true)
    httpClient('/api/online/source/settings', {
      method: 'POST',
      body: JSON.stringify({ downloadPath }),
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
      .then(({ json }) => {
        setDownloadPath(
          typeof json?.downloadPath === 'string' ? json.downloadPath : '',
        )
        notify('下载路径已保存', 'info')
      })
      .catch(() => {
        notify('下载路径保存失败', 'warning')
      })
      .finally(() => {
        setSavingDownloadPath(false)
      })
  }, [downloadPath, notify])

  const handleUploadChange = React.useCallback(
    async (event) => {
      const file = event.target.files && event.target.files[0]
      event.target.value = ''
      if (!file) {
        return
      }

      if (!file.name.toLowerCase().endsWith('.js')) {
        // eslint-disable-next-line no-console
        console.error('脚本解析失败: 仅支持 .js 文件', {
          filename: file.name,
        })
        notify('脚本解析失败', 'warning')
        return
      }

      try {
        const content = await file.text()
        const uploadScript = async (allowUnsafeVM = false) => {
          const { json } = await httpClient('/api/online/source/upload', {
            method: 'POST',
            body: JSON.stringify({
              filename: file.name,
              content,
              allowUnsafeVM,
            }),
            headers: new Headers({ 'Content-Type': 'application/json' }),
          })

          if (json && json.success === false) {
            if (json.disabledVM) {
              notify(json.message || '服务器已禁用原生 VM 模式', 'warning')
              return null
            }
            if (json.requireUnsafe && !allowUnsafeVM) {
              const confirmed = window.confirm(
                json.message ||
                  '该脚本需要原生 VM 模式运行，可能存在安全风险，是否继续？',
              )
              if (!confirmed) {
                return null
              }
              return uploadScript(true)
            }
            throw new Error(json.message || '脚本解析失败')
          }

          return json
        }

        const json = await uploadScript(false)
        if (!json) {
          return
        }
        setSources((prev) => [
          ...prev.filter((item) => item.id !== json.id),
          json,
        ])
        notifyOnlineSourceStatusChanged()
      } catch (e) {
        // eslint-disable-next-line no-console
        console.error('脚本解析失败', {
          filename: file.name,
          message: e?.message,
          status: e?.status,
          body: e?.body,
          stack: e?.stack,
          error: e,
        })
        notify('脚本解析失败', 'warning')
      }
    },
    [notify, notifyOnlineSourceStatusChanged],
  )

  const displaySources = React.useMemo(() => {
    const list = Array.isArray(sources) ? [...sources] : []
    const enabled = list.filter((item) => item.enabled)
    const disabled = list.filter((item) => !item.enabled)

    enabled.sort((a, b) => {
      const left = Number(a.enabledOrder) || Number.MAX_SAFE_INTEGER
      const right = Number(b.enabledOrder) || Number.MAX_SAFE_INTEGER
      if (left !== right) return left - right
      return String(a.createdAt || '').localeCompare(String(b.createdAt || ''))
    })

    return [...enabled, ...disabled]
  }, [sources])

  const reorderEnabledSources = React.useCallback(
    (dragID, targetID, placeAfter = false) => {
      if (!dragID || !targetID || dragID === targetID) return

      const enabledIDs = displaySources
        .filter((item) => item.enabled)
        .map((item) => item.id)
      const fromIndex = enabledIDs.indexOf(dragID)
      if (fromIndex < 0) return

      const nextIDs = [...enabledIDs]
      const [moved] = nextIDs.splice(fromIndex, 1)
      const targetIndex = nextIDs.indexOf(targetID)
      if (targetIndex < 0) return
      const insertIndex = placeAfter ? targetIndex + 1 : targetIndex
      nextIDs.splice(
        Math.max(0, Math.min(nextIDs.length, insertIndex)),
        0,
        moved,
      )

      const enabledMap = new Map(
        displaySources
          .filter((item) => item.enabled)
          .map((item) => [item.id, item]),
      )
      const disabled = displaySources.filter((item) => !item.enabled)
      const reorderedEnabled = nextIDs.map((id, idx) => ({
        ...enabledMap.get(id),
        enabledOrder: idx + 1,
      }))
      setSources([...reorderedEnabled, ...disabled])

      httpClient('/api/online/source/reorder', {
        method: 'POST',
        body: JSON.stringify({ sourceIds: nextIDs }),
        headers: new Headers({ 'Content-Type': 'application/json' }),
      })
        .then(() => {
          loadSources()
        })
        .catch(() => {
          loadSources()
          notify('排序保存失败', 'warning')
        })
    },
    [displaySources, loadSources, notify],
  )

  const clearTouchLongPressTimer = React.useCallback(() => {
    if (touchLongPressTimerRef.current) {
      window.clearTimeout(touchLongPressTimerRef.current)
      touchLongPressTimerRef.current = null
    }
  }, [])

  const handleCardMouseEnter = React.useCallback((event, item) => {
    if (!draggingIDRef.current || !item.enabled) return
    dragOverIDRef.current = item.id
    setDragOverID(item.id)
  }, [])

  const commitPointerDrag = React.useCallback(() => {
    const dragID = draggingIDRef.current
    const targetID = dragOverIDRef.current
    if (dragID && targetID && dragID !== targetID) {
      reorderEnabledSources(dragID, targetID, dragPlaceAfterRef.current)
    }

    draggingIDRef.current = ''
    dragOverIDRef.current = ''
    dragPlaceAfterRef.current = false
    dragModeRef.current = ''
    setDraggingID('')
    setTouchDraggingID('')
    setDragOverID('')
  }, [reorderEnabledSources])

  const handleMouseDragStart = React.useCallback((event, item) => {
    if (!item.enabled) return
    event.preventDefault()
    draggingIDRef.current = item.id
    dragOverIDRef.current = item.id
    dragPlaceAfterRef.current = false
    dragModeRef.current = 'mouse'
    setDraggingID(item.id)
    setDragOverID(item.id)
  }, [])

  const handleMouseMove = React.useCallback((event) => {
    if (dragModeRef.current !== 'mouse' || !draggingIDRef.current) return
    const element = document.elementFromPoint(event.clientX, event.clientY)
    if (!element) return

    let el = element
    while (el && el !== document.body) {
      const sid = el.getAttribute && el.getAttribute('data-source-id')
      if (sid) {
        if (el.getAttribute('data-enabled') === 'true') {
          dragOverIDRef.current = sid
          setDragOverID(sid)
          const rect = el.getBoundingClientRect()
          dragPlaceAfterRef.current = event.clientY > rect.top + rect.height / 2
        }
        break
      }
      el = el.parentElement
    }
  }, [])

  const handleMouseUp = React.useCallback(() => {
    if (dragModeRef.current !== 'mouse') return
    commitPointerDrag()
  }, [commitPointerDrag])

  const handleTouchStart = React.useCallback(
    (event, item) => {
      if (!item.enabled) return
      const touch = event.touches && event.touches[0]
      if (!touch) return

      touchPointRef.current = { x: touch.clientX, y: touch.clientY }
      clearTouchLongPressTimer()
      touchLongPressTimerRef.current = window.setTimeout(() => {
        draggingIDRef.current = item.id
        dragOverIDRef.current = item.id
        dragPlaceAfterRef.current = false
        dragModeRef.current = 'touch'
        setDraggingID(item.id)
        setTouchDraggingID(item.id)
        setDragOverID(item.id)
      }, 280)
    },
    [clearTouchLongPressTimer],
  )

  const handleTouchMove = React.useCallback(
    (event) => {
      const touch = event.touches && event.touches[0]
      if (!touch) return

      touchPointRef.current = { x: touch.clientX, y: touch.clientY }
      if (!touchDraggingID) return
      event.preventDefault()

      const element = document.elementFromPoint(touch.clientX, touch.clientY)
      if (element) {
        let el = element
        while (el && el !== document.body) {
          const sid = el.getAttribute && el.getAttribute('data-source-id')
          if (sid) {
            if (el.getAttribute('data-enabled') === 'true') {
              dragOverIDRef.current = sid
              setDragOverID(sid)
              const rect = el.getBoundingClientRect()
              dragPlaceAfterRef.current =
                touch.clientY > rect.top + rect.height / 2
            }
            break
          }
          el = el.parentElement
        }
      }
    },
    [touchDraggingID],
  )

  const handleTouchEnd = React.useCallback(() => {
    clearTouchLongPressTimer()
    if (dragModeRef.current === 'touch') {
      commitPointerDrag()
      return
    }
    setTouchDraggingID('')
    setDragOverID('')
  }, [clearTouchLongPressTimer, commitPointerDrag])

  React.useEffect(() => {
    window.addEventListener('mousemove', handleMouseMove)
    window.addEventListener('mouseup', handleMouseUp)
    return () => {
      window.removeEventListener('mousemove', handleMouseMove)
      window.removeEventListener('mouseup', handleMouseUp)
    }
  }, [handleMouseMove, handleMouseUp])

  React.useEffect(
    () => () => clearTouchLongPressTimer(),
    [clearTouchLongPressTimer],
  )

  const toDisplaySize = (size) => {
    const num = Number(size)
    if (!Number.isFinite(num) || num <= 0) return '0 KB'
    if (num < 1024) return `${num} B`
    if (num < 1024 * 1024) return `${(num / 1024).toFixed(1)} KB`
    return `${(num / 1024 / 1024).toFixed(1)} MB`
  }

  return (
    <div className={classes.root}>
      <Title
        title={'Navidrome - ' + translate('menu.online', { _: 'Online' })}
      />
      <div className={classes.settingsSection}>
        <div className={classes.settingsTitle}>
          <MdFolder className={classes.titleIcon} size={20} />
          <Typography variant="h6">
            {translate('online.downloadPathTitle', {
              _: 'Download Path Settings',
            })}
          </Typography>
        </div>
        <TextField
          className={classes.settingsField}
          variant="outlined"
          size="small"
          label={translate('online.downloadPathLabel', { _: 'Download Path' })}
          value={downloadPath}
          onChange={(event) => setDownloadPath(event.target.value)}
          placeholder={translate('online.downloadPathPlaceholder', {
            _: 'Enter download path',
          })}
        />
        <div className={classes.saveButtonWrap}>
          <Button
            variant="contained"
            color="primary"
            className={classes.manageButton}
            onClick={handleSaveDownloadPath}
            disabled={savingDownloadPath}
          >
            {translate('online.saveDownloadPath', { _: 'Save' })}
          </Button>
        </div>
      </div>

      <div className={classes.pageTitle}>
        <MdSettings className={classes.titleIcon} size={20} />
        <Typography variant="h6">
          {translate('online.title', {
            _: 'Custom Online Sources (Lx Source)',
          })}
        </Typography>
      </div>
      <Typography variant="body2" className={classes.subtitle}>
        {translate('online.subtitle', {
          _: 'Manage and configure third-party music scripts (lxmusic supported)',
        })}
      </Typography>

      <div className={classes.addButtonWrap}>
        <input
          ref={uploadInputRef}
          type="file"
          accept=".js,application/javascript,text/javascript"
          style={{ display: 'none' }}
          onChange={handleUploadChange}
        />
        <Button
          variant="contained"
          color="primary"
          className={classes.manageButton}
          startIcon={<MdAdd />}
          onClick={handlePickUpload}
        >
          {translate('online.manageOrAdd', { _: 'Add' })}
        </Button>
      </div>

      {displaySources.map((item) => (
        <Card
          key={item.id}
          className={`${classes.card} ${draggingID === item.id || touchDraggingID === item.id ? classes.cardDragging : ''} ${dragOverID === item.id && (draggingID || touchDraggingID) ? classes.cardDropTarget : ''}`}
          data-source-id={item.id}
          data-enabled={item.enabled ? 'true' : 'false'}
          onMouseEnter={(event) => handleCardMouseEnter(event, item)}
          onTouchStart={(event) => handleTouchStart(event, item)}
          onTouchMove={handleTouchMove}
          onTouchEnd={handleTouchEnd}
          onTouchCancel={handleTouchEnd}
        >
          <CardContent>
            <div className={classes.row}>
              <div className={classes.left}>
                <span
                  onMouseDown={(event) => handleMouseDragStart(event, item)}
                >
                  <MdDragIndicator
                    className={`${classes.dragHandle} ${!item.enabled ? classes.dragHandleDisabled : ''}`}
                    size={18}
                  />
                </span>
                <Box minWidth={0}>
                  <div className={classes.titleRow}>
                    <Typography
                      variant="subtitle1"
                      className={classes.sourceTitle}
                    >
                      {item.name}
                    </Typography>
                    {item.enabled && (
                      <Chip
                        size="small"
                        label={translate('online.enabled', { _: 'Enabled' })}
                        className={classes.statusEnabled}
                      />
                    )}
                  </div>

                  <div className={classes.sourceMeta}>
                    <span>{item.author}</span>
                    <span>{toDisplaySize(item.size)}</span>
                    <Chip
                      size="small"
                      label={item.version}
                      className={classes.tag}
                    />
                    <Chip
                      size="small"
                      icon={<MdCheckCircle size={14} />}
                      label={
                        item.status ||
                        translate('online.healthy', { _: 'Healthy' })
                      }
                      className={classes.statusHealthy}
                    />
                  </div>

                  <div className={classes.tags}>
                    {(item.supportedSources || []).map((tag) => {
                      const displayTag = sourceCodeMap[tag] || tag
                      return (
                        <Chip
                          key={tag}
                          size="small"
                          label={displayTag}
                          className={classes.tag}
                          style={tagColor(theme, displayTag)}
                        />
                      )
                    })}
                  </div>
                </Box>
              </div>

              <Box>
                <Button
                  size="small"
                  variant="outlined"
                  onClick={() => handleToggle(item)}
                >
                  {item.enabled
                    ? translate('online.disable', { _: 'Disable' })
                    : translate('online.enable', { _: 'Enable' })}
                </Button>
                <IconButton
                  className={classes.deleteBtn}
                  onClick={() => handleDelete(item)}
                >
                  <MdDeleteOutline />
                </IconButton>
              </Box>
            </div>
          </CardContent>
        </Card>
      ))}

      <Divider />
    </div>
  )
}

export default OnlineSetting
