import { BooleanInput, FormDataConsumer } from 'react-admin'

const OnlinePermissionsInputs = (props) => (
    <FormDataConsumer {...props}>
        {({ formData, ...rest }) => (
            <>
                <BooleanInput
                    {...rest}
                    source="onlineSearchEnabled"
                    label="resources.user.fields.onlineSearchEnabled"
                    defaultValue={true}
                    format={(value) => formData.isAdmin || value}
                    disabled={formData.isAdmin}
                />
                <BooleanInput
                    {...rest}
                    source="onlineBrowserDownloadEnabled"
                    label="resources.user.fields.onlineBrowserDownloadEnabled"
                    defaultValue={true}
                    format={(value) => formData.isAdmin || value}
                    disabled={formData.isAdmin || !formData.onlineSearchEnabled}
                />
                <BooleanInput
                    {...rest}
                    source="onlineServerDownloadEnabled"
                    label="resources.user.fields.onlineServerDownloadEnabled"
                    defaultValue={true}
                    format={(value) => formData.isAdmin || value}
                    disabled={formData.isAdmin || !formData.onlineSearchEnabled}
                />
                <BooleanInput
                    {...rest}
                    source="onlinePlaylistSyncEnabled"
                    label="resources.user.fields.onlinePlaylistSyncEnabled"
                    defaultValue={true}
                    format={(value) => formData.isAdmin || value}
                    disabled={formData.isAdmin || !formData.onlineSearchEnabled}
                />
            </>
        )}
    </FormDataConsumer>
)

export default OnlinePermissionsInputs
