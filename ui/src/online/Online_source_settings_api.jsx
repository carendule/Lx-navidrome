// Online_source_settings_api.jsx
//
// Tiny shared module that wraps /api/online/source/settings so the
// search and settings pages can both read & write the user-configured
// download name template and embed mode without duplicating the
// httpClient call.
//
// The settings endpoint returns:
//
//   {
//     downloadPath: string,
//     nameTemplate: string[],
//     embedMode: 'none' | 'metadata' | 'all',
//   }
//
// where nameTemplate is the *ordered* list of chip tokens the user
// picked in the "下载命名设置" panel (e.g. ["歌名", "音质", "歌手"]),
// and embedMode controls what the downloader writes into the audio
// file's tag container (no cover at all / cover+tags / cover+tags
// +lyrics). Keeping a single source of truth here also makes it
// easy to add a localStorage cache / event later if multiple
// components need reactive updates.

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

// Embed mode constants — mirror the server-side values in
// server/nativeapi/online_source.go so the settings page can label
// the radio options without hardcoding strings at the call site.
// If a future server adds a fourth mode, just extend the array +
// add a translation key and the form picks it up automatically.
export const EMBED_MODES = ['none', 'metadata', 'all']
export const EMBED_MODE_DEFAULT = 'metadata'
// Legacy 2-state UI value, kept here so an in-flight settings page
// that was rendering under the old single-checkbox schema still
// gets a valid radio selection on next mount.
export const LEGACY_EMBED_METADATA_TRUE = 'all'
export const LEGACY_EMBED_METADATA_FALSE = 'none'

/**
 * Fetch the persisted embed mode. Returns EMBED_MODE_DEFAULT when
 * the server response is missing, malformed, or carries an
 * unrecognised value — the backend applies sanitizeEmbedMode with
 * the same fallback, so a hand-edited settings.json (or a server
 * version behind the frontend) can't disable embedding silently.
 */
export const fetchOnlineEmbedMode = () =>
  httpClient('/api/online/source/settings')
    .then(({ json }) => {
      const mode = typeof json?.embedMode === 'string' ? json.embedMode : ''
      if (EMBED_MODES.includes(mode)) return mode
      // Map a legacy bool-style response (older server builds) to
      // a 3-state value so the UI still renders correctly while
      // the user is on a mixed-version fleet.
      if (json?.embedMetadata === true) return LEGACY_EMBED_METADATA_TRUE
      if (json?.embedMetadata === false) return LEGACY_EMBED_METADATA_FALSE
      return EMBED_MODE_DEFAULT
    })
    .catch(() => EMBED_MODE_DEFAULT)

/**
 * Persist a new embed mode. The endpoint accepts a full
 * onlineSourceSettings payload, so we re-fetch the current
 * downloadPath + nameTemplate and patch the embedMode in place
 * to avoid clobbering the user's other choices. Returns the
 * server's resolved value (sanitized) so the caller can update
 * its local state without re-fetching.
 */
export const saveOnlineEmbedMode = (mode) => {
  const sanitized = EMBED_MODES.includes(mode) ? mode : EMBED_MODE_DEFAULT
  return httpClient('/api/online/source/settings')
    .then(({ json }) => {
      const next = {
        downloadPath: json?.downloadPath ?? '',
        nameTemplate: Array.isArray(json?.nameTemplate)
          ? json.nameTemplate
          : NAME_TEMPLATE_DEFAULT,
        embedMode: sanitized,
      }
      return httpClient('/api/online/source/settings', {
        method: 'POST',
        body: JSON.stringify(next),
        headers: new Headers({ 'Content-Type': 'application/json' }),
      }).then(({ json: resp }) => resp?.embedMode ?? sanitized)
    })
    .catch(() => sanitized)
}
