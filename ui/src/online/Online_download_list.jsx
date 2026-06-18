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
    flexWrap: 'nowrap',
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
  },
  actionBtn: {
    textTransform: 'none',
    borderRadius: 999,
    color: theme.palette.text.secondary,
    borderColor: theme.palette.divider,
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

const statusLabel = {
  queued: '排队中',
  resolving: '解析中',
  downloading: '下载中',
  completed: '已完成',
  failed: '失败',
  paused: '已暂停',
  canceled: '已取消',
  syncing: '同步中',
  'sync-completed': '已完成',
  'sync-error': '有错误',
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
// name (e.g. "ikun[赞助]…") instead of the static source code ("wy"),
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

const formatInFlightLabel = (task) => {
  const candidate = truncateSourceName(task?.sourceName || task?.source || '')
  if (!candidate) return ''
  if (task.status === 'resolving') return `${candidate} 解析中...`
  if (task.status === 'downloading') {
    const p = Math.max(0, Math.min(100, Number(task?.progress) || 0))
    return p > 0 ? `${candidate} 下载中 ${p}%` : `${candidate} 下载中...`
  }
  if (task.status === 'queued') return `${candidate} 排队中`
  return candidate
}

const formatPlaylistSyncSubline = (task) => {
  const songTitle = String(task?.currentSongTitle || task?.artist || '未知歌曲').trim()
  const sourceLabel = truncateSourceName(
    String(task?.sourceName || task?.source || '未知源').trim(),
    5,
  )
  if (task.status === 'syncing') return `${songTitle} ${sourceLabel} 同步中...`.trim()
  if (task.status === 'resolving') return `${songTitle} ${sourceLabel} 解析中...`.trim()
  if (task.status === 'downloading') return `${songTitle} ${sourceLabel} 下载中...`.trim()
  if (task.status === 'queued') return `${songTitle} ${sourceLabel} 排队中`
  if (task.status === 'paused') return `${songTitle} 已暂停`
  if (task.status === 'sync-error') return `${songTitle} ${sourceLabel} 失败`
  if (task.status === 'sync-completed') return `${songTitle} ${sourceLabel} 已完成`
  return `${songTitle} ${sourceLabel}`.trim()
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
                下载管理
              </Typography>
              <Typography className={classes.subtitle}>
                {totalSpeed} • {displayTasks.length} TASKS
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
              <Box className={classes.empty}>暂无下载任务</Box>
            )}

            {sortedTasks.map((task) => (
              (() => {
                const isPlaylistSyncTask = task.taskType === 'playlist_sync'
                const isToggleable = !isPlaylistSyncTask

                return (
                  <Box
                    key={task.id}
                    className={classes.taskCard}
                    onClick={isToggleable ? () => onToggleTask && onToggleTask(task.id) : undefined}
                    style={{ cursor: isToggleable ? 'pointer' : 'default' }}
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
                                {formatPlaylistSyncSubline(task)}
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
                                formatInFlightLabel(task)
                                ? `${formatInFlightLabel(task)} · ${task.quality} · ${task.artist}`
                                : `${task.source} · ${task.quality} · ${task.artist}`}
                            </Typography>
                          </>
                        )}
                      </Box>

                      <Box className={classes.rightMeta}>
                        <Chip
                          size="small"
                          label={statusLabel[getDisplayStatus(task)] || '未知'}
                          style={{
                            backgroundColor:
                              taskStatusColorMap[getDisplayStatus(task)] || '#999',
                            color: 'white',
                          }}
                        />
                        {task.taskType === 'playlist_sync' && (
                          <Typography className={classes.remainText}>
                            {`剩余: ${Math.max(0, Number(task.remainingCount) || 0)}首`}
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
      title: PropTypes.string,
      currentSongTitle: PropTypes.string,
      remainingCount: PropTypes.number,
      cover: PropTypes.string,
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
