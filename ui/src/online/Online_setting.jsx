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
        git: { bg: theme.palette.action.selected, color: theme.palette.text.primary },
        qsvip: { bg: theme.palette.action.hover, color: theme.palette.text.primary },
    }
    return map[tag] || { bg: theme.palette.action.hover, color: theme.palette.text.secondary }
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
                setDownloadPath(typeof json?.downloadPath === 'string' ? json.downloadPath : '')
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
                setDownloadPath(typeof json?.downloadPath === 'string' ? json.downloadPath : '')
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
                        body: JSON.stringify({ filename: file.name, content, allowUnsafeVM }),
                        headers: new Headers({ 'Content-Type': 'application/json' }),
                    })

                    if (json && json.success === false) {
                        if (json.disabledVM) {
                            notify(json.message || '服务器已禁用原生 VM 模式', 'warning')
                            return null
                        }
                        if (json.requireUnsafe && !allowUnsafeVM) {
                            const confirmed = window.confirm(
                                json.message || '该脚本需要原生 VM 模式运行，可能存在安全风险，是否继续？',
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
                setSources((prev) => [json, ...prev.filter((item) => item.id !== json.id)])
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

    const toDisplaySize = (size) => {
        const num = Number(size)
        if (!Number.isFinite(num) || num <= 0) return '0 KB'
        if (num < 1024) return `${num} B`
        if (num < 1024 * 1024) return `${(num / 1024).toFixed(1)} KB`
        return `${(num / 1024 / 1024).toFixed(1)} MB`
    }

    return (
        <div className={classes.root}>
            <Title title={'Navidrome - ' + translate('menu.online', { _: 'Online' })} />
            <div className={classes.settingsSection}>
                <div className={classes.settingsTitle}>
                    <MdFolder className={classes.titleIcon} size={20} />
                    <Typography variant="h6">
                        {translate('online.downloadPathTitle', { _: '下载路径设置' })}
                    </Typography>
                </div>
                <TextField
                    className={classes.settingsField}
                    variant="outlined"
                    size="small"
                    label={translate('online.downloadPathLabel', { _: '下载路径' })}
                    value={downloadPath}
                    onChange={(event) => setDownloadPath(event.target.value)}
                    placeholder={translate('online.downloadPathPlaceholder', { _: '请输入下载路径' })}
                />
                <div className={classes.saveButtonWrap}>
                    <Button
                        variant="contained"
                        color="primary"
                        className={classes.manageButton}
                        onClick={handleSaveDownloadPath}
                        disabled={savingDownloadPath}
                    >
                        {translate('online.saveDownloadPath', { _: '保存' })}
                    </Button>
                </div>
            </div>

            <div className={classes.pageTitle}>
                <MdSettings className={classes.titleIcon} size={20} />
                <Typography variant="h6">
                    {translate('online.title', { _: '自定义在线源(Lx Source)' })}
                </Typography>
            </div>
            <Typography variant="body2" className={classes.subtitle}>
                {translate('online.subtitle', { _: '管理和配置第三方音乐脚本,支持lxmusic' })}
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
                    {translate('online.manageOrAdd', { _: '添加' })}
                </Button>
            </div>

            {sources.map((item) => (
                <Card key={item.id} className={classes.card}>
                    <CardContent>
                        <div className={classes.row}>
                            <div className={classes.left}>
                                <MdDragIndicator className={classes.dragHandle} size={18} />
                                <Box minWidth={0}>
                                    <div className={classes.row}>
                                        <Typography variant="subtitle1" className={classes.sourceTitle}>
                                            {item.name}
                                        </Typography>
                                        <Box>
                                            {item.enabled && (
                                                <Chip
                                                    size="small"
                                                    label={translate('online.enabled', { _: '已启用' })}
                                                    className={classes.statusEnabled}
                                                />
                                            )}
                                        </Box>
                                    </div>

                                    <div className={classes.sourceMeta}>
                                        <span>{item.author}</span>
                                        <span>{toDisplaySize(item.size)}</span>
                                        <Chip size="small" label={item.version} className={classes.tag} />
                                        <Chip
                                            size="small"
                                            icon={<MdCheckCircle size={14} />}
                                            label={item.status || translate('online.healthy', { _: '正常' })}
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
                                        ? translate('online.disable', { _: '禁用' })
                                        : translate('online.enable', { _: '启用' })}
                                </Button>
                                <IconButton className={classes.deleteBtn} onClick={() => handleDelete(item)}>
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
