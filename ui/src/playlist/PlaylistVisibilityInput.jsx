import {
    ReferenceArrayInput,
    SelectArrayInput,
    usePermissions,
} from 'react-admin'

const PlaylistVisibilityInput = (props) => {
    const { permissions } = usePermissions()
    if (permissions !== 'admin') return null
    return (
        <ReferenceArrayInput
            {...props}
            source="visibleToUserIds"
            label="resources.playlist.fields.visibleToUserIds"
            defaultValue={[]}
            reference="user"
            perPage={0}
            sort={{ field: 'userName', order: 'ASC' }}
        >
            <SelectArrayInput optionText="userName" label="resources.playlist.fields.visibleToUserIds" fullWidth />
        </ReferenceArrayInput>
    )
}

export default PlaylistVisibilityInput
