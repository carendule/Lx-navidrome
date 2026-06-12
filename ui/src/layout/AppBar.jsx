import React, {
  createElement,
  forwardRef,
  Fragment,
  useCallback,
  useEffect,
  useState,
} from 'react'
import {
  AppBar as RAAppBar,
  MenuItemLink,
  useTranslate,
  usePermissions,
  getResources,
} from 'react-admin'
import { MdInfo, MdPerson, MdSupervisorAccount, MdPublic } from 'react-icons/md'
import { useSelector } from 'react-redux'
import {
  makeStyles,
  MenuItem,
  ListItemIcon,
  Divider,
  IconButton,
  Tooltip,
  Badge,
} from '@material-ui/core'
import ViewListIcon from '@material-ui/icons/ViewList'
import GetAppIcon from '@material-ui/icons/GetApp'
import { Dialogs } from '../dialogs/Dialogs'
import { AboutDialog } from '../dialogs'
import PersonalMenu from './PersonalMenu'
import ActivityPanel from './ActivityPanel'
import NowPlayingPanel from './NowPlayingPanel'
import UserMenu from './UserMenu'
import config from '../config'
import { httpClient } from '../dataProvider'
import DownloadList from '../online/Download_list'

const ONLINE_SOURCE_STATUS_CHANGED_EVENT = 'nd:online-source-status-changed'
const ONLINE_DOWNLOAD_TASK_CHANGED_EVENT = 'nd:online-download-task-changed'

const useStyles = makeStyles(
  (theme) => ({
    root: {
      color: theme.palette.text.secondary,
    },
    active: {
      color: theme.palette.text.primary,
    },
    icon: { minWidth: theme.spacing(5) },
    downloadBadge: {
      '& .MuiBadge-badge': {
        backgroundColor: theme.palette.error.main,
        color: theme.palette.common.white,
        minWidth: 16,
        height: 16,
        borderRadius: '50%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontSize: '0.65rem',
        fontWeight: 700,
        padding: '0 4px',
      },
    },
  }),
  {
    name: 'NDAppBar',
  },
)

const emptyTaskState = {
  tasks: [],
  activeCount: 0,
  totalSpeedText: '0 B/s',
  totalProgress: 0,
}

const AboutMenuItem = forwardRef(({ onClick, ...rest }, ref) => {
  const classes = useStyles(rest)
  const translate = useTranslate()
  const [open, setOpen] = React.useState(false)

  const handleOpen = () => {
    setOpen(true)
  }
  const handleClose = () => {
    onClick && onClick()
    setOpen(false)
  }
  const label = translate('menu.about')
  return (
    <>
      <MenuItem ref={ref} onClick={handleOpen} className={classes.root}>
        <ListItemIcon className={classes.icon}>
          <MdInfo title={label} size={24} />
        </ListItemIcon>
        {label}
      </MenuItem>
      <AboutDialog onClose={handleClose} open={open} />
    </>
  )
})

AboutMenuItem.displayName = 'AboutMenuItem'

const settingsResources = (resource) =>
  resource.name !== 'user' &&
  resource.hasList &&
  resource.options &&
  resource.options.subMenu === 'settings'

const CustomUserMenu = ({ onClick, ...rest }) => {
  const translate = useTranslate()
  const resources = useSelector(getResources)
  const classes = useStyles(rest)
  const { permissions } = usePermissions()
  const [showOnlineSearch, setShowOnlineSearch] = useState(false)
  const [downloadListOpen, setDownloadListOpen] = useState(false)
  const [downloadTaskState, setDownloadTaskState] = useState(emptyTaskState)

  const refreshOnlineSearchVisibility = useCallback((activeRef) => {
    httpClient('/api/online/source/status')
      .then(({ json }) => {
        if (activeRef && !activeRef.current) return
        const visible = Boolean(json?.hasEnabledSource)
        setShowOnlineSearch(visible)
        if (!visible) {
          setDownloadListOpen(false)
          setDownloadTaskState(emptyTaskState)
        }
      })
      .catch(() => {
        if (activeRef && !activeRef.current) return
        setShowOnlineSearch(false)
        setDownloadListOpen(false)
        setDownloadTaskState(emptyTaskState)
      })
  }, [])

  const refreshDownloadTasks = useCallback((activeRef) => {
    if (!showOnlineSearch) {
      setDownloadTaskState(emptyTaskState)
      return
    }
    httpClient('/api/online/download/tasks')
      .then(({ json }) => {
        if (activeRef && !activeRef.current) return
        setDownloadTaskState({
          tasks: Array.isArray(json?.tasks) ? json.tasks : [],
          activeCount: Number(json?.activeCount) || 0,
          totalSpeedText: String(json?.totalSpeedText || '0 B/s'),
          totalProgress: Number(json?.totalProgress) || 0,
        })
      })
      .catch(() => {
        if (activeRef && !activeRef.current) return
      })
  }, [showOnlineSearch])

  useEffect(() => {
    const activeRef = { current: true }

    refreshOnlineSearchVisibility(activeRef)

    const handleStatusChanged = () => {
      refreshOnlineSearchVisibility(activeRef)
    }

    window.addEventListener(ONLINE_SOURCE_STATUS_CHANGED_EVENT, handleStatusChanged)

    return () => {
      activeRef.current = false
      window.removeEventListener(
        ONLINE_SOURCE_STATUS_CHANGED_EVENT,
        handleStatusChanged,
      )
    }
  }, [refreshOnlineSearchVisibility])

  useEffect(() => {
    if (!showOnlineSearch) return () => { }

    const activeRef = { current: true }
    refreshDownloadTasks(activeRef)

    const timer = window.setInterval(() => {
      refreshDownloadTasks(activeRef)
    }, 1200)

    const handleTaskChanged = () => {
      refreshDownloadTasks(activeRef)
    }
    window.addEventListener(ONLINE_DOWNLOAD_TASK_CHANGED_EVENT, handleTaskChanged)

    return () => {
      activeRef.current = false
      window.clearInterval(timer)
      window.removeEventListener(ONLINE_DOWNLOAD_TASK_CHANGED_EVENT, handleTaskChanged)
    }
  }, [showOnlineSearch, refreshDownloadTasks])

  const postTaskAction = useCallback((url) => {
    httpClient(url, {
      method: 'POST',
      body: JSON.stringify({}),
      headers: new Headers({ 'Content-Type': 'application/json' }),
    }).finally(() => {
      refreshDownloadTasks()
      window.dispatchEvent(new Event(ONLINE_DOWNLOAD_TASK_CHANGED_EVENT))
    })
  }, [refreshDownloadTasks])

  const handleToggleDownloadList = () => {
    setDownloadListOpen((prev) => !prev)
  }

  const handleCloseDownloadList = () => {
    setDownloadListOpen(false)
  }

  const handleRetryAll = useCallback(() => {
    postTaskAction('/api/online/download/tasks/retry')
  }, [postTaskAction])

  const handleCancelAll = useCallback(() => {
    postTaskAction('/api/online/download/tasks/cancel')
  }, [postTaskAction])

  const handleClearCompleted = useCallback(() => {
    postTaskAction('/api/online/download/tasks/clear-completed')
  }, [postTaskAction])

  const handleToggleTask = useCallback((taskID) => {
    if (!taskID) return
    postTaskAction(`/api/online/download/task/${encodeURIComponent(taskID)}/toggle`)
  }, [postTaskAction])

  const resourceDefinition = (resourceName) =>
    resources.find((r) => r?.name === resourceName)

  const renderUserMenuItemLink = () => {
    const userResource = resourceDefinition('user')
    if (!userResource) {
      return null
    }
    if (permissions !== 'admin') {
      if (!config.enableUserEditing) {
        return null
      }
      userResource.icon = MdPerson
    } else {
      userResource.icon = MdSupervisorAccount
    }
    return renderSettingsMenuItemLink(
      userResource,
      permissions !== 'admin' ? localStorage.getItem('userId') : null,
    )
  }

  const renderSettingsMenuItemLink = (resource, id) => {
    const label = translate(`resources.${resource.name}.name`, {
      smart_count: id ? 1 : 2,
    })
    const link = id ? `/${resource.name}/${id}` : `/${resource.name}`
    return (
      <MenuItemLink
        className={classes.root}
        activeClassName={classes.active}
        key={resource.name}
        to={link}
        primaryText={label}
        leftIcon={
          (resource.icon && createElement(resource.icon, { size: 24 })) || (
            <ViewListIcon />
          )
        }
        onClick={onClick}
        sidebarIsOpen={true}
      />
    )
  }

  return (
    <>
      {config.devActivityPanel &&
        permissions === 'admin' &&
        config.enableNowPlaying && <NowPlayingPanel />}
      {config.devActivityPanel && permissions === 'admin' && <ActivityPanel />}
      <UserMenu
        {...rest}
        beforeContent={
          showOnlineSearch ? (
            <Tooltip title={translate('menu.download', { _: '下载管理' })}>
              <IconButton
                className={classes.root}
                aria-label={translate('menu.download', { _: '下载管理' })}
                onClick={handleToggleDownloadList}
              >
                <Badge
                  classes={{ root: classes.downloadBadge }}
                  overlap="circle"
                  anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
                  badgeContent={downloadTaskState.activeCount > 0 ? downloadTaskState.activeCount : null}
                >
                  <GetAppIcon style={{ color: 'white' }} />
                </Badge>
              </IconButton>
            </Tooltip>
          ) : null
        }
      >
        <PersonalMenu sidebarIsOpen={true} onClick={onClick} />
        <Divider />
        {renderUserMenuItemLink()}
        {resources
          .filter(settingsResources)
          .map((r) => renderSettingsMenuItemLink(r))}
        {permissions === 'admin' && (
          <MenuItemLink
            className={classes.root}
            activeClassName={classes.active}
            to="/online"
            primaryText={translate('menu.online', { _: 'Online' })}
            leftIcon={<MdPublic size={24} />}
            onClick={onClick}
            sidebarIsOpen={true}
          />
        )}
        <Divider />
        <AboutMenuItem />
      </UserMenu>
      <DownloadList
        open={downloadListOpen}
        onClose={handleCloseDownloadList}
        tasks={downloadTaskState.tasks}
        totalSpeed={downloadTaskState.totalSpeedText}
        totalProgress={downloadTaskState.totalProgress}
        onRetryAll={handleRetryAll}
        onCancelAll={handleCancelAll}
        onClearCompleted={handleClearCompleted}
        onToggleTask={handleToggleTask}
      />
      <Dialogs />
    </>
  )
}

const AppBar = (props) => (
  <RAAppBar {...props} container={Fragment} userMenu={<CustomUserMenu />} />
)

export default AppBar
