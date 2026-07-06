import React from 'react'
import PropTypes from 'prop-types'
import { useTranslate } from 'react-admin'
import {
  Box,
  Fade,
  Paper,
  Typography,
  Chip,
  Divider,
  LinearProgress,
  Button,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  List,
  ListItem,
  ListItemText,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'

const useStyles = makeStyles((theme) => ({
  wrapper: {
    position: 'fixed',
    inset: 0,
    pointerEvents: 'none',
    zIndex: 1400,
  },
  backdrop: {
    position: 'absolute',
    inset: 0,
    background: 'rgba(0, 0, 0, 0.28)',
    pointerEvents: 'auto',
  },
  panel: {
    position: 'absolute',
    top: 64,
    right: 12,
    width: 'min(460px, calc(100vw - 20px))',
    maxHeight: 'calc(100vh - 82px)',
    borderRadius: 14,
    overflow: 'hidden',
    pointerEvents: 'auto',
    border: `1px solid ${theme.palette.divider}`,
    background: theme.palette.background.paper,
    color: theme.palette.text.primary,
    display: 'flex',
    flexDirection: 'column',
    [theme.breakpoints.down('sm')]: {
      top: 56,
      right: 8,
      width: 'calc(100vw - 16px)',
      maxHeight: 'calc(100vh - 66px)',
    },
  },
  header: {
    padding: theme.spacing(1.6, 2),
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'flex-start',
    borderBottom: `1px solid ${theme.palette.divider}`,
    background: theme.palette.action.hover,
  },
  titleWrap: {
    minWidth: 0,
  },
  title: {
    fontWeight: 700,
    letterSpacing: 0.2,
  },
  subtitle: {
    marginTop: theme.spacing(0.4),
    fontSize: '0.8rem',
    color: theme.palette.text.secondary,
  },
  summary: {
    padding: theme.spacing(1.2, 2),
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    gap: theme.spacing(1),
    flexWrap: 'wrap',
  },
  summaryText: {
    color: theme.palette.primary.main,
    fontWeight: 700,
    fontSize: '0.85rem',
    whiteSpace: 'nowrap',
  },
  actionRow: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    flexWrap: 'wrap',
    justifyContent: 'flex-end',
    minWidth: 0,
  },
  actionBtn: {
    textTransform: 'none',
    borderRadius: 999,
    color: theme.palette.text.secondary,
    borderColor: theme.palette.divider,
    whiteSpace: 'nowrap',
    paddingLeft: theme.spacing(1.2),
    paddingRight: theme.spacing(1.2),
    [theme.breakpoints.down('sm')]: {
      fontSize: '0.75rem',
      paddingLeft: theme.spacing(1),
      paddingRight: theme.spacing(1),
    },
  },
  taskList: {
    overflowY: 'auto',
    padding: theme.spacing(1.2, 1.2, 1.5),
    display: 'flex',
    flexDirection: 'column',
    gap: theme.spacing(1),
  },
  taskCard: {
    borderRadius: 12,
    border: `1px solid ${theme.palette.divider}`,
    background: theme.palette.action.hover,
    padding: theme.spacing(1.2),
  },
  taskCardContent: {
    display: 'flex',
    alignItems: 'flex-start',
    justifyContent: 'space-between',
    gap: theme.spacing(1),
  },
  taskMain: {
    minWidth: 0,
    flex: 1,
  },
  taskHead: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: theme.spacing(1),
  },
  taskName: {
    fontWeight: 600,
    color: theme.palette.text.primary,
  },
  taskMeta: {
    marginTop: theme.spacing(0.5),
    color: theme.palette.text.secondary,
    fontSize: '0.8rem',
  },
  playlistSyncBody: {
    display: 'flex',
    alignItems: 'flex-start',
    gap: theme.spacing(1),
    minWidth: 0,
  },
  playlistCover: {
    width: 44,
    height: 44,
    borderRadius: 8,
    flexShrink: 0,
    backgroundColor: theme.palette.action.selected,
    backgroundSize: 'cover',
    backgroundPosition: 'center',
    backgroundRepeat: 'no-repeat',
  },
  playlistSyncTextWrap: {
    minWidth: 0,
    flex: 1,
  },
  rightMeta: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'flex-end',
    gap: theme.spacing(0.45),
    flexShrink: 0,
  },
  remainText: {
    fontSize: '0.75rem',
    color: theme.palette.text.secondary,
    whiteSpace: 'nowrap',
  },
  failedReasonText: {
    fontSize: '0.75rem',
    color: theme.palette.text.secondary,
    maxWidth: 220,
    textAlign: 'right',
    marginTop: 2,
    display: 'block',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  progressWrap: {
    marginTop: theme.spacing(1),
  },
  progress: {
    height: 6,
    borderRadius: 999,
    backgroundColor: theme.palette.action.selected,
    '& .MuiLinearProgress-barColorPrimary': {
      backgroundColor: theme.palette.primary.main,
    },
  },
  empty: {
    padding: theme.spacing(4, 2),
    textAlign: 'center',
    color: theme.palette.text.secondary,
  },
}))

const defaultTasks = []

const statusLabelKeys = {
  queued: 'online.download.status.queued',
  resolving: 'online.download.status.resolving',
  downloading: 'online.download.status.downloading',
  completed: 'online.download.status.completed',
  failed: 'online.download.status.failed',
  paused: 'online.download.status.paused',
  canceled: 'online.download.status.canceled',
  syncing: 'online.download.status.syncing',
  'sync-completed': 'online.download.status.completed',
  'sync-error': 'online.download.status.error',
}

const statusColor = {
  queued: 'default',
  resolving: 'secondary',
  downloading: 'primary',
  completed: 'primary',
  failed: 'secondary',
  paused: 'default',
  canceled: 'default',
}

const taskStatusColorMap = {
  queued: '#9c27b0',
  resolving: '#ff9800',
  downloading: '#2196f3',
  completed: '#4caf50',
  failed: '#f44336',
  paused: '#fbc02d',
  canceled: '#9e9e9e',
  syncing: '#2196f3',
  'sync-completed': '#4caf50',
  'sync-error': '#ff9800',
}

const getDisplayStatus = (task) => {
  if (task?.taskType === 'playlist_sync' && task?.status === 'paused') {
    return 'canceled'
  }
  return task?.status
}

// While a server download is in flight we show the *resolver script*
// name (e.g. "ikun[sponsor]…") instead of the static source code ("wy"),
// because the user can see which candidate is currently being tried or
// is being downloaded. Falls back to the source code when the script
// name is missing (e.g. native wy/tx/kg/kw/mg paths, or pre-upgrade
// tasks that pre-date the SourceName field).
const inFlightStatuses = new Set(['queued', 'resolving', 'downloading', 'syncing'])

const truncateSourceName = (name, max = 5) => {
  if (!name) return ''
  if (name.length <= max) return name
  return `${name.slice(0, max)}...`
}

const formatInFlightLabel = (task, translate) => {
  const candidate = truncateSourceName(task?.sourceName || task?.source || '')
  if (!candidate) return ''
  if (task.status === 'resolving') return `${candidate} ${translate('online.download.status.resolving', { _: 'Resolving' })}...`
  if (task.status === 'downloading') {
    const p = Math.max(0, Math.min(100, Number(task?.progress) || 0))
    return p > 0 ? `${candidate} ${translate('online.download.status.downloading', { _: 'Downloading' })} ${p}%` : `${candidate} ${translate('online.download.status.downloading', { _: 'Downloading' })}...`
  }
  if (task.status === 'queued') return `${candidate} ${translate('online.download.status.queued', { _: 'Queued' })}`
  return candidate
}

const formatPlaylistSyncSubline = (task, translate) => {
  const songTitle = String(task?.currentSongTitle || task?.artist || translate('online.download.unknownSong', { _: 'Unknown song' })).trim()
  if (task?.currentSongReused) {
    return `${songTitle} ${translate('online.download.reusedFromLibrary', { _: 'reused from library' })}`
  }
  const sourceLabel = truncateSourceName(
    String(task?.sourceName || task?.source || translate('online.download.unknownSource', { _: 'Unknown source' })).trim(),
    5,
  )
  if (task.status === 'syncing') return `${songTitle} ${sourceLabel} ${translate('online.download.status.syncing', { _: 'Syncing' })}...`.trim()
  if (task.status === 'resolving') return `${songTitle} ${sourceLabel} ${translate('online.download.status.resolving', { _: 'Resolving' })}...`.trim()
  if (task.status === 'downloading') return `${songTitle} ${sourceLabel} ${translate('online.download.status.downloading', { _: 'Downloading' })}...`.trim()
  if (task.status === 'queued') return `${songTitle} ${sourceLabel} ${translate('online.download.status.queued', { _: 'Queued' })}`
  if (task.status === 'paused') return `${songTitle} ${translate('online.download.status.paused', { _: 'Paused' })}`
  if (task.status === 'sync-error') return `${songTitle} ${sourceLabel} ${translate('online.download.status.failed', { _: 'Failed' })}`
  if (task.status === 'sync-completed') return `${songTitle} ${sourceLabel} ${translate('online.download.status.completed', { _: 'Completed' })}`
  return `${songTitle} ${sourceLabel}`.trim()
}

const translateFailureReason = (reason, translate) => {
  const code = String(reason || '').trim()
  if (!code) return translate('online.error.unknown', { _: 'Unknown error' })
  if (code.startsWith('online.')) {
    return translate(code, { _: code })
  }
  return code
}

const normalizeFailedSongDetails = (task, translate) => {
  const details = Array.isArray(task?.failedSongDetails)
    ? task.failedSongDetails
    : []
  if (details.length > 0) {
    return details
      .map((item) => ({
        name: String(item?.name || '').trim(),
        singer: String(item?.singer || '').trim() || translate('online.download.unknownArtist', { _: 'Unknown artist' }),
        reason: translateFailureReason(item?.reason, translate),
      }))
      .filter((item) => item.name)
  }
  const names = Array.isArray(task?.failedSongs) ? task.failedSongs : []
  return names
    .map((name) => ({
      name: String(name || '').trim(),
      singer: translate('online.download.unknownArtist', { _: 'Unknown artist' }),
      reason: translate('online.error.unknown', { _: 'Unknown error' }),
    }))
    .filter((item) => item.name)
}

const formatSingleTaskFailedReason = (task, displayStatus, translate) => {
  if (task?.taskType === 'playlist_sync' || displayStatus !== 'failed') return ''
  const reason = String(task?.error || task?.reason || task?.message || '').trim()
  if (!reason) return `${translate('online.download.reasonLabel', { _: 'Reason' })}: ${translate('online.error.download_source_failed', { _: 'Download source failed' })}`
  return `${translate('online.download.reasonLabel', { _: 'Reason' })}: ${translateFailureReason(reason.replace(/\s+/g, ' '), translate)}`
}

const DownloadList = ({
  open,
  onClose,
  tasks = defaultTasks,
  totalSpeed = '0 B/s',
  totalProgress = 0,
  onRetryAll,
  onCancelAll,
  onClearCompleted,
  onClearFailed,
  onToggleTask,
}) => {
  const classes = useStyles()
  const translate = useTranslate()
  const taskOrderRef = React.useRef(new Map())
  const nextOrderRef = React.useRef(1)
  const [failedTask, setFailedTask] = React.useState(null)

  const failedRows = React.useMemo(() => normalizeFailedSongDetails(failedTask, translate), [failedTask, translate])

  const handleDownloadFailedList = React.useCallback(() => {
    if (!failedTask) return
    const rows = normalizeFailedSongDetails(failedTask, translate)
    const title = String(failedTask?.title || translate('online.download.playlistSyncTask', { _: 'Playlist sync task' })).trim()
    const lines = [
      `${translate('online.download.failedListTitle', { _: 'Playlist sync failed list' })}`,
      `${translate('online.download.taskName', { _: 'Task' })}: ${title}`,
      `${translate('online.download.exportTime', { _: 'Export time' })}: ${new Date().toLocaleString()}`,
      '',
      `${translate('online.download.index', { _: 'No.' })}\t${translate('online.download.songName', { _: 'Song' })}\t${translate('online.download.artist', { _: 'Artist' })}\t${translate('online.download.failedReason', { _: 'Reason' })}`,
      ...rows.map((row, idx) => `${idx + 1}\t${row.name}\t${row.singer || translate('online.download.unknownArtist', { _: 'Unknown artist' })}\t${row.reason || translate('online.error.unknown', { _: 'Unknown error' })}`),
    ]
    const txt = lines.join('\n')
    const blob = new Blob([txt], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const safeTitle = title.replace(/[\\/:*?"<>|]+/g, '_').slice(0, 60) || 'playlist_sync'
    const a = document.createElement('a')
    a.href = url
    a.download = `${safeTitle}_failed_songs.txt`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  }, [failedTask, translate])

  const displayTasks = React.useMemo(() => {
    return Array.isArray(tasks) ? tasks : []
  }, [tasks])

  const calculateTotalProgress = () => {
    if (!displayTasks || displayTasks.length === 0) return 0
    const totalProgress = displayTasks.reduce(
      (sum, task) => sum + (task.progress || 0),
      0,
    )
    return Math.round(totalProgress / displayTasks.length)
  }

  // Keep a stable insertion rank for each task ID so polling does not reshuffle items.
  React.useEffect(() => {
    if (!displayTasks || displayTasks.length === 0) {
      taskOrderRef.current.clear()
      nextOrderRef.current = 1
      return
    }

    const visibleIDs = new Set(displayTasks.map((task) => task.id))
    taskOrderRef.current.forEach((_, id) => {
      if (!visibleIDs.has(id)) {
        taskOrderRef.current.delete(id)
      }
    })

    displayTasks.forEach((task) => {
      if (!taskOrderRef.current.has(task.id)) {
        taskOrderRef.current.set(task.id, nextOrderRef.current)
        nextOrderRef.current += 1
      }
    })
  }, [displayTasks])

  // Sort tasks: active tasks first, and newest inserted first inside each group.
  const sortedTasks = React.useMemo(() => {
    if (!displayTasks || displayTasks.length === 0) return []

    const activeTasks = []
    const completedTasks = []

    displayTasks.forEach((task) => {
      const stableOrder = taskOrderRef.current.get(task.id) || 0
      if (task.status === 'completed') {
        completedTasks.push({ ...task, _stableOrder: stableOrder })
      } else {
        activeTasks.push({ ...task, _stableOrder: stableOrder })
      }
    })

    activeTasks.sort((a, b) => b._stableOrder - a._stableOrder)
    completedTasks.sort((a, b) => b._stableOrder - a._stableOrder)

    return [...activeTasks, ...completedTasks]
  }, [displayTasks])

  return (
    <Fade in={open} timeout={180}>
      <Box className={classes.wrapper}>
        <Box className={classes.backdrop} onClick={onClose} />
        <Paper className={classes.panel} elevation={12}>
          <Box className={classes.header}>
            <Box className={classes.titleWrap}>
              <Typography variant="h6" className={classes.title}>
                {translate('online.download.title', { _: 'Downloads' })}
              </Typography>
              <Typography className={classes.subtitle}>
                {totalSpeed} • {displayTasks.length} {translate('online.download.tasksSuffix', { _: 'tasks' })}
              </Typography>
            </Box>
          </Box>

          <Box className={classes.summary}>
            <Typography className={classes.summaryText}>
              {translate('downloadList.totalProgress', { _: 'Total Progress' })}
              : {calculateTotalProgress()}%
            </Typography>
            <Box className={classes.actionRow} style={{ flex: 'none' }}>
              <Button
                variant="outlined"
                size="small"
                className={classes.actionBtn}
                onClick={onRetryAll}
              >
                {translate('downloadList.retryAll', { _: 'Retry All' })}
              </Button>
              <Button
                variant="outlined"
                size="small"
                className={classes.actionBtn}
                onClick={onCancelAll}
              >
                {translate('downloadList.cancelAll', { _: 'Cancel All' })}
              </Button>
              <Button
                variant="outlined"
                size="small"
                className={classes.actionBtn}
                onClick={onClearCompleted}
              >
                {translate('downloadList.clearCompleted', {
                  _: 'Clear Completed',
                })}
              </Button>
              <Button
                variant="outlined"
                size="small"
                className={classes.actionBtn}
                onClick={onClearFailed}
              >
                {translate('downloadList.clearFailed', { _: 'Clear Failed' })}
              </Button>
            </Box>
          </Box>

          <Divider />

          <Box className={classes.taskList}>
            {displayTasks.length === 0 && (
              <Box className={classes.empty}>{translate('online.download.empty', { _: 'No download tasks' })}</Box>
            )}

            {sortedTasks.map((task) => (
              (() => {
                const isPlaylistSyncTask = task.taskType === 'playlist_sync'
                const isFailedPlaylistSyncTask =
                  isPlaylistSyncTask && task.status === 'sync-error'
                const isToggleable = !isPlaylistSyncTask
                const failedCount = normalizeFailedSongDetails(task, translate).length
                const displayStatus = getDisplayStatus(task)
                const singleTaskFailedReason = formatSingleTaskFailedReason(task, displayStatus, translate)

                const handleTaskClick = () => {
                  if (isFailedPlaylistSyncTask) {
                    setFailedTask(task)
                    return
                  }
                  if (isToggleable && onToggleTask) {
                    onToggleTask(task.id)
                  }
                }

                return (
                  <Box
                    key={task.id}
                    className={classes.taskCard}
                    onClick={isToggleable || isFailedPlaylistSyncTask ? handleTaskClick : undefined}
                    style={{ cursor: isToggleable || isFailedPlaylistSyncTask ? 'pointer' : 'default' }}
                  >
                    <Box className={classes.taskCardContent}>
                      <Box className={classes.taskMain}>
                        {isPlaylistSyncTask ? (
                          <Box className={classes.playlistSyncBody}>
                            <Box
                              className={classes.playlistCover}
                              style={
                                task.cover
                                  ? { backgroundImage: `url(${task.cover})` }
                                  : undefined
                              }
                            />
                            <Box className={classes.playlistSyncTextWrap}>
                              <Typography className={classes.taskName} noWrap title={task.title}>
                                {task.title}
                              </Typography>
                              <Typography className={classes.taskMeta} noWrap>
                                {formatPlaylistSyncSubline(task, translate)}
                              </Typography>
                            </Box>
                          </Box>
                        ) : (
                          <>
                            <Box className={classes.taskHead}>
                              <Typography className={classes.taskName} noWrap title={task.title}>
                                {task.title}
                              </Typography>
                            </Box>

                            <Typography className={classes.taskMeta} noWrap>
                              {inFlightStatuses.has(task.status) &&
                                formatInFlightLabel(task, translate)
                                ? `${formatInFlightLabel(task, translate)} · ${task.quality} · ${task.artist}`
                                : `${task.source} · ${task.quality} · ${task.artist}`}
                            </Typography>
                          </>
                        )}
                      </Box>

                      <Box className={classes.rightMeta}>
                        <Chip
                          size="small"
                          label={translate(statusLabelKeys[displayStatus] || 'online.error.unknown', { _: 'Unknown' })}
                          style={{
                            backgroundColor:
                              taskStatusColorMap[displayStatus] || '#999',
                            color: 'white',
                          }}
                        />
                        {task.taskType === 'playlist_sync' && (
                          <Typography className={classes.remainText}>
                            {task.status === 'sync-error'
                              ? `${translate('online.download.failedCount', { _: 'Failed' })}: ${failedCount}`
                              : `${translate('online.download.remaining', { _: 'Remaining' })}: ${Math.max(0, Number(task.remainingCount) || 0)}`}
                          </Typography>
                        )}
                        {!!singleTaskFailedReason && (
                          <Typography
                            className={classes.failedReasonText}
                            title={singleTaskFailedReason}
                          >
                            {singleTaskFailedReason}
                          </Typography>
                        )}
                      </Box>
                    </Box>

                    <Box className={classes.progressWrap}>
                      <LinearProgress
                        className={classes.progress}
                        variant="determinate"
                        value={Math.max(0, Math.min(100, task.progress || 0))}
                      />
                    </Box>
                  </Box>
                )
              })()
            ))}
          </Box>

          <Dialog
            open={Boolean(failedTask)}
            onClose={() => setFailedTask(null)}
            fullWidth
            maxWidth="sm"
          >
            <DialogTitle>
              {translate('online.download.failedListTitle', { _: 'Playlist sync failed list' })}
            </DialogTitle>
            <DialogContent dividers>
              <Typography variant="body2" color="textSecondary" gutterBottom>
                {`${translate('online.download.taskName', { _: 'Task' })}: ${String(failedTask?.title || translate('online.download.unknownTask', { _: 'Unknown task' }))}`}
              </Typography>
              {failedRows.length === 0 ? (
                <Typography variant="body2" color="textSecondary">
                  {translate('online.download.noFailedSongs', { _: 'No failed song details' })}
                </Typography>
              ) : (
                <List dense>
                  {failedRows.map((row, index) => (
                    <ListItem key={`${row.name}-${row.singer}-${index}`} divider>
                      <ListItemText
                        primary={`${index + 1}. ${row.name}`}
                        secondary={`${translate('online.download.artist', { _: 'Artist' })}: ${row.singer || translate('online.download.unknownArtist', { _: 'Unknown artist' })} · ${translate('online.download.failedReason', { _: 'Reason' })}: ${row.reason || translate('online.error.unknown', { _: 'Unknown error' })}`}
                      />
                    </ListItem>
                  ))}
                </List>
              )}
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setFailedTask(null)}>
                {translate('ra.action.close', { _: 'Close' })}
              </Button>
              <Button
                color="primary"
                variant="contained"
                onClick={handleDownloadFailedList}
                disabled={failedRows.length === 0}
              >
                {translate('online.download.exportFailedList', { _: 'Export failed list (.txt)' })}
              </Button>
            </DialogActions>
          </Dialog>
        </Paper>
      </Box>
    </Fade>
  )
}

DownloadList.propTypes = {
  onCancelAll: PropTypes.func,
  onClearCompleted: PropTypes.func,
  onClearFailed: PropTypes.func,
  onClose: PropTypes.func,
  onRetryAll: PropTypes.func,
  onToggleTask: PropTypes.func,
  open: PropTypes.bool,
  tasks: PropTypes.arrayOf(
    PropTypes.shape({
      artist: PropTypes.string,
      id: PropTypes.string.isRequired,
      taskType: PropTypes.string,
      progress: PropTypes.number,
      quality: PropTypes.string,
      source: PropTypes.string,
      sourceName: PropTypes.string,
      status: PropTypes.string,
      error: PropTypes.string,
      title: PropTypes.string,
      currentSongTitle: PropTypes.string,
      currentSongReused: PropTypes.bool,
      remainingCount: PropTypes.number,
      cover: PropTypes.string,
      failedSongs: PropTypes.arrayOf(PropTypes.string),
      failedSongDetails: PropTypes.arrayOf(
        PropTypes.shape({
          name: PropTypes.string,
          singer: PropTypes.string,
          reason: PropTypes.string,
        }),
      ),
    }),
  ),
  totalProgress: PropTypes.number,
  totalSpeed: PropTypes.string,
}

DownloadList.defaultProps = {
  onCancelAll: () => { },
  onClearCompleted: () => { },
  onClearFailed: () => { },
  onClose: () => { },
  onRetryAll: () => { },
  onToggleTask: () => { },
  open: false,
  tasks: defaultTasks,
  totalProgress: 0,
  totalSpeed: '0 B/s',
}

export default DownloadList
