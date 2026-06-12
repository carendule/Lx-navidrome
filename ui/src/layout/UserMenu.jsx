import * as React from 'react'
import {
  Children,
  cloneElement,
  isValidElement,
  useEffect,
  useState,
} from 'react'
import PropTypes from 'prop-types'
import { useTranslate, useGetIdentity } from 'react-admin'
import {
  Tooltip,
  IconButton,
  Popover,
  MenuList,
  Avatar,
  Card,
  CardContent,
  Divider,
  Typography,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import AccountCircle from '@material-ui/icons/AccountCircle'
import config from '../config'
import authProvider from '../authProvider'
import { startEventStream } from '../eventStream'
import { useDispatch } from 'react-redux'

const useStyles = makeStyles((theme) => ({
  user: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(0.5),
  },
  button: {
    color: 'inherit',
  },
  avatar: {
    width: theme.spacing(4),
    height: theme.spacing(4),
  },
  username: {
    maxWidth: '11em',
    marginTop: '-0.7em',
    marginBottom: '-1em',
  },
  usernameWrap: {
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
  },
}))

const UserMenu = (props) => {
  const [anchorEl, setAnchorEl] = useState(null)
  const translate = useTranslate()
  const { loaded, identity } = useGetIdentity()
  const classes = useStyles(props)
  const dispatch = useDispatch()

  const { children, label, icon, logout, beforeContent } = props

  useEffect(() => {
    if (config.devActivityPanel) {
      authProvider
        .checkAuth()
        .then(() => startEventStream(dispatch))
        .catch(() => {})
    }
  }, [dispatch])

  if (!logout && !children) return null
  const open = Boolean(anchorEl)

  const handleMenu = (event) => setAnchorEl(event.currentTarget)
  const handleClose = () => setAnchorEl(null)

  return (
    <div className={classes.user}>
      {beforeContent || null}
      <Tooltip title={label && translate(label, { _: label })}>
        <IconButton
          className={classes.button}
          aria-label={label && translate(label, { _: label })}
          aria-owns={open ? 'menu-appbar' : null}
          aria-haspopup={true}
          onClick={handleMenu}
        >
          {loaded && identity.avatar ? (
            <Avatar
              className={classes.avatar}
              src={identity.avatar}
              alt={identity.fullName}
            />
          ) : (
            icon
          )}
        </IconButton>
      </Tooltip>
      <Popover
        id="menu-appbar"
        anchorEl={anchorEl}
        anchorOrigin={{
          vertical: 'bottom',
          horizontal: 'right',
        }}
        transformOrigin={{
          vertical: 'top',
          horizontal: 'right',
        }}
        open={open}
        onClose={handleClose}
      >
        <MenuList>
          {loaded && (
            <Card elevation={0} className={classes.username}>
              <CardContent className={classes.usernameWrap}>
                <Typography variant={'button'}>{identity.fullName}</Typography>
              </CardContent>
            </Card>
          )}
          <Divider />
          {Children.map(children, (menuItem) =>
            isValidElement(menuItem)
              ? cloneElement(menuItem, {
                  onClick: handleClose,
                })
              : null,
          )}
          {(!config.auth || !!config.extAuthLogoutURL) && logout}
        </MenuList>
      </Popover>
    </div>
  )
}

UserMenu.propTypes = {
  beforeContent: PropTypes.node,
  children: PropTypes.node,
  label: PropTypes.string.isRequired,
  logout: PropTypes.element,
}

UserMenu.defaultProps = {
  label: 'menu.settings',
  icon: <AccountCircle />,
}

export default UserMenu
