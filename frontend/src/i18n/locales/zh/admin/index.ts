import overview from './overview'
import channels from './channels'
import accounts from './accounts'
import resources from './resources'
import ops from './ops'
import settings from './settings'
import upstreamConfigs from './upstreamConfigs'
import upstreamManagement from './upstreamManagement'
import upstreamDashboard from './upstreamDashboard'
import audit from './audit'
import promptAudit from './promptAudit'
import plugins from './plugins'

export default {
  ...overview,
  ...channels,
  ...accounts,
  ...resources,
  ...ops,
  ...settings,
  ...upstreamConfigs,
  ...upstreamManagement,
  ...upstreamDashboard,
  ...audit,
  ...promptAudit,
  ...plugins,
}
