import React from 'react'
import {
  Create,
  SimpleForm,
  TextInput,
  required,
  useTranslate,
  useRefresh,
  useNotify,
  useRedirect,
} from 'react-admin'
import { Title } from '../common'
import PlaylistVisibilityInput from './PlaylistVisibilityInput'

const PlaylistCreate = (props) => {
  const { basePath } = props
  const refresh = useRefresh()
  const notify = useNotify()
  const redirect = useRedirect()
  const translate = useTranslate()
  const resourceName = translate('resources.playlist.name', { smart_count: 1 })
  const title = translate('ra.page.create', {
    name: `${resourceName}`,
  })

  const onSuccess = () => {
    notify('ra.notification.created', 'info', { smart_count: 1 })
    redirect('list', basePath)
    refresh()
  }

  return (
    <Create title={<Title subTitle={title} />} {...props} onSuccess={onSuccess}>
      <SimpleForm redirect="list" variant={'outlined'}>
        <TextInput source="name" validate={required()} />
        <TextInput multiline source="comment" />
        <PlaylistVisibilityInput />
      </SimpleForm>
    </Create>
  )
}

export default PlaylistCreate
