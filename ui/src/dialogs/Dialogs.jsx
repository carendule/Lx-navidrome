import { AddToPlaylistDialog } from './AddToPlaylistDialog'
import DownloadMenuDialog from './DownloadMenuDialog'
import { HelpDialog } from './HelpDialog'
import { ShareDialog } from './ShareDialog'
import { SaveQueueDialog } from './SaveQueueDialog'
import { MergePlaylistsDialog } from './MergePlaylistsDialog'

export const Dialogs = (props) => (
  <>
    <AddToPlaylistDialog />
    <SaveQueueDialog />
    <DownloadMenuDialog />
    <HelpDialog />
    <ShareDialog />
    <MergePlaylistsDialog />
  </>
)
