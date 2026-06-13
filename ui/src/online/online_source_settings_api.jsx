// online_source_settings_api.jsx
//
// Tiny shared module that wraps /api/online/source/settings so the
// search and settings pages can both read & write the user-configured
// download name template without duplicating the httpClient call.
//
// The settings endpoint returns:
//
//   { downloadPath: string, nameTemplate: string[] }
//
// where nameTemplate is the *ordered* list of chip tokens the user
// picked in the "下载命名设置" panel (e.g. ["歌名", "音质", "歌手"]).
// Keeping a single source of truth here also makes it easy to add
// a localStorage cache / event later if multiple components need
// reactive updates.

import { httpClient } from '../dataProvider'

export const NAME_TEMPLATE_TOKENS = ['歌名', '歌手', '专辑', '来源', '音质']
export const NAME_TEMPLATE_DEFAULT = ['歌名', '歌手']

/**
 * Fetch the persisted name template. Returns NAME_TEMPLATE_DEFAULT
 * when the server response is missing, malformed, or empty — the
 * backend applies the same default in onlineDownloadFileName, so
 * callers don't need a special case.
 */
export const fetchOnlineNameTemplate = () =>
  httpClient('/api/online/source/settings')
    .then(({ json }) => {
      if (Array.isArray(json?.nameTemplate) && json.nameTemplate.length > 0) {
        const filtered = json.nameTemplate.filter((t) =>
          NAME_TEMPLATE_TOKENS.includes(t),
        )
        if (filtered.length > 0) return filtered
      }
      return NAME_TEMPLATE_DEFAULT
    })
    .catch(() => NAME_TEMPLATE_DEFAULT)
