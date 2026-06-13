import React from 'react'
import { Route } from 'react-router-dom'
import Personal from './personal/Personal'
import OnlineSetting from './online/Online_setting'
import OnlineSearch from './online/Online_search'

const routes = [
  <Route exact path="/personal" render={() => <Personal />} key={'personal'} />,
  <Route
    exact
    path="/online"
    render={() => <OnlineSetting />}
    key={'online'}
  />,
  <Route
    exact
    path="/online/search"
    render={() => <OnlineSearch />}
    key={'online-search'}
  />,
]

export default routes
