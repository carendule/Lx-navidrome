import React, { useState } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import { useDataProvider, useNotify, useRefresh, useTranslate } from 'react-admin'
import {
    Box,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    LinearProgress,
    Typography,
    makeStyles,
} from '@material-ui/core'
import { closeMergePlaylists } from '../actions'
import { SelectPlaylistInput } from './SelectPlaylistInput'
import { httpClient } from '../dataProvider'
import { REST_URL } from '../consts'

const useStyles = makeStyles({
    dialogPaper: {
        height: '26em',
        maxHeight: '26em',
    },
    dialogContent: {
        height: '17.5em',
        overflowY: 'auto',
        paddingTop: '0.5em',
        paddingBottom: '0.5em',
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
})

const getPlaylistTrackMediaFileIds = async (playlistId) => {
    const res = await httpClient(`${REST_URL}/playlist/${playlistId}/tracks`)
    return (res.json || []).map((track) => track.mediaFileId)
}

export const MergePlaylistsDialog = () => {
    const classes = useStyles()
    const { open, selectedIds } = useSelector(
        (state) => state.mergePlaylistsDialog,
    )
    const dispatch = useDispatch()
    const translate = useTranslate()
    const notify = useNotify()
    const refresh = useRefresh()
    const dataProvider = useDataProvider()
    const [value, setValue] = useState([])
    const [merging, setMerging] = useState(false)
    const [progress, setProgress] = useState({ current: 0, total: 0 })

    const handleChange = (pls) => setValue(pls)

    const resetAndClose = () => {
        setValue([])
        setMerging(false)
        setProgress({ current: 0, total: 0 })
        dispatch(closeMergePlaylists())
    }

    const handleClickClose = (e) => {
        if (merging) return
        resetAndClose()
        e.stopPropagation()
    }

    const handleSubmit = async (e) => {
        e.stopPropagation()
        const targets = value
        if (!targets.length) return

        setMerging(true)
        try {
            // Resolve target ids, creating any new playlists first.
            const targetIds = []
            for (const target of targets) {
                let targetId = target.id
                if (!targetId) {
                    const res = await dataProvider.create('playlist', {
                        data: { name: target.name },
                    })
                    targetId = res.data.id
                }
                targetIds.push(targetId)
            }

            // Each target merges tracks from every other selected playlist.
            const jobs = targetIds.map((targetId) => ({
                targetId,
                sourceIds: selectedIds.filter((id) => id !== targetId),
            }))
            const total = jobs.reduce((sum, job) => sum + job.sourceIds.length, 0)
            let current = 0
            setProgress({ current, total })

            for (const job of jobs) {
                // Songs already in the target playlist must not be duplicated.
                const mergedMediaFileIds = new Set(
                    await getPlaylistTrackMediaFileIds(job.targetId),
                )

                for (const sourceId of job.sourceIds) {
                    const sourceMediaFileIds = await getPlaylistTrackMediaFileIds(
                        sourceId,
                    )
                    const newIds = sourceMediaFileIds.filter(
                        (id) => !mergedMediaFileIds.has(id),
                    )
                    if (newIds.length) {
                        await dataProvider.create('playlistTrack', {
                            data: { ids: newIds },
                            filter: { playlist_id: job.targetId },
                        })
                        newIds.forEach((id) => mergedMediaFileIds.add(id))
                    }
                    current += 1
                    setProgress({ current, total })
                }
            }

            notify('resources.playlist.message.playlistsMerged')
            refresh()
        } catch (error) {
            notify('ra.page.error', 'warning')
        } finally {
            resetAndClose()
        }
    }

    return (
        <Dialog
            open={open}
            onClose={handleClickClose}
            aria-labelledby="form-dialog-merge-playlists"
            fullWidth={true}
            maxWidth={'sm'}
            classes={{
                paper: classes.dialogPaper,
            }}
        >
            <DialogTitle id="form-dialog-merge-playlists">
                {translate('resources.playlist.actions.selectPlaylist')}
            </DialogTitle>
            <DialogContent className={classes.dialogContent}>
                {merging ? (
                    <Box className={classes.progressContainer}>
                        <Typography className={classes.progressText} variant="body1">
                            {translate('resources.playlist.message.merging', {
                                current: progress.current,
                                total: progress.total,
                            })}
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
                ) : (
                    <SelectPlaylistInput onChange={handleChange} />
                )}
            </DialogContent>
            <DialogActions>
                <Button onClick={handleClickClose} color="primary" disabled={merging}>
                    {translate('ra.action.cancel')}
                </Button>
                <Button
                    onClick={handleSubmit}
                    color="primary"
                    disabled={merging || !value.length}
                    data-testid="playlist-merge"
                >
                    {translate('resources.playlist.actions.mergePlaylists')}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
