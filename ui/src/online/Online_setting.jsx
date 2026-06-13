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
  MdTextFields,
} from 'react-icons/md'
import { httpClient } from '../dataProvider'
import {
  NAME_TEMPLATE_TOKENS,
  NAME_TEMPLATE_DEFAULT,
  fetchOnlineNameTemplate,
} from './online_source_settings_api'

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
  titleRow: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    minWidth: 0,
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
    cursor: 'grab',
    touchAction: 'none',
  },
  dragHandleDisabled: {
    cursor: 'not-allowed',
    opacity: 0.35,
  },
  cardDragging: {
    opacity: 0.6,
  },
  cardDropTarget: {
    borderColor: theme.palette.primary.main,
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
  templateRow: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    padding: theme.spacing(0.8, 1),
    borderRadius: 10,
    border: `1px dashed ${theme.palette.divider}`,
    backgroundColor: 'transparent',
    marginBottom: theme.spacing(0.8),
    minHeight: 40,
    transition: 'background-color 0.15s, border-color 0.15s',
  },
  templateRowDrop: {
    backgroundColor: theme.palette.action.hover,
    borderColor: theme.palette.primary.main,
  },
  templateHint: {
    color: theme.palette.text.secondary,
    fontWeight: 600,
    minWidth: 48,
    flexShrink: 0,
  },
  templateChips: {
    display: 'flex',
    flexWrap: 'wrap',
    gap: theme.spacing(0.8),
    flex: 1,
    minWidth: 0,
    // Smooth re-flow when a chip is being dragged out of (or back
    // into) the row. The 180ms easing matches the chip height so the
    // flex reflow feels like the floating chip is being lifted out
    // of a slot rather than teleported.
    transition: 'min-height 0.18s ease',
  },
  templateChip: {
    display: 'inline-flex',
    alignItems: 'center',
    height: 28,
    paddingLeft: 4,
    paddingRight: 12,
    borderRadius: 14,
    cursor: 'grab',
    userSelect: 'none',
    backgroundColor: theme.palette.action.selected,
    color: theme.palette.text.primary,
    fontSize: '0.8rem',
    lineHeight: 1,
    // The `transform` transition is what the FLIP animation in the
    // layout effect (see useLayoutEffect below) uses to slide chips
    // smoothly from their old position to the new one. The opacity
    // transition covers the brief moment when a chip is being
    // dragged (the source chip is removed from the list, the
    // floating clone takes over visually).
    transition: 'transform 0.18s ease, opacity 0.18s ease',
    willChange: 'transform',
    '&:active': {
      cursor: 'grabbing',
    },
    '&:focus': {
      outline: `2px solid ${theme.palette.primary.main}`,
      outlineOffset: 2,
    },
  },
  templateChipIcon: {
    width: 18,
    height: 18,
    color: theme.palette.text.disabled,
    marginRight: 2,
    flexShrink: 0,
  },
  templateChipLabel: {
    fontWeight: 500,
  },
  // Floating chip rendered as a fixed-positioned clone that follows
  // the cursor while a real drag is in progress. The original chip is
  // simply *removed* from the list (not replaced with a ghost) so the
  // flex layout reflows the surrounding chips with the 0.18s easing
  // declared on .templateChip — that reflow is the "make room"
  // animation the user asked for.
  templateChipFloating: {
    position: 'fixed',
    zIndex: 1500,
    pointerEvents: 'none',
    display: 'inline-flex',
    alignItems: 'center',
    height: 28,
    paddingLeft: 4,
    paddingRight: 12,
    borderRadius: 14,
    backgroundColor: theme.palette.action.selected,
    color: theme.palette.text.primary,
    fontSize: '0.8rem',
    lineHeight: 1,
    boxShadow: theme.shadows[4],
    transform: 'translate(-50%, -50%)',
  },
  // Entry animation for chips: they slide in from a slight scale +
  // fade so newly added chips (or chips re-entering after a
  // drop) don't pop. `animation-fill-mode: backwards` keeps the
  // element invisible before its animation starts.
  '@keyframes templateChipEnter': {
    from: { opacity: 0, transform: 'scale(0.85)' },
    to: { opacity: 1, transform: 'scale(1)' },
  },
  templateChipEnter: {
    animation: 'templateChipEnter 0.18s ease',
    animationFillMode: 'backwards',
  },
  templateEmpty: {
    color: theme.palette.text.disabled,
    fontStyle: 'italic',
  },
  templateHintNote: {
    color: theme.palette.text.disabled,
    marginTop: theme.spacing(0.5),
  },
}))

const tagColor = (theme, tag) => {
  const map = {
    网易: { bg: '#fde2e2', color: '#a13030' },
    QQ: { bg: '#d7f6e8', color: '#1f7a53' },
    酷我: { bg: '#fdeccf', color: '#935b00' },
    酷狗: { bg: '#dfe8ff', color: '#2c4ca3' },
    咪咕: { bg: '#ffe1ea', color: '#a3335d' },
    git: {
      bg: theme.palette.action.selected,
      color: theme.palette.text.primary,
    },
    qsvip: {
      bg: theme.palette.action.hover,
      color: theme.palette.text.primary,
    },
  }
  return (
    map[tag] || {
      bg: theme.palette.action.hover,
      color: theme.palette.text.secondary,
    }
  )
}

const sourceCodeMap = {
  kg: '网易',
  tx: 'QQ',
  wy: '酷我',
  kw: '酷狗',
  mg: '咪咕',
}

// shallowEqualStringArray is used by the name-template auto-save
// gate to avoid POSTing when the new template is byte-identical
// to the previous one. Pointer-equal (same array reference) is
// treated as equal so moveTemplateChip can early-out cheaply
// before doing the network round-trip.
const shallowEqualStringArray = (a, b) => {
  if (a === b) return true
  if (!a || !b || a.length !== b.length) return false
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return false
  }
  return true
}

// NAME_TEMPLATE_TOKENS and NAME_TEMPLATE_DEFAULT are now imported
// from ./online_source_settings_api so the search page can re-use
// them and stay in sync with this page.

const OnlineSetting = () => {
  const classes = useStyles()
  const theme = useTheme()
  const notify = useNotify()
  const translate = useTranslate()
  const [sources, setSources] = React.useState([])
  const [downloadPath, setDownloadPath] = React.useState('')
  const [savingDownloadPath, setSavingDownloadPath] = React.useState(false)
  // nameTemplate: chips the user has selected for the download
  // filename template (saved to settings). Defaults to [歌名, 歌手].
  // The "available" row is derived as NAME_TEMPLATE_TOKENS minus
  // nameTemplate, so there is exactly one source of truth.
  const [nameTemplate, setNameTemplate] = React.useState(
    NAME_TEMPLATE_DEFAULT.slice(),
  )
  const [draggingID, setDraggingID] = React.useState('')
  const [dragOverID, setDragOverID] = React.useState('')
  const [touchDraggingID, setTouchDraggingID] = React.useState('')
  const touchLongPressTimerRef = React.useRef(null)
  const touchPointRef = React.useRef({ x: 0, y: 0 })
  const draggingIDRef = React.useRef('')
  const dragOverIDRef = React.useRef('')
  const dragPlaceAfterRef = React.useRef(false)
  const dragModeRef = React.useRef('')
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
    // The two settings fields are fetched in parallel — nameTemplate
    // uses the shared helper so the search page and this page agree
    // on what "valid template" means. Suspend auto-save until BOTH
    // the initial load finishes AND the React state setter has
    // settled, so a chip drop that races with the initial load
    // doesn't clobber the server value.
    suspendAutoSaveRef.current = true
    Promise.all([
      httpClient('/api/online/source/settings')
        .then(({ json }) =>
          typeof json?.downloadPath === 'string' ? json.downloadPath : '',
        )
        .catch(() => ''),
      fetchOnlineNameTemplate(),
    ]).then(([path, template]) => {
      setDownloadPath(path)
      setNameTemplate(template)
      // Re-enable auto-save on the next tick so the state updates
      // we just queued don't accidentally re-POST during the same
      // tick.
      setTimeout(() => {
        suspendAutoSaveRef.current = false
      }, 0)
    })
  }, [])

  // availableTemplate is the read-only complement of nameTemplate,
  // preserving NAME_TEMPLATE_TOKENS order so the UI is stable.
  const availableTemplate = React.useMemo(
    () => NAME_TEMPLATE_TOKENS.filter((t) => !nameTemplate.includes(t)),
    [nameTemplate],
  )

  // moveTemplateChip moves `clip` from `from` row to `toRow` at
  // `toIndex`. The index is the *slot* in the destination list, i.e.
  // 0 means "at the very start" and array.length means "appended at
  // the end". A `null` index is treated as "append to end".
  //
  // Same-row reorders within the available row are ignored (the
  // available row is rendered in NAME_TEMPLATE_TOKENS order for
  // stability). The selected row IS reorderable because its order is
  // the filename template the user cares about.
  // Auto-save support for the name template. The chip UI persists
  // the user's selected order as soon as they drop a chip (no
  // "Save" button is wired to it). The Download-Path save button
  // only persists `downloadPath` and reads the server-authoritative
  // name template back; it does not write nameTemplate.
  //
  // suspendAutoSaveRef is true during the initial load (so the
  // value we just received from the server doesn't immediately get
  // echoed back), and during the brief window after the Download
  // Path save when the server echoes the new template — both
  // cases would otherwise cause a redundant round-trip.
  const suspendAutoSaveRef = React.useRef(true)

  const persistNameTemplate = React.useCallback(
    (template) => {
      if (suspendAutoSaveRef.current) return
      httpClient('/api/online/source/settings', {
        method: 'POST',
        body: JSON.stringify({ nameTemplate: template }),
        headers: new Headers({ 'Content-Type': 'application/json' }),
      }).catch(() => {
        notify('下载命名顺序保存失败', 'warning')
      })
    },
    [notify],
  )

  const moveTemplateChip = React.useCallback(
    (clip, from, toRow, toIndex) => {
      if (!NAME_TEMPLATE_TOKENS.includes(clip)) return
      if (from === 'available' && toRow === 'available') return
      if (!toRow) return

      const insertAt = (row, idx) => {
        if (idx == null) return row.length
        return Math.max(0, Math.min(idx, row.length))
      }

      let next = null
      if (from === toRow) {
        // selected → selected: reorder within nameTemplate at toIndex
        const without = nameTemplate.filter((c) => c !== clip)
        const idx = insertAt(without, toIndex)
        without.splice(idx, 0, clip)
        next = without
      } else if (from === 'selected' && toRow === 'available') {
        // available row is read-only; we just need to remove `clip`
        // from the selected row.
        next = nameTemplate.filter((c) => c !== clip)
      } else {
        // from === 'available' && toRow === 'selected'
        const cloned = nameTemplate.slice()
        const idx = insertAt(cloned, toIndex)
        cloned.splice(idx, 0, clip)
        next = cloned
      }
      if (next && !shallowEqualStringArray(next, nameTemplate)) {
        setNameTemplate(next)
        persistNameTemplate(next)
      }
    },
    [nameTemplate, persistNameTemplate],
  )

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
    // The Download-Path save is intentionally a downloadPath-only
    // write: the chip template is auto-persisted on drop. We
    // suspend auto-save for the duration of the request to avoid a
    // race where a chip drop in the middle of this request would
    // clobber the server's nameTemplate value, then the server
    // response would echo back the (now stale) nameTemplate
    // bundled with downloadPath.
    suspendAutoSaveRef.current = true
    httpClient('/api/online/source/settings', {
      method: 'POST',
      body: JSON.stringify({ downloadPath }),
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
      .then(({ json }) => {
        setDownloadPath(
          typeof json?.downloadPath === 'string' ? json.downloadPath : '',
        )
        if (Array.isArray(json?.nameTemplate) && json.nameTemplate.length > 0) {
          setNameTemplate(json.nameTemplate)
        }
        notify('下载路径已保存', 'info')
      })
      .catch(() => {
        notify('下载路径保存失败', 'warning')
      })
      .finally(() => {
        setSavingDownloadPath(false)
        setTimeout(() => {
          suspendAutoSaveRef.current = false
        }, 0)
      })
  }, [downloadPath, notify])

  // Self-implemented mouse drag for the name-template chips. We don't
  // use the HTML5 drag-and-drop API because MUI v4's Chip wrapper
  // swallows the `draggable` prop, and React 17+ does not consistently
  // expose the live DataTransfer on synthetic drag events. Instead we
  // implement the same behavior natively:
  //
  //   1. mousedown on a chip arms the drag; the original chip stays
  //      in its slot, becoming a transparent "ghost" placeholder so
  //      its slot in the flex layout is preserved.
  //   2. once the cursor moves past a small threshold a floating
  //      clone of the chip is rendered as a fixed-positioned
  //      element that follows the cursor / finger.
  //   3. on every mousemove we resolve both the destination row and
  //      the precise insertion index by comparing the cursor x to
  //      each candidate chip's horizontal center; an insertion
  //      indicator line is rendered at that slot.
  //   4. on mouseup we commit the move at the resolved index and
  //      hide the floating chip + insertion indicator.
  //
  // Touch events are synthesized to mouse by the browser, so this
  // works on mobile without an explicit touch code path.
  const templateDragRef = React.useRef({
    clip: '',
    fromRow: '',
    pending: false,
    started: false,
    startX: 0,
    startY: 0,
    dropTarget: { row: '', index: 0 },
  })
  const [templateFloating, setTemplateFloating] = React.useState({
    clip: '',
    fromRow: '',
    x: 0,
    y: 0,
  })

  const handleTemplateChipMouseDown = React.useCallback(
    (event, clip, fromRow) => {
      if (event.button !== 0) return
      event.preventDefault()
      const ref = templateDragRef.current
      ref.clip = clip
      ref.fromRow = fromRow
      ref.pending = true
      ref.started = false
      ref.startX = event.clientX
      ref.startY = event.clientY
    },
    [],
  )

  // Compute the destination row + index for the current cursor
  // position. We walk up from elementFromPoint until we find a node
  // carrying `data-template-row` (the row container) or
  // `data-template-chip` (a chip slot). For chips we compare the
  // cursor x to the chip's horizontal midpoint to decide whether the
  // insertion should land *before* or *after* it. For empty rows we
  // return index 0 (only slot available).
  const resolveTemplateDropTarget = React.useCallback((clientX, clientY) => {
    let el = document.elementFromPoint(clientX, clientY)
    let rowNode = null
    let chipNode = null
    // Walk up to find the nearest row and chip (if any).
    let node = el
    while (node && node !== document.body) {
      if (
        !rowNode &&
        node.getAttribute &&
        node.getAttribute('data-template-row')
      ) {
        rowNode = node
      }
      if (
        !chipNode &&
        node.getAttribute &&
        node.getAttribute('data-template-chip')
      ) {
        chipNode = node
      }
      if (rowNode && chipNode) break
      node = node.parentElement
    }
    if (!rowNode) return { row: '', index: 0 }

    const row = rowNode.getAttribute('data-template-row')
    if (!chipNode) {
      // Empty row (no chips) — the only valid slot is index 0.
      return { row, index: 0 }
    }

    // Walk into the chip's children to find its inner padding box so
    // we compare against the actual content area, not the outer DOM
    // node which can be slightly larger.
    const rect = chipNode.getBoundingClientRect()
    const midX = rect.left + rect.width / 2
    const indexAttr = Number(chipNode.getAttribute('data-template-index'))
    // If the dragged chip itself is the one under the cursor, the
    // nearest "external" slot is the one *after* it (or before, both
    // produce the same effective position). We bias toward "after"
    // so the floating chip feels like it's been "lifted and dropped
    // back" rather than flickered.
    const draggedClip = templateDragRef.current.clip
    const isSelf =
      chipNode.getAttribute('data-template-chip') === draggedClip &&
      templateDragRef.current.fromRow === row
    if (isSelf) {
      return { row, index: indexAttr + 1 }
    }
    return {
      row,
      index: clientX < midX ? indexAttr : indexAttr + 1,
    }
  }, [])

  React.useEffect(() => {
    const DRAG_THRESHOLD = 4

    const onMouseMove = (event) => {
      const ref = templateDragRef.current
      if (!ref.pending) return
      if (!ref.started) {
        const dx = event.clientX - ref.startX
        const dy = event.clientY - ref.startY
        if (dx * dx + dy * dy < DRAG_THRESHOLD * DRAG_THRESHOLD) return
        ref.started = true
        setTemplateFloating({
          clip: ref.clip,
          fromRow: ref.fromRow,
          x: event.clientX,
          y: event.clientY,
        })
      }
      // Always update the floating chip position; the React state
      // setter is cheap and the floating chip is a single small DOM
      // node, so per-event updates don't cause noticeable jank.
      setTemplateFloating((prev) =>
        prev.x === event.clientX && prev.y === event.clientY
          ? prev
          : { ...prev, x: event.clientX, y: event.clientY },
      )
      const target = resolveTemplateDropTarget(event.clientX, event.clientY)
      templateDragRef.current.dropTarget = target
    }

    const onMouseUp = () => {
      const ref = templateDragRef.current
      if (!ref.pending) return
      const wasStarted = ref.started
      const { clip, fromRow, dropTarget: target } = ref
      ref.clip = ''
      ref.fromRow = ''
      ref.pending = false
      ref.started = false
      ref.dropTarget = { row: '', index: 0 }
      setTemplateFloating({ clip: '', fromRow: '', x: 0, y: 0 })
      if (wasStarted && clip && target.row) {
        // Adjust the target index when the source row is the same as
        // the destination row: removing the source chip first means
        // the slot at the original index is "after" the dragged chip
        // in the final array, not the position the user saw mid-drag.
        let insertAt = target.index
        if (target.row === fromRow) {
          const fromIndex = nameTemplate.indexOf(clip)
          if (fromIndex >= 0 && fromIndex < insertAt) {
            insertAt -= 1
          }
        }
        moveTemplateChip(clip, fromRow, target.row, insertAt)
      }
    }

    window.addEventListener('mousemove', onMouseMove)
    window.addEventListener('mouseup', onMouseUp)
    return () => {
      window.removeEventListener('mousemove', onMouseMove)
      window.removeEventListener('mouseup', onMouseUp)
    }
    // We intentionally do not depend on `nameTemplate` or
    // `templateDropTarget` here — the handler reads them via the
    // ref-like state closure, and the effect would otherwise be
    // re-bound on every state change. Instead we read `nameTemplate`
    // directly via the closure (it stays current) and capture the
    // latest `templateDropTarget` in the ref-bound handler scope.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [moveTemplateChip, resolveTemplateDropTarget])

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
            body: JSON.stringify({
              filename: file.name,
              content,
              allowUnsafeVM,
            }),
            headers: new Headers({ 'Content-Type': 'application/json' }),
          })

          if (json && json.success === false) {
            if (json.disabledVM) {
              notify(json.message || '服务器已禁用原生 VM 模式', 'warning')
              return null
            }
            if (json.requireUnsafe && !allowUnsafeVM) {
              const confirmed = window.confirm(
                json.message ||
                  '该脚本需要原生 VM 模式运行，可能存在安全风险，是否继续？',
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
        setSources((prev) => [
          ...prev.filter((item) => item.id !== json.id),
          json,
        ])
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

  const displaySources = React.useMemo(() => {
    const list = Array.isArray(sources) ? [...sources] : []
    const enabled = list.filter((item) => item.enabled)
    const disabled = list.filter((item) => !item.enabled)

    enabled.sort((a, b) => {
      const left = Number(a.enabledOrder) || Number.MAX_SAFE_INTEGER
      const right = Number(b.enabledOrder) || Number.MAX_SAFE_INTEGER
      if (left !== right) return left - right
      return String(a.createdAt || '').localeCompare(String(b.createdAt || ''))
    })

    return [...enabled, ...disabled]
  }, [sources])

  const reorderEnabledSources = React.useCallback(
    (dragID, targetID, placeAfter = false) => {
      if (!dragID || !targetID || dragID === targetID) return

      const enabledIDs = displaySources
        .filter((item) => item.enabled)
        .map((item) => item.id)
      const fromIndex = enabledIDs.indexOf(dragID)
      if (fromIndex < 0) return

      const nextIDs = [...enabledIDs]
      const [moved] = nextIDs.splice(fromIndex, 1)
      const targetIndex = nextIDs.indexOf(targetID)
      if (targetIndex < 0) return
      const insertIndex = placeAfter ? targetIndex + 1 : targetIndex
      nextIDs.splice(
        Math.max(0, Math.min(nextIDs.length, insertIndex)),
        0,
        moved,
      )

      const enabledMap = new Map(
        displaySources
          .filter((item) => item.enabled)
          .map((item) => [item.id, item]),
      )
      const disabled = displaySources.filter((item) => !item.enabled)
      const reorderedEnabled = nextIDs.map((id, idx) => ({
        ...enabledMap.get(id),
        enabledOrder: idx + 1,
      }))
      setSources([...reorderedEnabled, ...disabled])

      httpClient('/api/online/source/reorder', {
        method: 'POST',
        body: JSON.stringify({ sourceIds: nextIDs }),
        headers: new Headers({ 'Content-Type': 'application/json' }),
      })
        .then(() => {
          loadSources()
        })
        .catch(() => {
          loadSources()
          notify('排序保存失败', 'warning')
        })
    },
    [displaySources, loadSources, notify],
  )

  const clearTouchLongPressTimer = React.useCallback(() => {
    if (touchLongPressTimerRef.current) {
      window.clearTimeout(touchLongPressTimerRef.current)
      touchLongPressTimerRef.current = null
    }
  }, [])

  const handleCardMouseEnter = React.useCallback((event, item) => {
    if (!draggingIDRef.current || !item.enabled) return
    dragOverIDRef.current = item.id
    setDragOverID(item.id)
  }, [])

  const commitPointerDrag = React.useCallback(() => {
    const dragID = draggingIDRef.current
    const targetID = dragOverIDRef.current
    if (dragID && targetID && dragID !== targetID) {
      reorderEnabledSources(dragID, targetID, dragPlaceAfterRef.current)
    }

    draggingIDRef.current = ''
    dragOverIDRef.current = ''
    dragPlaceAfterRef.current = false
    dragModeRef.current = ''
    setDraggingID('')
    setTouchDraggingID('')
    setDragOverID('')
  }, [reorderEnabledSources])

  const handleMouseDragStart = React.useCallback((event, item) => {
    if (!item.enabled) return
    event.preventDefault()
    draggingIDRef.current = item.id
    dragOverIDRef.current = item.id
    dragPlaceAfterRef.current = false
    dragModeRef.current = 'mouse'
    setDraggingID(item.id)
    setDragOverID(item.id)
  }, [])

  const handleMouseMove = React.useCallback((event) => {
    if (dragModeRef.current !== 'mouse' || !draggingIDRef.current) return
    const element = document.elementFromPoint(event.clientX, event.clientY)
    if (!element) return

    let el = element
    while (el && el !== document.body) {
      const sid = el.getAttribute && el.getAttribute('data-source-id')
      if (sid) {
        if (el.getAttribute('data-enabled') === 'true') {
          dragOverIDRef.current = sid
          setDragOverID(sid)
          const rect = el.getBoundingClientRect()
          dragPlaceAfterRef.current = event.clientY > rect.top + rect.height / 2
        }
        break
      }
      el = el.parentElement
    }
  }, [])

  const handleMouseUp = React.useCallback(() => {
    if (dragModeRef.current !== 'mouse') return
    commitPointerDrag()
  }, [commitPointerDrag])

  const handleTouchStart = React.useCallback(
    (event, item) => {
      if (!item.enabled) return
      const touch = event.touches && event.touches[0]
      if (!touch) return

      touchPointRef.current = { x: touch.clientX, y: touch.clientY }
      clearTouchLongPressTimer()
      touchLongPressTimerRef.current = window.setTimeout(() => {
        draggingIDRef.current = item.id
        dragOverIDRef.current = item.id
        dragPlaceAfterRef.current = false
        dragModeRef.current = 'touch'
        setDraggingID(item.id)
        setTouchDraggingID(item.id)
        setDragOverID(item.id)
      }, 280)
    },
    [clearTouchLongPressTimer],
  )

  const handleTouchMove = React.useCallback(
    (event) => {
      const touch = event.touches && event.touches[0]
      if (!touch) return

      touchPointRef.current = { x: touch.clientX, y: touch.clientY }
      if (!touchDraggingID) return
      event.preventDefault()

      const element = document.elementFromPoint(touch.clientX, touch.clientY)
      if (element) {
        let el = element
        while (el && el !== document.body) {
          const sid = el.getAttribute && el.getAttribute('data-source-id')
          if (sid) {
            if (el.getAttribute('data-enabled') === 'true') {
              dragOverIDRef.current = sid
              setDragOverID(sid)
              const rect = el.getBoundingClientRect()
              dragPlaceAfterRef.current =
                touch.clientY > rect.top + rect.height / 2
            }
            break
          }
          el = el.parentElement
        }
      }
    },
    [touchDraggingID],
  )

  const handleTouchEnd = React.useCallback(() => {
    clearTouchLongPressTimer()
    if (dragModeRef.current === 'touch') {
      commitPointerDrag()
      return
    }
    setTouchDraggingID('')
    setDragOverID('')
  }, [clearTouchLongPressTimer, commitPointerDrag])

  React.useEffect(() => {
    window.addEventListener('mousemove', handleMouseMove)
    window.addEventListener('mouseup', handleMouseUp)
    return () => {
      window.removeEventListener('mousemove', handleMouseMove)
      window.removeEventListener('mouseup', handleMouseUp)
    }
  }, [handleMouseMove, handleMouseUp])

  React.useEffect(
    () => () => clearTouchLongPressTimer(),
    [clearTouchLongPressTimer],
  )

  // FLIP animation for chip list reflow. The technique: right before
  // React commits the next render we snapshot every chip's bounding
  // rect; after the DOM has been updated we read the new rects,
  // compute the per-chip delta, and apply `transform: translate(dx,
  // dy)` synchronously to put each chip back where it was — then on
  // the very next animation frame we remove the transform, and the
  // browser animates the chip from its old visual position to the
  // new one with the existing 0.18s ease transition declared on
  // .templateChip. The net effect is that when the user drops a
  // chip, every other chip glides into its new slot rather than
  // teleporting.
  const chipRectsRef = React.useRef(new Map())
  const flipPendingRef = React.useRef(false)

  // Schedule a flip right before React commits the next DOM update
  // caused by a nameTemplate/availableTemplate change. We use a
  // layout effect that depends on the chip lists, so it runs
  // *synchronously after* React has applied the DOM mutation but
  // *before* the browser paints. The "previous frame" rects are
  // still in chipRectsRef.current, so we set flipPendingRef to true
  // and let the unconditional layout effect below perform the
  // animation on the same tick.
  const previousNameTemplateRef = React.useRef(nameTemplate)
  const previousAvailableRef = React.useRef(availableTemplate)
  React.useLayoutEffect(() => {
    if (
      previousNameTemplateRef.current !== nameTemplate ||
      previousAvailableRef.current !== availableTemplate
    ) {
      flipPendingRef.current = true
      previousNameTemplateRef.current = nameTemplate
      previousAvailableRef.current = availableTemplate
    }
  }, [nameTemplate, availableTemplate])

  // Perform the FLIP animation. Runs after every render; if no flip
  // is pending, just snapshot the current rects for the next round.
  React.useLayoutEffect(() => {
    const nodes = document.querySelectorAll('[data-template-chip]')
    const newRects = new Map()
    nodes.forEach((node) => {
      const key = node.getAttribute('data-template-row')
      const clip = node.getAttribute('data-template-chip')
      if (!key || !clip) return
      newRects.set(`${key}:${clip}`, node.getBoundingClientRect())
    })

    if (flipPendingRef.current) {
      const FLIP_DURATION = 180
      nodes.forEach((node) => {
        const key = node.getAttribute('data-template-row')
        const clip = node.getAttribute('data-template-chip')
        if (!key || !clip) return
        const fullKey = `${key}:${clip}`
        const oldRect = chipRectsRef.current.get(fullKey)
        const newRect = newRects.get(fullKey)
        if (!oldRect || !newRect) return
        const dx = oldRect.left - newRect.left
        const dy = oldRect.top - newRect.top
        if (dx === 0 && dy === 0) return
        // Invert: jump the chip back to its old position, then let
        // the CSS transition carry it to the new (untransformed)
        // position. The transition is declared on .templateChip so
        // we don't need to set it here.
        node.style.transform = `translate(${dx}px, ${dy}px)`
        node.style.transition = 'none'
        // Force layout flush so the next frame starts from the
        // transformed state, then on the next animation frame
        // release the transform.
        // Reading getBoundingClientRect here is intentional — the
        // side effect (forcing layout) is the whole point of the
        // call. The result is discarded.
        void node.getBoundingClientRect()
        requestAnimationFrame(() => {
          node.style.transition = ''
          node.style.transform = ''
        })
        // Best-effort cleanup after the animation in case the inline
        // transition override was preserved.
        setTimeout(() => {
          node.style.transition = ''
        }, FLIP_DURATION + 50)
      })
      flipPendingRef.current = false
    }

    // Snapshot the current rects so the *next* flip can compare
    // against them. This runs every render so the snapshot stays
    // current.
    chipRectsRef.current = newRects
  })

  const toDisplaySize = (size) => {
    const num = Number(size)
    if (!Number.isFinite(num) || num <= 0) return '0 KB'
    if (num < 1024) return `${num} B`
    if (num < 1024 * 1024) return `${(num / 1024).toFixed(1)} KB`
    return `${(num / 1024 / 1024).toFixed(1)} MB`
  }

  return (
    <div className={classes.root}>
      <Title
        title={'Navidrome - ' + translate('menu.online', { _: 'Online' })}
      />
      <div className={classes.settingsSection}>
        <div className={classes.settingsTitle}>
          <MdFolder className={classes.titleIcon} size={20} />
          <Typography variant="h6">
            {translate('online.downloadPathTitle', {
              _: 'Download Path Settings',
            })}
          </Typography>
        </div>
        <TextField
          className={classes.settingsField}
          variant="outlined"
          size="small"
          label={translate('online.downloadPathLabel', { _: 'Download Path' })}
          value={downloadPath}
          onChange={(event) => setDownloadPath(event.target.value)}
          placeholder={translate('online.downloadPathPlaceholder', {
            _: 'Enter download path',
          })}
        />
        <div className={classes.saveButtonWrap}>
          <Button
            variant="contained"
            color="primary"
            className={classes.manageButton}
            onClick={handleSaveDownloadPath}
            disabled={savingDownloadPath}
          >
            {translate('online.saveDownloadPath', { _: 'Save' })}
          </Button>
        </div>
      </div>

      <div className={classes.settingsSection}>
        <div className={classes.settingsTitle}>
          <MdTextFields className={classes.titleIcon} size={20} />
          <Typography variant="h6">
            {translate('online.nameTemplateTitle', {
              _: 'Download Name Settings',
            })}
          </Typography>
        </div>
        <div className={classes.templateRow} data-template-row="available">
          <Typography variant="caption" className={classes.templateHint}>
            {translate('online.nameTemplateAvailable', { _: '可用:' })}
          </Typography>
          <div className={classes.templateChips}>
            {availableTemplate.map((clip, index) => (
              <div
                key={clip}
                role="button"
                tabIndex={0}
                className={classes.templateChip}
                data-template-row="available"
                data-template-chip={clip}
                data-template-index={index}
                onMouseDown={(event) =>
                  handleTemplateChipMouseDown(event, clip, 'available')
                }
              >
                <MdDragIndicator className={classes.templateChipIcon} />
                <span className={classes.templateChipLabel}>{clip}</span>
              </div>
            ))}
            {availableTemplate.length === 0 && (
              <Typography variant="caption" className={classes.templateEmpty}>
                {translate('online.nameTemplateEmpty', { _: '（已全部选用）' })}
              </Typography>
            )}
          </div>
        </div>
        <div className={classes.templateRow} data-template-row="selected">
          <Typography variant="caption" className={classes.templateHint}>
            {translate('online.nameTemplateSelected', { _: '已选:' })}
          </Typography>
          <div className={classes.templateChips}>
            {nameTemplate.map((clip, index) => (
              <div
                key={clip}
                role="button"
                tabIndex={0}
                className={classes.templateChip}
                data-template-row="selected"
                data-template-chip={clip}
                data-template-index={index}
                onMouseDown={(event) =>
                  handleTemplateChipMouseDown(event, clip, 'selected')
                }
              >
                <MdDragIndicator className={classes.templateChipIcon} />
                <span className={classes.templateChipLabel}>{clip}</span>
              </div>
            ))}
            {nameTemplate.length === 0 && (
              <Typography variant="caption" className={classes.templateEmpty}>
                {translate('online.nameTemplateNoneSelected', {
                  _: '（请从可用行拖入至少一个片段）',
                })}
              </Typography>
            )}
          </div>
        </div>
        <Typography variant="caption" className={classes.templateHintNote}>
          {translate('online.nameTemplateHint', {
            _: '提示：拖动片段在两行间移动；已选行顺序即文件名模板，松手后自动保存。',
          })}
        </Typography>
      </div>

      {templateFloating.clip && (
        <div
          className={classes.templateChipFloating}
          style={{ left: templateFloating.x, top: templateFloating.y }}
        >
          <MdDragIndicator className={classes.templateChipIcon} />
          <span className={classes.templateChipLabel}>
            {templateFloating.clip}
          </span>
        </div>
      )}

      <div className={classes.pageTitle}>
        <MdSettings className={classes.titleIcon} size={20} />
        <Typography variant="h6">
          {translate('online.title', {
            _: 'Custom Online Sources (Lx Source)',
          })}
        </Typography>
      </div>
      <Typography variant="body2" className={classes.subtitle}>
        {translate('online.subtitle', {
          _: 'Manage and configure third-party music scripts (lxmusic supported)',
        })}
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
          {translate('online.manageOrAdd', { _: 'Add' })}
        </Button>
      </div>

      {displaySources.map((item) => (
        <Card
          key={item.id}
          className={`${classes.card} ${draggingID === item.id || touchDraggingID === item.id ? classes.cardDragging : ''} ${dragOverID === item.id && (draggingID || touchDraggingID) ? classes.cardDropTarget : ''}`}
          data-source-id={item.id}
          data-enabled={item.enabled ? 'true' : 'false'}
          onMouseEnter={(event) => handleCardMouseEnter(event, item)}
          onTouchStart={(event) => handleTouchStart(event, item)}
          onTouchMove={handleTouchMove}
          onTouchEnd={handleTouchEnd}
          onTouchCancel={handleTouchEnd}
        >
          <CardContent>
            <div className={classes.row}>
              <div className={classes.left}>
                <span
                  onMouseDown={(event) => handleMouseDragStart(event, item)}
                >
                  <MdDragIndicator
                    className={`${classes.dragHandle} ${!item.enabled ? classes.dragHandleDisabled : ''}`}
                    size={18}
                  />
                </span>
                <Box minWidth={0}>
                  <div className={classes.titleRow}>
                    <Typography
                      variant="subtitle1"
                      className={classes.sourceTitle}
                    >
                      {item.name}
                    </Typography>
                    {item.enabled && (
                      <Chip
                        size="small"
                        label={translate('online.enabled', { _: 'Enabled' })}
                        className={classes.statusEnabled}
                      />
                    )}
                  </div>

                  <div className={classes.sourceMeta}>
                    <span>{item.author}</span>
                    <span>{toDisplaySize(item.size)}</span>
                    <Chip
                      size="small"
                      label={item.version}
                      className={classes.tag}
                    />
                    <Chip
                      size="small"
                      icon={<MdCheckCircle size={14} />}
                      label={
                        item.status ||
                        translate('online.healthy', { _: 'Healthy' })
                      }
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
                    ? translate('online.disable', { _: 'Disable' })
                    : translate('online.enable', { _: 'Enable' })}
                </Button>
                <IconButton
                  className={classes.deleteBtn}
                  onClick={() => handleDelete(item)}
                >
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
