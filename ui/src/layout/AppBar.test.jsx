import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, beforeEach, vi } from 'vitest'
import { Provider } from 'react-redux'
import { createStore, combineReducers } from 'redux'
import { activityReducer } from '../reducers'
import AppBar from './AppBar'
import config from '../config'

let store
const onlineMocks = vi.hoisted(() => ({ httpClient: vi.fn() }))

vi.mock('../dataProvider', () => ({ httpClient: onlineMocks.httpClient }))

vi.mock('react-admin', () => ({
  AppBar: ({ userMenu }) => <div data-testid="appbar">{userMenu}</div>,
  useTranslate: () => (x) => x,
  usePermissions: () => ({ permissions: 'admin' }),
  getResources: () => [],
  MenuItemLink: ({ primaryText }) => <span>{primaryText}</span>,
}))

vi.mock('./NowPlayingPanel', () => ({
  default: () => <div data-testid="now-playing-panel" />,
}))
vi.mock('./ActivityPanel', () => ({
  default: () => <div data-testid="activity-panel" />,
}))
vi.mock('./PersonalMenu', () => ({
  default: () => <div />,
}))
vi.mock('./UserMenu', () => ({
  default: ({ children }) => <div>{children}</div>,
}))
vi.mock('../dialogs/Dialogs', () => ({
  Dialogs: () => <div />,
}))
vi.mock('../dialogs', () => ({
  AboutDialog: () => <div />,
  QuickConnectDialog: () => <div />,
}))

describe('<AppBar />', () => {
  beforeEach(() => {
    onlineMocks.httpClient.mockReset()
    onlineMocks.httpClient.mockResolvedValue({
      json: { hasEnabledSource: false },
    })
    config.devActivityPanel = true
    config.enableNowPlaying = true
    config.enableQuickConnect = false
    store = createStore(combineReducers({ activity: activityReducer }), {
      activity: { nowPlayingCount: 0 },
    })
  })

  it('renders NowPlayingPanel when enabled', () => {
    render(
      <Provider store={store}>
        <AppBar />
      </Provider>,
    )
    expect(screen.getByTestId('now-playing-panel')).toBeInTheDocument()
  })

  it('loads sync tasks without requesting server downloads when only sync is allowed', async () => {
    onlineMocks.httpClient.mockImplementation((url) =>
      Promise.resolve({
        json: url.endsWith('/status')
          ? {
            hasEnabledSource: true,
            permissions: { search: true, sync: true, server: false },
          }
          : { tasks: [] },
      }),
    )
    render(
      <Provider store={store}>
        <AppBar />
      </Provider>,
    )
    await waitFor(() =>
      expect(onlineMocks.httpClient).toHaveBeenCalledWith(
        '/api/online/playlist/sync/tasks',
      ),
    )
    expect(onlineMocks.httpClient).not.toHaveBeenCalledWith(
      '/api/online/download/tasks',
    )
  })

  it('hides NowPlayingPanel when disabled', () => {
    config.enableNowPlaying = false
    render(
      <Provider store={store}>
        <AppBar />
      </Provider>,
    )
    expect(screen.queryByTestId('now-playing-panel')).toBeNull()
  })

  it('shows the Quick Connect menu item when enabled', () => {
    config.enableQuickConnect = true
    render(
      <Provider store={store}>
        <AppBar />
      </Provider>,
    )
    expect(screen.queryAllByText('menu.quickConnect.name')).not.toHaveLength(0)
  })

  it('hides the Quick Connect menu item when disabled', () => {
    render(
      <Provider store={store}>
        <AppBar />
      </Provider>,
    )
    expect(screen.queryAllByText('menu.quickConnect.name')).toHaveLength(0)
    expect(screen.queryAllByText('menu.about')).not.toHaveLength(0)
  })
})
