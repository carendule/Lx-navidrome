import React, { useState } from 'react'
import PropTypes from 'prop-types'
import { useSelector } from 'react-redux'
import {
    Button,
    useDataProvider,
    useNotify,
    useRefresh,
    useTranslate,
    useUnselectAll,
} from 'react-admin'
import {
    Dialog,
    DialogTitle,
    DialogContent,
    DialogActions,
    List,
    ListItem,
    ListItemText,
    LinearProgress,
    Typography,
    Box,
    Button as MuiButton,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import DeleteIcon from '@material-ui/icons/Delete'

const useStyles = makeStyles((theme) => ({
    dialogPaper: {
        height: '20em',
        maxHeight: '20em',
    },
    dialogContent: {
        overflowY: 'auto',
    },
    progressContainer: {
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'center',
    },
    progressText: {
        marginBottom: '1em',
        textAlign: 'center',
    },
    highlight: {
        color: theme.palette.error.main,
    },
}))

// Splits a "{{token}}" templated translation and renders the named values in red
const renderImpactText = (template, values, highlightClass) => {
    const regex = /{{(\w+)}}/g
    const parts = []
    let lastIndex = 0
    let match
    let key = 0
    while ((match = regex.exec(template)) !== null) {
        if (match.index > lastIndex) {
            parts.push(template.slice(lastIndex, match.index))
        }
        parts.push(
            <span key={key++} className={highlightClass}>
                {values[match[1]]}
            </span>,
        )
        lastIndex = regex.lastIndex
    }
    if (lastIndex < template.length) {
        parts.push(template.slice(lastIndex))
    }
    return parts
}

// Runs `worker` over `items` with at most `limit` calls in flight at once,
// so a large selection doesn't fire hundreds of simultaneous requests
const runWithConcurrency = async (items, limit, worker) => {
    let index = 0
    const runners = Array.from(
        { length: Math.min(limit, items.length) },
        async () => {
            while (index < items.length) {
                const current = index++
                await worker(items[current])
            }
        },
    )
    await Promise.all(runners)
}

// Steps: null (closed) -> 'list' -> 'analyzing' -> 'impact'
const STEP_LIST = 'list'
const STEP_ANALYZING = 'analyzing'
const STEP_IMPACT = 'impact'
const PLAYLIST_LOOKUP_CONCURRENCY = 6

export const DeleteSongsButton = ({ resource, selectedIds, className }) => {
    const classes = useStyles()
    const translate = useTranslate()
    const dataProvider = useDataProvider()
    const notify = useNotify()
    const refresh = useRefresh()
    const unselectAll = useUnselectAll()
    const songs = useSelector((state) => state.admin.resources.song?.data)
    const [step, setStep] = useState(null)
    const [progress, setProgress] = useState({ current: 0, total: 0 })
    const [impacts, setImpacts] = useState([])
    const [deleting, setDeleting] = useState(false)

    const reset = () => {
        setStep(null)
        setProgress({ current: 0, total: 0 })
        setImpacts([])
        setDeleting(false)
    }

    const handleClick = () => setStep(STEP_LIST)
    const handleClose = () => {
        if (deleting) return
        reset()
    }

    const handleAnalyze = async () => {
        setStep(STEP_ANALYZING)

        const albumIds = [
            ...new Set(selectedIds.map((id) => songs?.[id]?.albumId).filter(Boolean)),
        ]
        const total = albumIds.length + selectedIds.length
        let current = 0
        setProgress({ current, total })
        const bump = () => setProgress({ current: ++current, total })

        const albumImpacts = []
        if (albumIds.length > 0) {
            try {
                // Single batched request: album.songCount is already denormalized,
                // so we avoid one song-count query per album.
                const { data: albumRecords } = await dataProvider.getMany('album', {
                    ids: albumIds,
                })
                const albumById = new Map(albumRecords.map((a) => [a.id, a]))
                albumIds.forEach((albumId) => {
                    const albumSongIds = selectedIds.filter(
                        (id) => songs?.[id]?.albumId === albumId,
                    )
                    const album = albumById.get(albumId)
                    albumImpacts.push({
                        type: 'album',
                        id: albumId,
                        name: album?.name ?? songs?.[albumSongIds[0]]?.album ?? albumId,
                        empty:
                            (album?.songCount ?? albumSongIds.length) <=
                            albumSongIds.length,
                        songTitles: albumSongIds.map((id) => songs?.[id]?.title ?? id),
                    })
                    bump()
                })
            } catch (error) {
                // eslint-disable-next-line no-console
                console.error('Failed to check album impact:', error)
                albumIds.forEach(() => bump())
            }
        }

        const playlistCounts = new Map()
        await runWithConcurrency(
            selectedIds,
            PLAYLIST_LOOKUP_CONCURRENCY,
            async (id) => {
                try {
                    const { data } = await dataProvider.getPlaylists(id)
                    data.forEach((pl) => {
                        const entry = playlistCounts.get(pl.id) || {
                            name: pl.name,
                            songCount: pl.songCount,
                            songIds: [],
                        }
                        entry.songIds.push(id)
                        playlistCounts.set(pl.id, entry)
                    })
                } catch (error) {
                    // eslint-disable-next-line no-console
                    console.error('Failed to check playlist impact:', error)
                } finally {
                    bump()
                }
            },
        )

        const playlistImpacts = Array.from(playlistCounts.entries()).map(
            ([id, { name, songCount, songIds }]) => ({
                type: 'playlist',
                id,
                name,
                empty: songCount <= songIds.length,
                songTitles: songIds.map((songId) => songs?.[songId]?.title ?? songId),
            }),
        )

        setImpacts([...albumImpacts, ...playlistImpacts])
        setStep(STEP_IMPACT)
    }

    const handleExecuteDelete = async () => {
        setDeleting(true)
        try {
            await dataProvider.deleteSongs(selectedIds)
            notify('resources.song.notifications.deleted', {
                messageArgs: { smart_count: selectedIds.length },
            })
            refresh()
            unselectAll(resource)
            reset()
        } catch (error) {
            // eslint-disable-next-line no-console
            console.error('Failed to delete songs:', error)
            notify('ra.page.error', { type: 'warning' })
            setDeleting(false)
        }
    }

    const impactLabel = (impact) => {
        const key = impact.empty
            ? `message.impact_${impact.type}_empty`
            : `message.impact_${impact.type}_partial`
        const template = translate(key)
        const values = impact.empty
            ? { name: impact.name }
            : { name: impact.name, songs: impact.songTitles.join('\u3001') }
        return renderImpactText(template, values, classes.highlight)
    }

    return (
        <>
            <Button
                onClick={handleClick}
                className={className}
                label={translate('resources.song.actions.delete')}
            >
                <DeleteIcon />
            </Button>
            <Dialog
                open={step === STEP_LIST}
                onClose={handleClose}
                fullWidth
                maxWidth="xs"
                classes={{ paper: classes.dialogPaper }}
                aria-labelledby="delete-songs-dialog-title"
            >
                <DialogTitle id="delete-songs-dialog-title">
                    {translate('message.delete_song_title', {
                        smart_count: selectedIds.length,
                    })}
                </DialogTitle>
                <DialogContent className={classes.dialogContent} dividers>
                    <List dense>
                        {selectedIds.map((id) => {
                            const song = songs?.[id]
                            return (
                                <ListItem key={id}>
                                    <ListItemText
                                        primary={song?.title ?? id}
                                        secondary={song?.artist}
                                    />
                                </ListItem>
                            )
                        })}
                    </List>
                </DialogContent>
                <DialogActions>
                    <MuiButton onClick={handleClose}>
                        {translate('ra.action.cancel')}
                    </MuiButton>
                    <MuiButton onClick={handleAnalyze} color="secondary">
                        {translate('resources.song.actions.next')}
                    </MuiButton>
                </DialogActions>
            </Dialog>
            <Dialog
                open={step === STEP_ANALYZING}
                fullWidth
                maxWidth="xs"
                classes={{ paper: classes.dialogPaper }}
                aria-labelledby="delete-songs-analyzing-title"
            >
                <DialogContent className={classes.dialogContent}>
                    <Box className={classes.progressContainer}>
                        <Typography className={classes.progressText} variant="body1">
                            {translate('message.calculating_impact')}
                        </Typography>
                        <LinearProgress
                            variant={progress.total ? 'determinate' : 'indeterminate'}
                            value={
                                progress.total
                                    ? (progress.current / progress.total) * 100
                                    : undefined
                            }
                        />
                    </Box>
                </DialogContent>
            </Dialog>
            <Dialog
                open={step === STEP_IMPACT}
                onClose={handleClose}
                fullWidth
                maxWidth="xs"
                classes={{ paper: classes.dialogPaper }}
                aria-labelledby="delete-songs-impact-title"
            >
                <DialogTitle id="delete-songs-impact-title">
                    {translate('message.delete_song_title', {
                        smart_count: selectedIds.length,
                    })}
                </DialogTitle>
                <DialogContent className={classes.dialogContent} dividers>
                    {impacts.length > 0 ? (
                        <List dense>
                            {impacts.map((impact) => (
                                <ListItem key={`${impact.type}-${impact.id}`}>
                                    <ListItemText primary={impactLabel(impact)} />
                                </ListItem>
                            ))}
                        </List>
                    ) : (
                        <Typography variant="body2">
                            {translate('message.no_impact')}
                        </Typography>
                    )}
                </DialogContent>
                <DialogActions>
                    <MuiButton onClick={handleClose} disabled={deleting}>
                        {translate('ra.action.cancel')}
                    </MuiButton>
                    <MuiButton
                        onClick={handleExecuteDelete}
                        color="secondary"
                        disabled={deleting}
                    >
                        {translate('resources.song.actions.executeDelete')}
                    </MuiButton>
                </DialogActions>
            </Dialog>
        </>
    )
}

DeleteSongsButton.propTypes = {
    resource: PropTypes.string,
    selectedIds: PropTypes.arrayOf(PropTypes.string).isRequired,
}
