import React from 'react'
import PropTypes from 'prop-types'
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
        flexWrap: 'wrap',
    },
    summaryText: {
        color: theme.palette.primary.main,
        fontWeight: 700,
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

const defaultTasks = [
    {
        id: 'demo-task-1',
        title: '海屿你',
        artist: '马也_Crabbit',
        source: '网易',
        quality: '标准',
        status: 'completed',
        progress: 100,
    },
]

const statusLabel = {
    queued: '排队中',
    resolving: '解析中',
    downloading: '下载中',
    completed: '已完成',
    failed: '失败',
}

const statusColor = {
    queued: 'default',
    resolving: 'secondary',
    downloading: 'primary',
    completed: 'primary',
    failed: 'secondary',
}

const DownloadList = ({
    open,
    onClose,
    tasks = defaultTasks,
    totalSpeed = '0 B/s',
}) => {
    const classes = useStyles()

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
                                {totalSpeed} • {tasks.length} TASKS
                            </Typography>
                        </Box>
                    </Box>

                    <Box className={classes.summary}>
                        <Typography className={classes.summaryText}>总进度: 100%</Typography>
                        <Box className={classes.actionRow}>
                            <Button variant="outlined" size="small" className={classes.actionBtn}>
                                全部重试
                            </Button>
                            <Button variant="outlined" size="small" className={classes.actionBtn}>
                                全部取消
                            </Button>
                            <Button variant="outlined" size="small" className={classes.actionBtn}>
                                清空已完成
                            </Button>
                        </Box>
                    </Box>

                    <Divider />

                    <Box className={classes.taskList}>
                        {tasks.length === 0 && (
                            <Box className={classes.empty}>暂无下载任务</Box>
                        )}

                        {tasks.map((task) => (
                            <Box key={task.id} className={classes.taskCard}>
                                <Box className={classes.taskHead}>
                                    <Typography className={classes.taskName}>{task.title}</Typography>
                                    <Chip
                                        size="small"
                                        label={statusLabel[task.status] || '未知'}
                                        color={statusColor[task.status] || 'default'}
                                    />
                                </Box>

                                <Typography className={classes.taskMeta}>
                                    {task.source} · {task.quality} · {task.artist}
                                </Typography>

                                {task.status !== 'completed' && (
                                    <Box className={classes.progressWrap}>
                                        <LinearProgress
                                            className={classes.progress}
                                            variant="determinate"
                                            value={Math.max(0, Math.min(100, task.progress || 0))}
                                        />
                                    </Box>
                                )}
                            </Box>
                        ))}
                    </Box>
                </Paper>
            </Box>
        </Fade>
    )
}

DownloadList.propTypes = {
    onClose: PropTypes.func,
    open: PropTypes.bool,
    tasks: PropTypes.arrayOf(
        PropTypes.shape({
            artist: PropTypes.string,
            id: PropTypes.string.isRequired,
            progress: PropTypes.number,
            quality: PropTypes.string,
            source: PropTypes.string,
            status: PropTypes.string,
            title: PropTypes.string,
        }),
    ),
    totalSpeed: PropTypes.string,
}

DownloadList.defaultProps = {
    onClose: () => { },
    open: false,
    tasks: defaultTasks,
    totalSpeed: '0 B/s',
}

export default DownloadList
