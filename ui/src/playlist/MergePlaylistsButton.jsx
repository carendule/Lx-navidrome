import * as React from 'react'
import { useDispatch } from 'react-redux'
import MergeTypeIcon from '@material-ui/icons/MergeType'
import { Button, useTranslate } from 'react-admin'
import { openMergePlaylists } from '../actions'

const MergePlaylistsButton = (props) => {
    const translate = useTranslate()
    const dispatch = useDispatch()
    const { selectedIds, ...rest } = props

    const handleClick = () => {
        dispatch(openMergePlaylists({ selectedIds }))
    }

    return (
        <Button
            {...rest}
            label={translate('resources.playlist.actions.mergePlaylists')}
            onClick={handleClick}
        >
            <MergeTypeIcon />
        </Button>
    )
}

export default MergePlaylistsButton
