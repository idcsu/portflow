import {
  Activity,
  ArrowDownToLine,
  ArrowUpFromLine,
  Bell,
  Boxes,
  Cable,
  ChevronDown,
  CheckCircle2,
  CircleDot,
  Copy,
  Gauge,
  LayoutDashboard,
  ListChecks,
  KeyRound,
  LogOut,
  Menu,
  Moon,
  Network,
  Plus,
  Search,
  Settings,
  ShieldCheck,
  Sun,
  TerminalSquare,
  Trash2,
  Users,
  X,
} from 'lucide-react'
import { type ReactNode, useEffect, useMemo, useRef, useState } from 'react'

type Summary = {
  nodes: { online: number; offline: number }
  rules: { healthy: number; degraded: number; stopped: number }
  connections: number
  trafficToday: { uploadBytes: number; downloadBytes: number }
  source?: string
}

type User = { id: string; username: string; role: 'admin' | 'member'; disabled: boolean; mfaEnabled: boolean; createdAt: string }
type MemberItem = User
type NodeItem = { id: string; name: string; region: string; publicIp: string; tunnelAddress?: string; status: 'online' | 'offline' | 'disabled'; architecture: string; agentVersion: string; lastHeartbeat: string; configVersion: number; appliedConfigVersion: number; attemptedConfigVersion: number; lastConfigError?: string; cpuPercent: number; memoryPercent: number; loadOne: number; diskPercent: number; networkRxBps: number; networkTxBps: number; activeConnections: number }
type ForwardRule = { id: string; name: string; protocol: 'tcp' | 'udp' | 'tcp_udp'; mode: 'direct' | 'relay'; ingressNodeId: string; egressNodeId?: string; listenHost: string; listenPort: number; targetHost: string; targetPort: number; relayHost?: string; relayPort?: number; enabled: boolean; bandwidthKbps: number; maxConnections: number; allowCidrs?: string[]; denyCidrs?: string[]; configVersion: number; egressConfigVersion?: number; activeConnections: number; bytesIn: number; bytesOut: number }
type RuleForm = { name: string; protocol: 'tcp' | 'udp' | 'tcp_udp'; mode: 'direct' | 'relay'; ingressNodeId: string; egressNodeId: string; listenHost: string; listenPort: string; targetHost: string; targetPort: string; relayPort: string; bandwidthKbps: string; maxConnections: string; allowCidrs: string; denyCidrs: string; enabled: boolean }
type TrafficPoint = { time: string; uploadBytes: number; downloadBytes: number }
type TrafficHistory = { from: string; to: string; intervalSeconds: number; uploadBytes: number; downloadBytes: number; points: TrafficPoint[] }
type NodeMetricPoint = TrafficPoint & { cpuPercent: number; memoryPercent: number; loadOne: number; diskPercent: number; networkRxBps: number; networkTxBps: number; activeConnections: number }
type NodeDetail = { node: NodeItem; rules: ForwardRule[]; from: string; to: string; intervalSeconds: number; uploadBytes: number; downloadBytes: number; points: NodeMetricPoint[] }
type AuditEvent = { id: string; actorType: string; actorId?: string; action: string; targetType: string; targetId?: string; remoteIp?: string; details?: Record<string, unknown>; createdAt: string }
type AgentLog = { nodeId: string; id: string; level: 'info' | 'warning' | 'error'; component: string; message: string; occurredAt: string; receivedAt: string }
type LiveConnection = { nodeId: string; id: string; ruleId: string; protocol: 'tcp' | 'udp'; sourceAddress: string; targetAddress: string; startedAt: string; lastActivity: string; bytesIn: number; bytesOut: number; observedAt: string }
type SystemSettings = {
  runtime: { version: string; startedAt: string; serverTime: string; uptimeSeconds: number }
  security: { secureCookies: boolean; httpOnlyCookies: boolean; sameSite: string; totpMfa: boolean; sessionTtlSeconds: number; passwordMinLength: number; passwordMaxLength: number; loginFailureLimit: number; loginFailureWindowSeconds: number }
  agents: { heartbeatIntervalSeconds: number; offlineAfterSeconds: number; maxConnectionsPerHeartbeat: number; maxLogsPerHeartbeat: number; maxStoredConnectionsPerNode: number }
  retention: { nodeMetricsDays: number; agentLogsDays: number; auditEventsAutoCleanup: boolean; activeConnectionsMode: string }
  deployment: { ready: boolean; storageMode: string; httpsObserved: boolean; activeAdministrators: number; checks: Array<{ id: string; label: string; status: 'pass' | 'fail'; detail: string }> }
}
type View = 'dashboard' | 'nodes' | 'rules' | 'connections' | 'monitoring' | 'logs' | 'audit' | 'members' | 'settings'
type Theme = 'light' | 'dark'

const fallbackSummary: Summary = {
  nodes: { online: 0, offline: 0 },
  rules: { healthy: 0, degraded: 0, stopped: 0 },
  connections: 0,
  trafficToday: { uploadBytes: 0, downloadBytes: 0 },
  source: 'loading',
}

const emptyTraffic: TrafficHistory = { from: '', to: '', intervalSeconds: 1800, uploadBytes: 0, downloadBytes: 0, points: [] }
const installerVersion = '1.1.2'
const installerRepository = 'idcsu/portflow'

function shellQuote(value: string) {
  return `'${value.replaceAll("'", "'\\\"'\\\"")}'`
}

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** unitIndex
  return `${value.toFixed(unitIndex === 0 || value >= 10 ? 0 : 1)} ${units[unitIndex]}`
}

function formatDuration(startedAt: string) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000))
  if (seconds < 60) return `${seconds} 秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`
  const hours = Math.floor(seconds / 3600)
  return `${hours} 小时 ${Math.floor(seconds % 3600 / 60)} 分`
}

function formatUptime(seconds: number) {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor(seconds % 86400 / 3600)
  const minutes = Math.floor(seconds % 3600 / 60)
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${minutes} 分`
  return `${minutes} 分钟`
}

function auditActionLabel(action: string) {
  const labels: Record<string, string> = {
    'user.bootstrap': '初始化管理员', 'auth.login': '用户登录', 'auth.login_failed': '登录失败',
    'auth.logout': '用户退出', 'enrollment_token.create': '创建注册令牌', 'agent.enroll': '节点注册',
    'forward_rule.create': '创建转发线路', 'forward_rule.update': '修改转发线路', 'forward_rule.delete': '删除转发线路',
    'user.create': '创建成员', 'user.update': '修改成员权限', 'user.delete': '删除成员',
    'auth.mfa_setup': '开始设置二次验证', 'auth.mfa_enable': '启用二次验证',
    'auth.mfa_disable': '关闭二次验证', 'auth.mfa_failed': '二次验证码错误',
  }
  return labels[action] ?? action
}

function auditCategory(action: string) {
  if (action.startsWith('auth.')) return 'security'
  if (action.startsWith('user.')) return 'members'
  if (action.startsWith('agent.') || action.startsWith('enrollment_token.')) return 'nodes'
  if (action.startsWith('forward_rule.')) return 'rules'
  return 'other'
}

function auditTypeLabel(value: string) {
  return ({ user: '成员账号', anonymous: '未登录访问', agent: '节点程序', session: '登录会话', node: '节点', forward_rule: '转发线路', enrollment_token: '节点注册令牌' } as Record<string, string>)[value] ?? '系统对象'
}

function auditDetailsLabel(details?: Record<string, unknown>) {
  if (!details || Object.keys(details).length === 0) return '没有附加信息'
  const labels: Record<string, string> = { username: '用户名', role: '新角色', previousRole: '原角色', disabled: '是否禁用', previousDisabled: '原禁用状态', passwordReset: '已重置密码', mfaReset: '已重置二次验证', name: '名称' }
  return Object.entries(details).map(([key, value]) => `${labels[key] ?? key}：${value === true ? '是' : value === false ? '否' : String(value)}`).join('；')
}

function logComponentLabel(component: string) {
  const normalized = component.toLowerCase()
  if (normalized.includes('forward') || normalized.includes('tcp') || normalized.includes('udp')) return '转发引擎'
  if (normalized.includes('config')) return '配置同步'
  if (normalized.includes('heartbeat') || normalized.includes('control')) return '控制面连接'
  if (normalized.includes('agent')) return '节点服务'
  return component || '节点服务'
}

function friendlyError(body: any, fallback: string) {
  const code = body?.error?.code
  const labels: Record<string, string> = {
    invalid_credentials: '用户名或密码不正确', login_rate_limited: '登录失败次数过多，请 15 分钟后再试',
    invalid_mfa_code: '验证码不正确或已过期，请输入验证器里最新的 6 位数字', invalid_password: '当前密码不正确',
    last_administrator: '必须至少保留一个启用中的管理员', self_modification: '不能在成员列表修改或删除当前登录账号',
    username_conflict: '这个用户名已经存在', setup_completed: '管理员已经初始化，请直接登录',
    permission_denied: '当前账号没有执行这项操作的权限', authentication_required: '登录已经过期，请重新登录', invalid_session: '登录已经过期，请重新登录',
    invalid_name: '名称不能为空，且不能超过 80 个字符', invalid_role: '请选择管理员或普通成员',
    invalid_rule: '线路配置不完整或数值超出允许范围，请检查标红项目', invalid_listen_host: '监听地址格式不正确', invalid_target_host: '目标地址格式不正确',
    listener_conflict: '所选节点上的监听地址、端口和协议已被另一条线路使用', relay_not_provisioned: '入口或出口节点尚未配置加密隧道地址，暂时不能启用',
    sensitive_target: '为防止访问云平台敏感地址，目标地址已被安全策略拒绝', unsupported_rule: '当前节点版本暂不支持这项线路配置',
    invalid_expiration: '有效期必须在 5 到 1440 分钟之间', mfa_already_enabled: '二次验证已经开启，如需重新绑定请先关闭', mfa_setup_required: '请先开始二次验证设置',
  }
  return labels[code] ?? body?.error?.message ?? fallback
}

const emptyRuleForm = (): RuleForm => ({ name: '', protocol: 'tcp', mode: 'direct', ingressNodeId: '', egressNodeId: '', listenHost: '0.0.0.0', listenPort: '', targetHost: '', targetPort: '', relayPort: '', bandwidthKbps: '0', maxConnections: '0', allowCidrs: '', denyCidrs: '', enabled: true })

function Sparkline({ history, label = '最近 24 小时流量趋势' }: { history: TrafficHistory; label?: string }) {
  const values = history.points.map((point) => point.uploadBytes + point.downloadBytes)
  const chartValues = values.length > 1 ? values : [0, values[0] ?? 0]
  const maximum = Math.max(...chartValues, 1)
  const points = useMemo(() => chartValues.map((value, index) => `${(index / (chartValues.length - 1)) * 100},${96 - (value / maximum) * 82}`).join(' '), [history.points])
  const labels = history.points.length > 1
    ? [history.points[0], history.points[Math.floor(history.points.length / 2)], history.points[history.points.length - 1]].map((point) => new Date(point.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))
    : ['24 小时前', '12 小时前', '现在']
  return (
    <div className="chart-shell" aria-label={label}>
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" role="img">
        <defs>
          <linearGradient id="trafficFill" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor="#61e7bd" stopOpacity=".34" />
            <stop offset="100%" stopColor="#61e7bd" stopOpacity="0" />
          </linearGradient>
        </defs>
        <path d={`M0,100 L${points} L100,100 Z`} fill="url(#trafficFill)" />
        <polyline points={points} fill="none" stroke="#61e7bd" strokeWidth="1.35" vectorEffect="non-scaling-stroke" />
      </svg>
      <div className="chart-grid" />
      <div className="chart-labels"><span>{labels[0]}</span><span>{labels[1]}</span><span>{labels[2]}</span></div>
    </div>
  )
}

function ResourceSparkline({ points }: { points: NodeMetricPoint[] }) {
  const chartPoints = points.length > 1 ? points : [{ cpuPercent: 0, memoryPercent: 0, diskPercent: 0 } as NodeMetricPoint, points[0] ?? { cpuPercent: 0, memoryPercent: 0, diskPercent: 0 } as NodeMetricPoint]
  const line = (field: 'cpuPercent' | 'memoryPercent' | 'diskPercent') => chartPoints.map((point, index) => `${(index / (chartPoints.length - 1)) * 100},${96 - Math.min(point[field], 100) * .82}`).join(' ')
  const labels = points.length > 1
    ? [points[0], points[Math.floor(points.length / 2)], points[points.length - 1]].map((point) => new Date(point.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))
    : ['范围开始', '历史采样', '现在']
  return <div className="chart-shell resource-chart" aria-label="节点 CPU 和内存趋势">
    <svg viewBox="0 0 100 100" preserveAspectRatio="none" role="img">
      <polyline points={line('cpuPercent')} fill="none" stroke="#61e7bd" strokeWidth="1.35" vectorEffect="non-scaling-stroke" />
      <polyline points={line('memoryPercent')} fill="none" stroke="#6c9cff" strokeWidth="1.35" vectorEffect="non-scaling-stroke" />
      <polyline points={line('diskPercent')} fill="none" stroke="#f3bd68" strokeWidth="1.35" vectorEffect="non-scaling-stroke" />
    </svg>
    <div className="chart-grid" /><div className="chart-labels"><span>{labels[0]}</span><span>{labels[1]}</span><span>{labels[2]}</span></div>
  </div>
}

function AnimatedPopover({ open, className, children, role = 'dialog' }: { open: boolean; className: string; children: ReactNode; role?: 'dialog' | 'menu' | 'listbox' }) {
  const [rendered, setRendered] = useState(open)
  const [visible, setVisible] = useState(false)
  const visibleChildren = useRef(children)
  if (open) visibleChildren.current = children

  useEffect(() => {
    if (open) {
      setRendered(true)
      const frame = window.requestAnimationFrame(() => setVisible(true))
      return () => window.cancelAnimationFrame(frame)
    }
    setVisible(false)
    if (!rendered) return
    const timer = window.setTimeout(() => setRendered(false), 180)
    return () => window.clearTimeout(timer)
  }, [open, rendered])

  if (!rendered) return null
  return <div className={`top-popover ${className} ${visible ? 'is-open' : 'is-closing'}`} role={role} aria-hidden={!open}>{visibleChildren.current}</div>
}

function LoginScreen({ onAuthenticated }: { onAuthenticated: (user: User) => void }) {
  const [setup, setSetup] = useState(false)
  const [setupRequired, setSetupRequired] = useState(false)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [mfaRequired, setMfaRequired] = useState(false)
  const [mfaCode, setMfaCode] = useState('')
  const [useRecoveryCode, setUseRecoveryCode] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    fetch('/api/v1/setup/status').then((response) => response.ok ? response.json() : Promise.reject())
      .then((body) => { setSetupRequired(Boolean(body.required)); setSetup(Boolean(body.required)) })
      .catch(() => setSetupRequired(false))
  }, [])

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      if (setup) {
        const setupResponse = await fetch('/api/v1/setup/admin', {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }),
        })
        const setupBody = await setupResponse.json()
        if (!setupResponse.ok) throw new Error(friendlyError(setupBody, '初始化失败'))
        setSetupRequired(false)
        setSetup(false)
      }
      const response = await fetch('/api/v1/auth/login', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password, code: useRecoveryCode ? '' : mfaCode, recoveryCode: useRecoveryCode ? mfaCode : '' }),
      })
      const body = await response.json()
      if (response.status === 202 && body.mfaRequired) {
        setMfaRequired(true)
        setMfaCode('')
        return
      }
      if (!response.ok) throw new Error(friendlyError(body, '登录失败'))
      onAuthenticated(body.user as User)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '请求失败，请稍后重试')
    } finally {
      setBusy(false)
    }
  }

  return <main className="auth-page">
    <div className="auth-ambient ambient-one" /><div className="auth-ambient ambient-two" />
    <section className="auth-card">
      <div className="auth-brand"><div className="brand-mark"><CircleDot size={22} /></div><div><b>PortFlow</b><span>网络控制台</span></div></div>
      <div className="auth-heading"><p>{setup ? '首次使用设置' : mfaRequired ? '二次身份验证' : '安全登录'}</p><h1>{setup ? '初始化管理员' : mfaRequired ? '确认是你本人' : '欢迎回来'}</h1><span>{setup ? '创建系统中的第一个管理员账号，这个步骤只会出现一次。' : mfaRequired ? '打开手机验证器，输入 PortFlow 对应的最新验证码。' : '登录后管理节点、线路与实时状态。'}</span></div>
      <form onSubmit={submit}>
        {!mfaRequired && <><label><span>用户名</span><input autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} placeholder="operator" required /></label>
        <label><span>密码</span><input type="password" autoComplete={setup ? 'new-password' : 'current-password'} value={password} onChange={(event) => setPassword(event.target.value)} placeholder="至少 12 个字符" minLength={12} required /></label></>}
        {mfaRequired && <label><span>{useRecoveryCode ? '一次性恢复码' : '6 位动态验证码'}</span><input className="otp-input" inputMode={useRecoveryCode ? 'text' : 'numeric'} autoComplete="one-time-code" value={mfaCode} onChange={(event) => setMfaCode(event.target.value)} placeholder={useRecoveryCode ? 'XXXX-XXXX-XXXX' : '000000'} minLength={6} required autoFocus /></label>}
        {error && <div className="auth-error">{error}</div>}
        <button className="auth-submit" disabled={busy}>{busy ? '正在验证…' : setup ? '初始化并登录' : mfaRequired ? '验证并进入控制台' : '安全登录'}</button>
      </form>
      {mfaRequired && <button className="auth-switch" onClick={() => { setUseRecoveryCode(!useRecoveryCode); setMfaCode(''); setError('') }}>{useRecoveryCode ? '使用手机验证器' : '手机不在身边？使用恢复码'}</button>}
      {mfaRequired && <button className="auth-switch subtle" onClick={() => { setMfaRequired(false); setMfaCode(''); setError('') }}>返回账号密码登录</button>}
      {!mfaRequired && setupRequired && <button className="auth-switch" onClick={() => { setSetup(!setup); setError('') }}>{setup ? '已有管理员？返回登录' : '首次部署？初始化管理员'}</button>}
      <div className="auth-foot"><ShieldCheck size={15} /><span>会话凭证仅保存在 HttpOnly Cookie 中</span></div>
    </section>
  </main>
}

function App() {
  const [summary, setSummary] = useState(fallbackSummary)
  const [activeView, setActiveView] = useState<View>('dashboard')
  const [trafficHistory, setTrafficHistory] = useState<TrafficHistory>(emptyTraffic)
  const [auditItems, setAuditItems] = useState<AuditEvent[]>([])
  const [auditNextBefore, setAuditNextBefore] = useState<string | null>(null)
  const [auditLoading, setAuditLoading] = useState(false)
  const [auditError, setAuditError] = useState('')
  const [auditCategoryFilter, setAuditCategoryFilter] = useState('all')
  const [logItems, setLogItems] = useState<AgentLog[]>([])
  const [logNextBefore, setLogNextBefore] = useState<string | null>(null)
  const [logNodeFilter, setLogNodeFilter] = useState('')
  const [logLevelFilter, setLogLevelFilter] = useState('')
  const [logLoading, setLogLoading] = useState(false)
  const [logError, setLogError] = useState('')
  const [connectionItems, setConnectionItems] = useState<LiveConnection[]>([])
  const [connectionNodeFilter, setConnectionNodeFilter] = useState('')
  const [connectionProtocolFilter, setConnectionProtocolFilter] = useState('')
  const [connectionLoading, setConnectionLoading] = useState(false)
  const [connectionError, setConnectionError] = useState('')
  const [memberItems, setMemberItems] = useState<MemberItem[]>([])
  const [memberLoading, setMemberLoading] = useState(false)
  const [memberError, setMemberError] = useState('')
  const [showMemberEditor, setShowMemberEditor] = useState(false)
  const [editingMemberId, setEditingMemberId] = useState<string | null>(null)
  const [memberUsername, setMemberUsername] = useState('')
  const [memberPassword, setMemberPassword] = useState('')
  const [memberRole, setMemberRole] = useState<'admin' | 'member'>('member')
  const [memberDisabled, setMemberDisabled] = useState(false)
  const [memberResetMFA, setMemberResetMFA] = useState(false)
  const [savingMember, setSavingMember] = useState(false)
  const [systemSettings, setSystemSettings] = useState<SystemSettings | null>(null)
  const [settingsLoading, setSettingsLoading] = useState(false)
  const [settingsError, setSettingsError] = useState('')
  const [menuOpen, setMenuOpen] = useState(false)
  const [notificationOpen, setNotificationOpen] = useState(false)
  const [accountMenuOpen, setAccountMenuOpen] = useState(false)
  const [showSecurity, setShowSecurity] = useState(false)
  const [securityPassword, setSecurityPassword] = useState('')
  const [securityCode, setSecurityCode] = useState('')
  const [securityUseRecovery, setSecurityUseRecovery] = useState(false)
  const [securitySecret, setSecuritySecret] = useState('')
  const [securityURI, setSecurityURI] = useState('')
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([])
  const [securityBusy, setSecurityBusy] = useState(false)
  const [securityError, setSecurityError] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [theme, setTheme] = useState<Theme>(() => localStorage.getItem('portflow-theme') === 'dark' ? 'dark' : 'light')
  const searchInputRef = useRef<HTMLInputElement>(null)
  const searchShellRef = useRef<HTMLDivElement>(null)
  const notificationShellRef = useRef<HTMLDivElement>(null)
  const accountShellRef = useRef<HTMLDivElement>(null)
  const [apiOnline, setApiOnline] = useState(false)
  const [user, setUser] = useState<User | null>(null)
  const [checkingAuth, setCheckingAuth] = useState(true)
  const [nodeItems, setNodeItems] = useState<NodeItem[]>([])
  const [selectedNodeId, setSelectedNodeId] = useState('')
  const [nodeDetail, setNodeDetail] = useState<NodeDetail | null>(null)
  const [nodeHours, setNodeHours] = useState(24)
  const [nodeDetailLoading, setNodeDetailLoading] = useState(false)
  const [nodeDetailError, setNodeDetailError] = useState('')
  const [showEnrollment, setShowEnrollment] = useState(false)
  const [enrollmentName, setEnrollmentName] = useState('新节点注册')
  const [enrollmentNodeName, setEnrollmentNodeName] = useState('New Node')
  const [enrollmentRegion, setEnrollmentRegion] = useState('')
  const [enrollmentToken, setEnrollmentToken] = useState('')
  const [enrollmentError, setEnrollmentError] = useState('')
  const [creatingToken, setCreatingToken] = useState(false)
  const [ruleItems, setRuleItems] = useState<ForwardRule[]>([])
  const [showRuleEditor, setShowRuleEditor] = useState(false)
  const [editingRuleId, setEditingRuleId] = useState<string | null>(null)
  const [copyingRule, setCopyingRule] = useState(false)
  const [ruleForm, setRuleForm] = useState<RuleForm>(emptyRuleForm)
  const [ruleError, setRuleError] = useState('')
  const [savingRule, setSavingRule] = useState(false)

  async function loadControlData(signal?: AbortSignal) {
    const [summaryResponse, nodesResponse, rulesResponse, trafficResponse] = await Promise.all([
      fetch('/api/v1/dashboard/summary', { signal }),
      fetch('/api/v1/nodes', { signal }),
      fetch('/api/v1/forward-rules', { signal }),
      fetch('/api/v1/metrics/traffic?hours=24', { signal }),
    ])
    if (!summaryResponse.ok || !nodesResponse.ok || !rulesResponse.ok || !trafficResponse.ok) throw new Error('API unavailable')
    const [summaryValue, nodesValue, rulesValue, trafficValue] = await Promise.all([
      summaryResponse.json() as Promise<Summary>,
      nodesResponse.json() as Promise<{ items: NodeItem[] | null }>,
      rulesResponse.json() as Promise<{ items: ForwardRule[] | null }>,
      trafficResponse.json() as Promise<TrafficHistory>,
    ])
    setSummary(summaryValue)
    setNodeItems(nodesValue.items ?? [])
    setRuleItems(rulesValue.items ?? [])
    setTrafficHistory({ ...trafficValue, points: trafficValue.points ?? [] })
    setApiOnline(true)
  }

  async function loadNodeDetail(nodeId: string, hours = nodeHours) {
	if (nodeId !== selectedNodeId) setNodeDetail(null)
    setSelectedNodeId(nodeId)
    setNodeDetailLoading(true)
    setNodeDetailError('')
    try {
      const response = await fetch(`/api/v1/nodes/${encodeURIComponent(nodeId)}?hours=${hours}`)
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法读取节点详情'))
      setNodeDetail({ ...body, rules: body.rules ?? [], points: body.points ?? [] } as NodeDetail)
    } catch (reason) {
      setNodeDetailError(reason instanceof Error ? reason.message : '无法读取节点详情')
    } finally {
      setNodeDetailLoading(false)
    }
  }

  function openNode(nodeId: string) {
    setActiveView('nodes')
    setMenuOpen(false)
    setSearchQuery('')
    setNotificationOpen(false)
    setAccountMenuOpen(false)
    void loadNodeDetail(nodeId)
  }

  async function loadAudit(before?: string, append = false) {
    setAuditLoading(true)
    setAuditError('')
    try {
      const query = new URLSearchParams({ limit: '50' })
      if (before) query.set('before', before)
      const response = await fetch(`/api/v1/audit-events?${query}`)
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法读取审计记录'))
      setAuditItems((current) => append ? [...current, ...(body.items ?? [])] : (body.items ?? []))
      setAuditNextBefore(body.nextBefore ?? null)
    } catch (reason) {
      setAuditError(reason instanceof Error ? reason.message : '无法读取审计记录')
    } finally {
      setAuditLoading(false)
    }
  }

  async function loadLogs(before?: string, append = false) {
    setLogLoading(true)
    setLogError('')
    try {
      const query = new URLSearchParams({ limit: '100' })
      if (before) query.set('before', before)
      if (logNodeFilter) query.set('nodeId', logNodeFilter)
      if (logLevelFilter) query.set('level', logLevelFilter)
      const response = await fetch(`/api/v1/agent-logs?${query}`)
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法读取运行日志'))
      setLogItems((current) => append ? [...current, ...(body.items ?? [])] : (body.items ?? []))
      setLogNextBefore(body.nextBefore ?? null)
    } catch (reason) {
      setLogError(reason instanceof Error ? reason.message : '无法读取运行日志')
    } finally {
      setLogLoading(false)
    }
  }

  async function loadConnections() {
    setConnectionLoading(true)
    setConnectionError('')
    try {
      const response = await fetch('/api/v1/connections')
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法读取实时连接'))
      setConnectionItems(body.items ?? [])
    } catch (reason) {
      setConnectionError(reason instanceof Error ? reason.message : '无法读取实时连接')
    } finally {
      setConnectionLoading(false)
    }
  }

  async function loadMembers() {
    setMemberLoading(true)
    setMemberError('')
    try {
      const response = await fetch('/api/v1/users')
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法读取成员'))
      setMemberItems(body.items ?? [])
    } catch (reason) {
      setMemberError(reason instanceof Error ? reason.message : '无法读取成员')
    } finally {
      setMemberLoading(false)
    }
  }

  async function loadSystemSettings() {
    setSettingsLoading(true)
    setSettingsError('')
    try {
      const response = await fetch('/api/v1/system/settings')
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法读取系统设置'))
      setSystemSettings(body as SystemSettings)
    } catch (reason) {
      setSettingsError(reason instanceof Error ? reason.message : '无法读取系统设置')
    } finally {
      setSettingsLoading(false)
    }
  }

  function switchView(view: View) {
    setActiveView(view)
    setMenuOpen(false)
    setSearchQuery('')
    setNotificationOpen(false)
    setAccountMenuOpen(false)
    if (view === 'audit' && user?.role === 'admin' && auditItems.length === 0) void loadAudit()
    if (view === 'logs' && user?.role === 'admin' && logItems.length === 0) void loadLogs()
    if (view === 'connections') void loadConnections()
    if (view === 'members' && user?.role === 'admin') void loadMembers()
    if (view === 'settings' && user?.role === 'admin') void loadSystemSettings()
    if (view === 'nodes' && nodeItems.length > 0 && !selectedNodeId) void loadNodeDetail(nodeItems[0].id)
  }

  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/auth/me', { signal: controller.signal })
      .then((response) => {
        if (!response.ok) throw new Error('not authenticated')
        return response.json() as Promise<User>
      })
      .then((authenticatedUser) => {
        setUser(authenticatedUser)
        return loadControlData(controller.signal)
      })
      .catch(() => setApiOnline(false))
      .finally(() => setCheckingAuth(false))
    return () => controller.abort()
  }, [])

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    localStorage.setItem('portflow-theme', theme)
  }, [theme])

  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        searchInputRef.current?.focus()
      }
      if (event.key === 'Escape') {
        setSearchQuery('')
        setNotificationOpen(false)
        setAccountMenuOpen(false)
        setMenuOpen(false)
      }
    }
    window.addEventListener('keydown', focusSearch)
    return () => window.removeEventListener('keydown', focusSearch)
  }, [])

  useEffect(() => {
    if (!searchQuery.trim() && !notificationOpen && !accountMenuOpen) return
    const dismissOutside = (event: PointerEvent) => {
      const target = event.target as Node | null
      if (!target) return
      if (searchQuery.trim() && !searchShellRef.current?.contains(target)) setSearchQuery('')
      if (notificationOpen && !notificationShellRef.current?.contains(target)) setNotificationOpen(false)
      if (accountMenuOpen && !accountShellRef.current?.contains(target)) setAccountMenuOpen(false)
    }
    document.addEventListener('pointerdown', dismissOutside)
    return () => document.removeEventListener('pointerdown', dismissOutside)
  }, [searchQuery, notificationOpen, accountMenuOpen])

  useEffect(() => {
    if (!user) return
    const interval = window.setInterval(() => loadControlData().catch(() => setApiOnline(false)), 15_000)
    return () => window.clearInterval(interval)
  }, [user?.id])

  useEffect(() => {
    if (!user || activeView !== 'nodes' || !selectedNodeId) return
    const interval = window.setInterval(() => void loadNodeDetail(selectedNodeId, nodeHours), 30_000)
    return () => window.clearInterval(interval)
  }, [user?.id, activeView, selectedNodeId, nodeHours])

  useEffect(() => {
    if (!user || activeView !== 'connections') return
    const interval = window.setInterval(() => void loadConnections(), 15_000)
    return () => window.clearInterval(interval)
  }, [user?.id, activeView])

  async function logout() {
    await fetch('/api/v1/auth/logout', { method: 'POST' })
    setUser(null)
    setApiOnline(false)
    setActiveView('dashboard')
    setAuditItems([])
    setAuditNextBefore(null)
    setLogItems([])
    setLogNextBefore(null)
    setConnectionItems([])
    setMemberItems([])
    setSystemSettings(null)
    setSelectedNodeId('')
    setNodeDetail(null)
    setAccountMenuOpen(false)
    setNotificationOpen(false)
  }

  async function createEnrollmentToken(event: React.FormEvent) {
    event.preventDefault()
    setCreatingToken(true)
    setEnrollmentError('')
    try {
      const response = await fetch('/api/v1/enrollment-tokens', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: enrollmentName, expiresInMinutes: 30 }),
      })
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法创建注册令牌'))
      setEnrollmentToken(body.token)
    } catch (reason) {
      setEnrollmentError(reason instanceof Error ? reason.message : '无法创建注册令牌')
    } finally {
      setCreatingToken(false)
    }
  }

  function closeEnrollment() {
    setShowEnrollment(false)
    setEnrollmentToken('')
    setEnrollmentError('')
  }

  function enrollmentInstallCommand() {
    return `curl -fsSL https://raw.githubusercontent.com/${installerRepository}/v${installerVersion}/install.sh | sudo bash -s -- agent --repo ${installerRepository} --version ${installerVersion} --control-url ${shellQuote(window.location.origin)} --enrollment-token ${shellQuote(enrollmentToken)} --name ${shellQuote(enrollmentNodeName)} --region ${shellQuote(enrollmentRegion)}`
  }

  function openCreateRule() {
    setEditingRuleId(null)
    setCopyingRule(false)
    setRuleForm({ ...emptyRuleForm(), ingressNodeId: nodeItems.find((node) => node.status !== 'disabled')?.id ?? '' })
    setRuleError('')
    setShowRuleEditor(true)
  }

  function openEditRule(rule: ForwardRule) {
    setEditingRuleId(rule.id)
    setCopyingRule(false)
    setRuleForm({
      name: rule.name, protocol: rule.protocol, mode: rule.mode, ingressNodeId: rule.ingressNodeId, egressNodeId: rule.egressNodeId ?? '', listenHost: rule.listenHost,
      listenPort: String(rule.listenPort), targetHost: rule.targetHost, targetPort: String(rule.targetPort), relayPort: rule.relayPort ? String(rule.relayPort) : '',
      bandwidthKbps: String(rule.bandwidthKbps ?? 0), maxConnections: String(rule.maxConnections ?? 0), allowCidrs: (rule.allowCidrs ?? []).join('\n'),
      denyCidrs: (rule.denyCidrs ?? []).join('\n'), enabled: rule.enabled,
    })
    setRuleError('')
    setShowRuleEditor(true)
  }

  function openCopyRule(rule: ForwardRule) {
    setEditingRuleId(null)
    setCopyingRule(true)
    setRuleForm({
      name: `${rule.name} 副本`, protocol: rule.protocol, mode: rule.mode, ingressNodeId: rule.ingressNodeId, egressNodeId: rule.egressNodeId ?? '',
      listenHost: rule.listenHost, listenPort: '', targetHost: rule.targetHost, targetPort: String(rule.targetPort), relayPort: '',
      bandwidthKbps: String(rule.bandwidthKbps ?? 0), maxConnections: String(rule.maxConnections ?? 0), allowCidrs: (rule.allowCidrs ?? []).join('\n'),
      denyCidrs: (rule.denyCidrs ?? []).join('\n'), enabled: false,
    })
    setRuleError('')
    setShowRuleEditor(true)
  }

  function closeRuleEditor() {
    setShowRuleEditor(false)
    setEditingRuleId(null)
    setCopyingRule(false)
    setRuleError('')
  }

  async function saveRule(event: React.FormEvent) {
    event.preventDefault()
    setSavingRule(true)
    setRuleError('')
    const splitCIDRs = (value: string) => value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean)
    const payload = {
      name: ruleForm.name, protocol: ruleForm.protocol, mode: ruleForm.mode, ingressNodeId: ruleForm.ingressNodeId,
      egressNodeId: ruleForm.mode === 'relay' ? ruleForm.egressNodeId : '',
      listenHost: ruleForm.listenHost, listenPort: Number(ruleForm.listenPort), targetHost: ruleForm.targetHost,
      targetPort: Number(ruleForm.targetPort), relayPort: ruleForm.mode === 'relay' ? Number(ruleForm.relayPort || ruleForm.listenPort) : 0, enabled: ruleForm.enabled, bandwidthKbps: Number(ruleForm.bandwidthKbps || 0),
      maxConnections: Number(ruleForm.maxConnections || 0), allowCidrs: splitCIDRs(ruleForm.allowCidrs), denyCidrs: splitCIDRs(ruleForm.denyCidrs),
    }
    try {
      const response = await fetch(editingRuleId ? `/api/v1/forward-rules/${editingRuleId}` : '/api/v1/forward-rules', {
        method: editingRuleId ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
      })
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法保存转发线路'))
      closeRuleEditor()
      await loadControlData()
    } catch (reason) {
      setRuleError(reason instanceof Error ? reason.message : '无法保存转发线路')
    } finally {
      setSavingRule(false)
    }
  }

  async function deleteRule(rule: ForwardRule) {
    if (!window.confirm(`确认删除线路“${rule.name}”？节点将在下一次同步后停止对应监听。`)) return
    const response = await fetch(`/api/v1/forward-rules/${rule.id}`, { method: 'DELETE' })
    if (!response.ok) {
      const body = await response.json().catch(() => null)
      window.alert(body?.error?.message ?? '删除失败')
      return
    }
    await loadControlData()
  }

  function openCreateMember() {
    setEditingMemberId(null)
    setMemberUsername('')
    setMemberPassword('')
    setMemberRole('member')
    setMemberDisabled(false)
    setMemberResetMFA(false)
    setMemberError('')
    setShowMemberEditor(true)
  }

  function openEditMember(member: MemberItem) {
    setEditingMemberId(member.id)
    setMemberUsername(member.username)
    setMemberPassword('')
    setMemberRole(member.role)
    setMemberDisabled(member.disabled)
    setMemberResetMFA(false)
    setMemberError('')
    setShowMemberEditor(true)
  }

  function closeMemberEditor() {
    setShowMemberEditor(false)
    setEditingMemberId(null)
    setMemberPassword('')
    setMemberResetMFA(false)
    setMemberError('')
  }

  async function saveMember(event: React.FormEvent) {
    event.preventDefault()
    setSavingMember(true)
    setMemberError('')
    try {
      const editing = Boolean(editingMemberId)
      const response = await fetch(editing ? `/api/v1/users/${editingMemberId}` : '/api/v1/users', {
        method: editing ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(editing
          ? { role: memberRole, disabled: memberDisabled, password: memberPassword, resetMfa: memberResetMFA }
          : { username: memberUsername, password: memberPassword, role: memberRole }),
      })
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法保存成员'))
      closeMemberEditor()
      await loadMembers()
    } catch (reason) {
      setMemberError(reason instanceof Error ? reason.message : '无法保存成员')
    } finally {
      setSavingMember(false)
    }
  }

  async function deleteMember(member: MemberItem) {
    if (!window.confirm(`确认永久删除成员“${member.username}”？该成员会立即退出登录，且此操作无法撤销。`)) return
    const response = await fetch(`/api/v1/users/${member.id}`, { method: 'DELETE' })
    if (!response.ok) {
      const body = await response.json().catch(() => null)
      window.alert(friendlyError(body, '删除成员失败'))
      return
    }
    await loadMembers()
  }

  function openSecurityCenter() {
    setAccountMenuOpen(false)
    setSecurityPassword('')
    setSecurityCode('')
    setSecurityUseRecovery(false)
    setSecuritySecret('')
    setSecurityURI('')
    setRecoveryCodes([])
    setSecurityError('')
    setShowSecurity(true)
  }

  function closeSecurityCenter() {
    setShowSecurity(false)
    setSecurityPassword('')
    setSecurityCode('')
    setSecurityUseRecovery(false)
    setSecuritySecret('')
    setSecurityURI('')
    setRecoveryCodes([])
    setSecurityError('')
  }

  async function startMFASetup(event: React.FormEvent) {
    event.preventDefault()
    setSecurityBusy(true)
    setSecurityError('')
    try {
      const response = await fetch('/api/v1/auth/mfa/setup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: securityPassword }) })
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法开始设置二次验证'))
      setSecuritySecret(body.secret)
      setSecurityURI(body.otpauthUrl)
      setSecurityCode('')
    } catch (reason) {
      setSecurityError(reason instanceof Error ? reason.message : '无法开始设置二次验证')
    } finally {
      setSecurityBusy(false)
    }
  }

  async function enableMFA(event: React.FormEvent) {
    event.preventDefault()
    setSecurityBusy(true)
    setSecurityError('')
    try {
      const response = await fetch('/api/v1/auth/mfa/enable', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: securityPassword, code: securityCode }) })
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法启用二次验证'))
      setRecoveryCodes(body.recoveryCodes ?? [])
      setUser((current) => current ? { ...current, mfaEnabled: true } : current)
      setSecuritySecret('')
      setSecurityURI('')
      setSecurityCode('')
    } catch (reason) {
      setSecurityError(reason instanceof Error ? reason.message : '无法启用二次验证')
    } finally {
      setSecurityBusy(false)
    }
  }

  async function disableMFA(event: React.FormEvent) {
    event.preventDefault()
    setSecurityBusy(true)
    setSecurityError('')
    try {
      const response = await fetch('/api/v1/auth/mfa/disable', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: securityPassword, code: securityUseRecovery ? '' : securityCode, recoveryCode: securityUseRecovery ? securityCode : '' }) })
      const body = await response.json()
      if (!response.ok) throw new Error(friendlyError(body, '无法关闭二次验证'))
      setUser((current) => current ? { ...current, mfaEnabled: false } : current)
      closeSecurityCenter()
    } catch (reason) {
      setSecurityError(reason instanceof Error ? reason.message : '无法关闭二次验证')
    } finally {
      setSecurityBusy(false)
    }
  }

  const routes = ruleItems.map((rule) => {
    const node = nodeItems.find((item) => item.id === rule.ingressNodeId)
    const egressNode = nodeItems.find((item) => item.id === rule.egressNodeId)
    const ingressApplied = Boolean(node && node.status === 'online' && node.appliedConfigVersion >= rule.configVersion)
    const egressApplied = rule.mode === 'direct' || Boolean(egressNode && egressNode.status === 'online' && egressNode.appliedConfigVersion >= (rule.egressConfigVersion ?? Number.MAX_SAFE_INTEGER))
    const applied = ingressApplied && egressApplied
    const ingressFailed = Boolean(node?.lastConfigError && node.attemptedConfigVersion >= rule.configVersion && !ingressApplied)
    const egressFailed = rule.mode === 'relay' && Boolean(egressNode?.lastConfigError && egressNode.attemptedConfigVersion >= (rule.egressConfigVersion ?? Number.MAX_SAFE_INTEGER) && !egressApplied)
    const failed = ingressFailed || egressFailed
    const failureReason = ingressFailed ? `入口：${node?.lastConfigError}` : egressFailed ? `出口：${egressNode?.lastConfigError}` : undefined
    const state = !rule.enabled ? '已停止' : applied ? '已生效' : failed ? '应用失败' : node?.status === 'offline' || rule.mode === 'relay' && egressNode?.status === 'offline' ? '节点离线' : '等待同步'
    const path = rule.mode === 'relay'
      ? `${node?.name ?? '未知入口'}:${rule.listenPort} → ${egressNode?.name ?? '未知出口'} → ${rule.targetHost}:${rule.targetPort}`
      : `${node?.name ?? '未知节点'}:${rule.listenPort} → ${rule.targetHost}:${rule.targetPort}`
    return { ...rule, node, egressNode, path, state, failureReason }
  })

  const alertNotifications = [
    ...(!apiOnline ? [{ id: 'api', title: '控制面连接中断', detail: '页面暂时无法刷新实时数据', view: 'dashboard' as View, level: 'warning' }] : []),
    ...(summary.nodes.offline > 0 ? [{ id: 'nodes', title: `${summary.nodes.offline} 个节点离线`, detail: '请检查 Agent 服务或节点网络', view: 'nodes' as View, level: 'warning' }] : []),
    ...(summary.rules.degraded > 0 ? [{ id: 'rules', title: `${summary.rules.degraded} 条线路需要处理`, detail: '存在同步失败、节点离线或未生效线路', view: 'rules' as View, level: 'warning' }] : []),
  ]
  const notifications = alertNotifications.length > 0 ? alertNotifications : [{ id: 'healthy', title: '系统运行正常', detail: '节点和已启用线路均处于预期状态', view: 'dashboard' as View, level: 'healthy' }]
  const normalizedSearch = searchQuery.trim().toLocaleLowerCase()
  const searchResults = normalizedSearch ? [
    ...nodeItems.filter((node) => `${node.name} ${node.region} ${node.publicIp}`.toLocaleLowerCase().includes(normalizedSearch)).slice(0, 4).map((node) => ({ id: node.id, label: node.name, meta: `${node.region || '未设置地区'} · ${node.publicIp}`, kind: 'node' as const })),
    ...routes.filter((route) => `${route.name} ${route.path} ${route.protocol}`.toLocaleLowerCase().includes(normalizedSearch)).slice(0, 4).map((route) => ({ id: route.id, label: route.name, meta: route.path, kind: 'rule' as const })),
  ] : []

  const filteredConnections = connectionItems.filter((connection) =>
    (!connectionNodeFilter || connection.nodeId === connectionNodeFilter) &&
    (!connectionProtocolFilter || connection.protocol === connectionProtocolFilter))
  const filteredAuditItems = auditItems.filter((event) => auditCategoryFilter === 'all' || auditCategory(event.action) === auditCategoryFilter)

  const nav = [
    { label: '概览', icon: LayoutDashboard, view: 'dashboard' as View },
    { label: '节点', icon: Boxes, view: 'nodes' as View },
    { label: '转发线路', icon: Cable, view: 'rules' as View, badge: String(ruleItems.length) },
    { label: '实时连接', icon: Network, view: 'connections' as View, badge: connectionItems.length ? String(connectionItems.length) : undefined },
    { label: '监控分析', icon: Activity, view: 'monitoring' as View },
    { label: '运行日志', icon: TerminalSquare, view: 'logs' as View, adminOnly: true },
    { label: '操作审计', icon: ListChecks, view: 'audit' as View, adminOnly: true },
  ].filter((item) => !item.adminOnly || user?.role === 'admin')

  const latestTraffic = trafficHistory.points[trafficHistory.points.length - 1]
  const averageBytesPerSecond = latestTraffic && trafficHistory.intervalSeconds > 0
    ? (latestTraffic.uploadBytes + latestTraffic.downloadBytes) / trafficHistory.intervalSeconds : 0
  const nodeTrafficHistory: TrafficHistory = nodeDetail ? {
    from: nodeDetail.from, to: nodeDetail.to, intervalSeconds: nodeDetail.intervalSeconds,
    uploadBytes: nodeDetail.uploadBytes, downloadBytes: nodeDetail.downloadBytes, points: nodeDetail.points,
  } : emptyTraffic
  const nodeNetworkHistory: TrafficHistory = nodeDetail ? {
    from: nodeDetail.from, to: nodeDetail.to, intervalSeconds: nodeDetail.intervalSeconds, uploadBytes: 0, downloadBytes: 0,
    points: nodeDetail.points.map((point) => ({ time: point.time, uploadBytes: point.networkTxBps, downloadBytes: point.networkRxBps })),
  } : emptyTraffic

  if (checkingAuth) return <main className="auth-page"><div className="auth-loader"><CircleDot /><span>正在连接控制面…</span></div></main>
  if (!user) return <LoginScreen onAuthenticated={(value) => { setUser(value); loadControlData().catch(() => setApiOnline(false)) }} />

  return (
    <div className="app-shell">
      <aside className={`sidebar ${menuOpen ? 'sidebar-open' : ''}`}>
        <div className="brand"><div className="brand-mark"><CircleDot size={22} /></div><div><b>PortFlow</b><span>网络控制台</span></div></div>
        <button className="mobile-close" onClick={() => setMenuOpen(false)} aria-label="关闭菜单"><X /></button>
        <nav>
          <p className="nav-title">工作台</p>
          {nav.map(({ label, icon: Icon, view, badge }) => (
            <button key={label} onClick={() => switchView(view)} className={view === activeView ? 'nav-item active' : 'nav-item'}>
              <Icon size={18} /><span>{label}</span>{badge && <em>{badge}</em>}
            </button>
          ))}
          <p className="nav-title nav-title-secondary">系统</p>
          {user.role === 'admin' && <button onClick={() => switchView('members')} className={activeView === 'members' ? 'nav-item active' : 'nav-item'}><Users size={18} /><span>成员权限</span><em>{memberItems.length || ''}</em></button>}
          {user.role === 'admin' && <button onClick={() => switchView('settings')} className={activeView === 'settings' ? 'nav-item active' : 'nav-item'}><Settings size={18} /><span>系统设置</span></button>}
        </nav>
        <div className="sidebar-health">
          <div className="health-icon"><ShieldCheck size={19} /></div>
          <div><b>控制面正常</b><span>{apiOnline ? '已连接实时数据' : '界面预览数据'}</span></div>
          <i className={apiOnline ? 'dot online' : 'dot'} />
        </div>
      </aside>
      {menuOpen && <button className="backdrop" onClick={() => setMenuOpen(false)} aria-label="关闭菜单遮罩" />}

      <main>
        <header className="topbar">
          <button className="menu-button" onClick={() => setMenuOpen(true)} aria-label="打开菜单"><Menu /></button>
          <div className="search-shell" ref={searchShellRef}>
            <div className="search"><Search size={17} /><input ref={searchInputRef} value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} aria-label="全局搜索" placeholder="搜索节点或线路..." /><kbd>⌘ K</kbd>{searchQuery && <button onClick={() => setSearchQuery('')} aria-label="清空搜索"><X size={14} /></button>}</div>
            <AnimatedPopover open={Boolean(normalizedSearch)} className="search-popover" role="listbox">
              <div className="popover-heading"><b>搜索结果</b><span>{searchResults.length} 项</span></div>
              {searchResults.map((result) => <button key={`${result.kind}-${result.id}`} className="search-result" onClick={() => { if (result.kind === 'node') openNode(result.id); else switchView('rules'); setSearchQuery('') }}><span className={result.kind === 'node' ? 'search-result-icon node' : 'search-result-icon rule'}>{result.kind === 'node' ? <Boxes size={15} /> : <Cable size={15} />}</span><span><b>{result.label}</b><small>{result.meta}</small></span></button>)}
              {searchResults.length === 0 && <div className="popover-empty">没有匹配的节点或线路</div>}
            </AnimatedPopover>
          </div>
          <div className="top-actions">
            <button className="icon-button theme-button" onClick={() => setTheme(theme === 'light' ? 'dark' : 'light')} aria-label={theme === 'light' ? '切换到深色主题' : '切换到明亮主题'} title={theme === 'light' ? '切换到深色主题' : '切换到明亮主题'}>{theme === 'light' ? <Moon size={18} /> : <Sun size={18} />}</button>
            <div className="top-action-shell" ref={notificationShellRef}>
              <button className={notificationOpen ? 'icon-button active' : 'icon-button'} onClick={() => { setNotificationOpen(!notificationOpen); setAccountMenuOpen(false) }} aria-label="通知" aria-expanded={notificationOpen}><Bell size={19} />{alertNotifications.length > 0 && <span className="notification-dot" />}</button>
              <AnimatedPopover open={notificationOpen} className="notification-popover"><div className="popover-heading"><b>状态通知</b><span>{alertNotifications.length > 0 ? `${alertNotifications.length} 项待处理` : '当前正常'}</span></div>{notifications.map((notification) => <button key={notification.id} className="notification-item" onClick={() => { switchView(notification.view); setNotificationOpen(false) }}><span className={notification.level === 'healthy' ? 'notice-icon healthy' : 'notice-icon warning'}>{notification.level === 'healthy' ? <CheckCircle2 size={16} /> : <Bell size={16} />}</span><span><b>{notification.title}</b><small>{notification.detail}</small></span></button>)}</AnimatedPopover>
            </div>
            <div className="divider" />
            <div className="top-action-shell" ref={accountShellRef}>
              <button className={accountMenuOpen ? 'profile active' : 'profile'} onClick={() => { setAccountMenuOpen(!accountMenuOpen); setNotificationOpen(false) }} aria-label="打开账户菜单" aria-expanded={accountMenuOpen}><span className="avatar">{user.username.slice(0, 1).toUpperCase()}</span><span className="profile-copy"><b>{user.username}</b><small>{user.role === 'admin' ? '管理员' : '普通成员'}</small></span><ChevronDown size={16} /></button>
              <AnimatedPopover open={accountMenuOpen} className="account-popover" role="menu"><div className="account-summary"><span className="avatar large">{user.username.slice(0, 1).toUpperCase()}</span><span><b>{user.username}</b><small>{user.role === 'admin' ? '管理员账户' : '普通成员账户'}</small></span></div><button className="account-action" onClick={openSecurityCenter}><KeyRound size={16} /><span>账户安全</span><em>{user.mfaEnabled ? '已开启二次验证' : '建议开启'}</em></button><button className="account-action danger" onClick={() => void logout()}><LogOut size={16} /><span>退出登录</span></button></AnimatedPopover>
            </div>
          </div>
        </header>

        <div className="content">
          {activeView === 'dashboard' && <>
          <section className="page-heading">
            <div><p className="eyebrow">网络运行概览</p><h1>{summary.nodes.offline || summary.rules.degraded ? '有项目需要关注' : '网络运行平稳'}</h1><p>{summary.nodes.offline || summary.rules.degraded ? `${summary.nodes.offline} 个节点离线，${summary.rules.degraded} 条线路异常。` : '所有已启用线路和在线节点均在预期状态。'}</p></div>
            <button className="primary-button" onClick={openCreateRule}><Plus size={18} />创建转发线路</button>
          </section>

          <section className="stat-grid">
            <article className="stat-card"><div className="stat-icon mint"><Boxes /></div><div className="stat-meta"><span>在线节点</span><b>{summary.nodes.online}<small> / {summary.nodes.online + summary.nodes.offline}</small></b><em className={summary.nodes.offline === 0 ? 'positive' : ''}>{summary.nodes.offline === 0 ? '全部区域可达' : `${summary.nodes.offline} 个节点离线`}</em></div></article>
            <article className="stat-card"><div className="stat-icon blue"><Cable /></div><div className="stat-meta"><span>健康线路</span><b>{summary.rules.healthy}<small> / {summary.rules.healthy + summary.rules.degraded + summary.rules.stopped}</small></b><em>{summary.rules.degraded} 条需要留意</em></div></article>
            <article className="stat-card"><div className="stat-icon violet"><Network /></div><div className="stat-meta"><span>实时连接</span><b>{summary.connections.toLocaleString()}</b><em className="positive">来自 Agent 心跳</em></div></article>
            <article className="stat-card"><div className="stat-icon amber"><Gauge /></div><div className="stat-meta"><span>最近 24 小时流量</span><b>{formatBytes(summary.trafficToday.downloadBytes + summary.trafficToday.uploadBytes)}</b><em className="positive">心跳历史聚合</em></div></article>
          </section>

          <section className="dashboard-grid">
            <article className="panel traffic-panel">
              <div className="panel-heading"><div><h2>流量趋势</h2><p>所有线路聚合 · 最近 24 小时</p></div><span className="retention-badge">最近 24 小时</span></div>
              <div className="traffic-legend">
                <div><span className="legend-icon download"><ArrowDownToLine size={16} /></span><span><small>下载</small><b>{formatBytes(summary.trafficToday.downloadBytes)}</b></span></div>
                <div><span className="legend-icon upload"><ArrowUpFromLine size={16} /></span><span><small>上传</small><b>{formatBytes(summary.trafficToday.uploadBytes)}</b></span></div>
                <div className="live-speed"><i className="dot online" />最近区间均速 <b>{formatBytes(averageBytesPerSecond)}/s</b></div>
              </div>
              <Sparkline history={trafficHistory} />
            </article>

            <article className="panel nodes-panel">
              <div className="panel-heading"><div><h2>节点状态</h2><p>关键入口与出口节点</p></div><button className="text-button" onClick={() => switchView('nodes')}>查看全部</button></div>
              <div className="node-list">
                {nodeItems.map((node, index) => <button className="node-row node-row-button" key={node.id} onClick={() => openNode(node.id)}>
                  <div className={`node-orb ${['mint', 'blue', 'violet'][index % 3]}`}><Network size={18} /></div>
                  <div className="node-name"><b>{node.region ? `${node.region} · ` : ''}{node.name}</b><span>{node.publicIp}{node.tunnelAddress ? ` · WG ${node.tunnelAddress}` : ''} · {node.architecture} · Agent {node.agentVersion}</span></div>
                  <div className="load"><span><small>CPU</small><b>{node.cpuPercent.toFixed(0)}%</b></span><div><i style={{ width: `${Math.min(node.cpuPercent, 100)}%` }} /></div></div>
                  <span className="state"><i className={node.status === 'online' ? 'dot online' : 'dot'} />{node.status === 'online' ? '在线' : node.status === 'disabled' ? '已禁用' : '离线'}</span>
                </button>)}
                {nodeItems.length === 0 && <div className="empty-state"><Network size={22} /><b>还没有节点</b><span>创建一次性令牌并注册第一台服务器</span></div>}
              </div>
              {user.role === 'admin' && <button className="add-node" onClick={() => setShowEnrollment(true)}><Plus size={17} />添加新节点</button>}
            </article>
          </section>

          <section className="panel routes-panel">
            <div className="panel-heading"><div><h2>活跃线路</h2><p>Agent 最近一次上报的独立统计</p></div><button className="text-button" onClick={() => switchView('rules')}>管理所有线路</button></div>
            <div className="table-wrap"><table><thead><tr><th>线路</th><th>路径</th><th>协议</th><th>连接/会话</th><th>本次运行流量</th><th>状态</th><th>操作</th></tr></thead><tbody>
              {routes.map((route) => <tr key={route.id}><td><div className="route-name"><span><Cable size={16} /></span><b>{route.name}</b></div></td><td className="route-path">{route.path}</td><td><span className="protocol">{route.protocol === 'tcp_udp' ? 'TCP+UDP' : route.protocol.toUpperCase()}</span></td><td>{route.activeConnections.toLocaleString()} / {route.maxConnections ? route.maxConnections.toLocaleString() : '不限'}</td><td>{formatBytes(route.bytesIn + route.bytesOut)}</td><td><span title={route.failureReason} className={route.state === '已生效' ? 'route-state healthy' : route.state === '等待同步' ? 'route-state pending' : route.state === '应用失败' ? 'route-state failed' : 'route-state stopped'}><i />{route.state}</span></td><td><div className="row-actions"><button onClick={() => openCopyRule(route)}>复制</button><button onClick={() => openEditRule(route)}>编辑</button><button className="danger" onClick={() => deleteRule(route)}>删除</button></div></td></tr>)}
            </tbody></table></div>
          </section>
          </>}
          {activeView === 'rules' && <>
            <section className="page-heading">
              <div><p className="eyebrow">转发配置管理</p><h1>转发线路</h1><p>集中创建、复制、编辑和删除线路，并查看节点程序的实际应用状态。</p></div>
              <button className="primary-button" onClick={openCreateRule}><Plus size={18} />创建转发线路</button>
            </section>
            <section className="stat-grid monitoring-stats">
              <article className="stat-card"><div className="stat-icon blue"><Cable /></div><div className="stat-meta"><span>全部线路</span><b>{routes.length}</b><em>当前控制面配置</em></div></article>
              <article className="stat-card"><div className="stat-icon mint"><CheckCircle2 /></div><div className="stat-meta"><span>已生效</span><b>{routes.filter((route) => route.state === '已生效').length}</b><em className="positive">Agent 已确认应用</em></div></article>
              <article className="stat-card"><div className="stat-icon amber"><Activity /></div><div className="stat-meta"><span>等待或异常</span><b>{routes.filter((route) => route.enabled && route.state !== '已生效').length}</b><em>同步与节点状态</em></div></article>
              <article className="stat-card"><div className="stat-icon violet"><Network /></div><div className="stat-meta"><span>活跃连接</span><b>{routes.reduce((total, route) => total + route.activeConnections, 0).toLocaleString()}</b><em>最近一次 Agent 心跳</em></div></article>
            </section>
            <section className="panel routes-panel routes-management-panel">
              <div className="panel-heading"><div><h2>线路列表</h2><p>状态以节点最近一次配置确认和心跳为准</p></div><span className="retention-badge">{routes.length} 条线路</span></div>
              <div className="table-wrap"><table><thead><tr><th>线路</th><th>路径</th><th>协议</th><th>连接/会话</th><th>本次运行流量</th><th>状态</th><th>操作</th></tr></thead><tbody>
                {routes.map((route) => <tr key={route.id}><td><div className="route-name"><span><Cable size={16} /></span><b>{route.name}</b></div></td><td className="route-path">{route.path}</td><td><span className="protocol">{route.protocol === 'tcp_udp' ? 'TCP+UDP' : route.protocol.toUpperCase()}</span></td><td>{route.activeConnections.toLocaleString()} / {route.maxConnections ? route.maxConnections.toLocaleString() : '不限'}</td><td>{formatBytes(route.bytesIn + route.bytesOut)}</td><td><span title={route.failureReason} className={route.state === '已生效' ? 'route-state healthy' : route.state === '等待同步' ? 'route-state pending' : route.state === '应用失败' ? 'route-state failed' : 'route-state stopped'}><i />{route.state}</span></td><td><div className="row-actions"><button onClick={() => openCopyRule(route)}>复制</button><button onClick={() => openEditRule(route)}>编辑</button><button className="danger" onClick={() => deleteRule(route)}>删除</button></div></td></tr>)}
              </tbody></table></div>
              {routes.length === 0 && <div className="empty-state"><Cable size={23} /><b>还没有转发线路</b><span>创建第一条线路后，Agent 会自动拉取并应用配置</span><button className="empty-action" onClick={openCreateRule}>立即创建</button></div>}
            </section>
          </>}
          {activeView === 'nodes' && <>
            <section className="page-heading">
              <div><p className="eyebrow">服务器节点管理</p><h1>节点详情</h1><p>查看节点身份、配置同步状态、关联线路和最长 30 天资源历史。</p></div>
              {user.role === 'admin' && <button className="primary-button" onClick={() => setShowEnrollment(true)}><Plus size={18} />添加新节点</button>}
            </section>
            <section className="node-detail-layout">
              <aside className="panel node-directory">
                <div className="panel-heading"><div><h2>全部节点</h2><p>{nodeItems.length} 台已注册服务器</p></div></div>
                <div className="node-directory-list">
                  {nodeItems.map((node) => <button key={node.id} className={node.id === selectedNodeId ? 'node-directory-item active' : 'node-directory-item'} onClick={() => loadNodeDetail(node.id)}>
                    <span className={`node-orb ${node.status === 'online' ? 'mint' : ''}`}><Network size={17} /></span>
                    <span><b>{node.name}</b><small>{node.region || '未设置地区'} · {node.publicIp}</small></span>
                    <i className={node.status === 'online' ? 'dot online' : 'dot'} />
                  </button>)}
                  {nodeItems.length === 0 && <div className="empty-state"><Boxes size={22} /><b>还没有节点</b><span>注册第一台 Agent 后即可查看详情</span></div>}
                </div>
              </aside>
              <div className="node-detail-content">
                {nodeDetailLoading && !nodeDetail && <div className="panel node-detail-placeholder"><CircleDot /><span>正在读取节点历史…</span></div>}
                {nodeDetailError && <div className="auth-error node-detail-error">{nodeDetailError}</div>}
                {nodeDetail && <>
                  <section className="node-detail-head panel">
                    <div><span className={`node-orb ${nodeDetail.node.status === 'online' ? 'mint' : ''}`}><Network size={20} /></span><div><h2>{nodeDetail.node.name}</h2><p>{nodeDetail.node.region || '未设置地区'} · {nodeDetail.node.publicIp}{nodeDetail.node.tunnelAddress ? ` · WG ${nodeDetail.node.tunnelAddress}` : ''}</p></div></div>
                    <div className="node-range"><span className={nodeDetail.node.status === 'online' ? 'route-state healthy' : 'route-state stopped'}><i />{nodeDetail.node.status === 'online' ? '在线' : nodeDetail.node.status === 'disabled' ? '已禁用' : '离线'}</span><select value={nodeHours} onChange={(event) => { const hours = Number(event.target.value); setNodeHours(hours); void loadNodeDetail(nodeDetail.node.id, hours) }}><option value={6}>最近 6 小时</option><option value={24}>最近 24 小时</option><option value={168}>最近 7 天</option><option value={720}>最近 30 天</option></select></div>
                  </section>
                  {nodeDetail.node.lastConfigError && <div className="node-warning"><ShieldCheck size={16} /><span><b>最近配置应用失败</b>{nodeDetail.node.lastConfigError}</span></div>}
                  <section className="stat-grid node-detail-stats">
                    <article className="stat-card"><div className="stat-icon mint"><Activity /></div><div className="stat-meta"><span>CPU / 内存</span><b>{nodeDetail.node.cpuPercent.toFixed(1)}%<small> / {nodeDetail.node.memoryPercent.toFixed(1)}%</small></b><em>负载 {nodeDetail.node.loadOne.toFixed(2)}</em></div></article>
                    <article className="stat-card"><div className="stat-icon amber"><Gauge /></div><div className="stat-meta"><span>根磁盘使用率</span><b>{nodeDetail.node.diskPercent.toFixed(1)}%</b><em className={nodeDetail.node.diskPercent >= 85 ? '' : 'positive'}>{nodeDetail.node.diskPercent >= 85 ? '磁盘空间需要关注' : '磁盘空间正常'}</em></div></article>
                    <article className="stat-card"><div className="stat-icon violet"><Network /></div><div className="stat-meta"><span>主机网络收 / 发</span><b>{formatBytes(nodeDetail.node.networkRxBps)}<small> / {formatBytes(nodeDetail.node.networkTxBps)}</small></b><em>每秒接口活动</em></div></article>
                    <article className="stat-card"><div className="stat-icon blue"><Cable /></div><div className="stat-meta"><span>连接 / 会话</span><b>{nodeDetail.node.activeConnections.toLocaleString()}</b><em>最近一次心跳</em></div></article>
                  </section>
                  <section className="node-history-grid">
                    <article className="panel analytics-panel node-history-panel"><div className="panel-heading"><div><h2>资源趋势</h2><p>CPU、内存与根磁盘区间平均值</p></div><div className="resource-legend"><span><i className="cpu" />CPU</span><span><i className="memory" />内存</span><span><i className="disk" />磁盘</span></div></div><ResourceSparkline points={nodeDetail.points} /></article>
                    <article className="panel analytics-panel node-history-panel"><div className="panel-heading"><div><h2>流量趋势</h2><p>识别 Agent 重启后的计数归零</p></div></div><div className="traffic-legend compact"><div><span className="legend-icon download"><ArrowDownToLine size={15} /></span><span><small>下载</small><b>{formatBytes(nodeDetail.downloadBytes)}</b></span></div><div><span className="legend-icon upload"><ArrowUpFromLine size={15} /></span><span><small>上传</small><b>{formatBytes(nodeDetail.uploadBytes)}</b></span></div></div><Sparkline history={nodeTrafficHistory} /></article>
                    <article className="panel analytics-panel node-history-panel network-history-panel"><div className="panel-heading"><div><h2>主机网络活动</h2><p>所有非回环接口的平均收发速率</p></div></div><div className="traffic-legend compact"><div><span className="legend-icon download"><ArrowDownToLine size={15} /></span><span><small>当前接收</small><b>{formatBytes(nodeDetail.node.networkRxBps)}/s</b></span></div><div><span className="legend-icon upload"><ArrowUpFromLine size={15} /></span><span><small>当前发送</small><b>{formatBytes(nodeDetail.node.networkTxBps)}/s</b></span></div></div><Sparkline history={nodeNetworkHistory} label="节点主机网络收发速率趋势" /></article>
                  </section>
                  <section className="panel node-info-panel"><div className="panel-heading"><div><h2>运行与配置</h2><p>身份信息和最近一次配置同步结果</p></div></div><div className="node-info-grid"><div><span>节点 ID</span><code>{nodeDetail.node.id}</code></div><div><span>系统架构</span><b>{nodeDetail.node.architecture}</b></div><div><span>Agent 版本</span><b>{nodeDetail.node.agentVersion}</b></div><div><span>最后心跳</span><b>{new Date(nodeDetail.node.lastHeartbeat).toLocaleString()}</b></div><div><span>期望配置</span><b>v{nodeDetail.node.configVersion}</b></div><div><span>已应用配置</span><b>v{nodeDetail.node.appliedConfigVersion}</b></div></div></section>
                  <section className="panel monitoring-table node-rules-panel"><div className="panel-heading"><div><h2>关联线路</h2><p>该节点作为入口或出口参与的线路</p></div><span className="retention-badge">{nodeDetail.rules.length} 条</span></div><div className="table-wrap"><table><thead><tr><th>线路</th><th>角色</th><th>协议</th><th>目标</th><th>状态</th></tr></thead><tbody>{nodeDetail.rules.map((rule) => <tr key={rule.id}><td><b>{rule.name}</b></td><td>{rule.ingressNodeId === nodeDetail.node.id ? '入口' : '出口'}</td><td><span className="protocol">{rule.protocol === 'tcp_udp' ? 'TCP+UDP' : rule.protocol.toUpperCase()}</span></td><td>{rule.targetHost}:{rule.targetPort}</td><td><span className={rule.enabled ? 'route-state healthy' : 'route-state stopped'}><i />{rule.enabled ? '已启用' : '已停止'}</span></td></tr>)}</tbody></table></div>{nodeDetail.rules.length === 0 && <div className="table-empty">该节点暂未关联转发线路</div>}</section>
                </>}
              </div>
            </section>
          </>}
          {activeView === 'connections' && <>
            <section className="page-heading">
              <div><p className="eyebrow">当前连接快照</p><h1>实时连接</h1><p>查看节点程序最近一次上报的 TCP 连接与 UDP 会话信息，不采集转发内容。</p></div>
              <button className="select-button" disabled={connectionLoading} onClick={() => loadConnections()}>{connectionLoading ? '正在刷新…' : '刷新快照'}</button>
            </section>
            <section className="stat-grid monitoring-stats">
              <article className="stat-card"><div className="stat-icon mint"><Network /></div><div className="stat-meta"><span>快照条目</span><b>{connectionItems.length.toLocaleString()}</b><em>单次心跳最多 2000 条</em></div></article>
              <article className="stat-card"><div className="stat-icon blue"><Cable /></div><div className="stat-meta"><span>TCP 连接</span><b>{connectionItems.filter((item) => item.protocol === 'tcp').length.toLocaleString()}</b><em>当前已建立连接</em></div></article>
              <article className="stat-card"><div className="stat-icon violet"><Activity /></div><div className="stat-meta"><span>UDP 会话</span><b>{connectionItems.filter((item) => item.protocol === 'udp').length.toLocaleString()}</b><em>60 秒空闲自动清理</em></div></article>
              <article className="stat-card"><div className="stat-icon amber"><Gauge /></div><div className="stat-meta"><span>会话内流量</span><b>{formatBytes(connectionItems.reduce((sum, item) => sum + item.bytesIn + item.bytesOut, 0))}</b><em>当前快照合计</em></div></article>
            </section>
            <section className="panel monitoring-table connections-panel">
              <div className="panel-heading"><div><h2>连接快照</h2><p>每 15 秒自动刷新；节点离线或心跳超过 45 秒时标记为过期</p></div><span className="retention-badge">仅元数据</span></div>
              <div className="log-filters">
                <label><span>节点</span><select value={connectionNodeFilter} onChange={(event) => setConnectionNodeFilter(event.target.value)}><option value="">全部节点</option>{nodeItems.map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}</select></label>
                <label><span>协议</span><select value={connectionProtocolFilter} onChange={(event) => setConnectionProtocolFilter(event.target.value)}><option value="">全部协议</option><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
                <span className="connection-filter-result">显示 {filteredConnections.length.toLocaleString()} 条</span>
              </div>
              {connectionError && <div className="auth-error audit-error">{connectionError}</div>}
              <div className="table-wrap"><table className="connection-table"><thead><tr><th>状态</th><th>协议</th><th>节点 / 线路</th><th>来源</th><th>目标</th><th>持续时间</th><th>上行 / 下行</th><th>最后活动</th></tr></thead><tbody>
                {filteredConnections.map((connection) => {
                  const node = nodeItems.find((item) => item.id === connection.nodeId)
                  const rule = ruleItems.find((item) => item.id === connection.ruleId)
                  const stale = node?.status !== 'online' || Date.now() - new Date(connection.observedAt).getTime() > 45_000
                  return <tr key={`${connection.nodeId}-${connection.id}`}><td><span className={stale ? 'route-state stopped' : 'route-state healthy'}><i />{stale ? '快照过期' : '活跃'}</span></td><td><span className="protocol">{connection.protocol.toUpperCase()}</span></td><td><b>{node?.name ?? connection.nodeId}</b><br /><small>{rule?.name ?? connection.ruleId}</small></td><td><code>{connection.sourceAddress}</code></td><td><code>{connection.targetAddress}</code></td><td>{formatDuration(connection.startedAt)}<br /><small>{new Date(connection.startedAt).toLocaleString()}</small></td><td>{formatBytes(connection.bytesIn)} / {formatBytes(connection.bytesOut)}</td><td>{new Date(connection.lastActivity).toLocaleString()}<br /><small>采集于 {new Date(connection.observedAt).toLocaleTimeString()}</small></td></tr>
                })}
              </tbody></table></div>
              {filteredConnections.length === 0 && !connectionLoading && !connectionError && <div className="empty-state"><Network size={22} /><b>当前没有匹配的连接</b><span>建立转发连接后，下一次 Agent 心跳会在这里显示</span></div>}
            </section>
          </>}
          {activeView === 'monitoring' && <>
            <section className="page-heading">
              <div><p className="eyebrow">资源与流量趋势</p><h1>监控分析</h1><p>基于节点程序每分钟上报数据整理出的最近 24 小时趋势。</p></div>
            </section>
            <section className="stat-grid monitoring-stats">
              <article className="stat-card"><div className="stat-icon mint"><ArrowDownToLine /></div><div className="stat-meta"><span>24 小时下载</span><b>{formatBytes(trafficHistory.downloadBytes)}</b><em>所有节点聚合</em></div></article>
              <article className="stat-card"><div className="stat-icon blue"><ArrowUpFromLine /></div><div className="stat-meta"><span>24 小时上传</span><b>{formatBytes(trafficHistory.uploadBytes)}</b><em>所有节点聚合</em></div></article>
              <article className="stat-card"><div className="stat-icon violet"><Gauge /></div><div className="stat-meta"><span>最近区间均速</span><b>{formatBytes(averageBytesPerSecond)}<small> /s</small></b><em>{trafficHistory.intervalSeconds / 60} 分钟采样区间</em></div></article>
              <article className="stat-card"><div className="stat-icon amber"><Network /></div><div className="stat-meta"><span>当前连接/会话</span><b>{summary.connections.toLocaleString()}</b><em>{summary.nodes.online} 个节点在线</em></div></article>
            </section>
            <section className="panel analytics-panel">
              <div className="panel-heading"><div><h2>聚合流量趋势</h2><p>计数器差值聚合，Agent 重启后自动识别归零</p></div><span className="retention-badge">保留 30 天</span></div>
              <div className="traffic-legend">
                <div><span className="legend-icon download"><ArrowDownToLine size={16} /></span><span><small>下载</small><b>{formatBytes(trafficHistory.downloadBytes)}</b></span></div>
                <div><span className="legend-icon upload"><ArrowUpFromLine size={16} /></span><span><small>上传</small><b>{formatBytes(trafficHistory.uploadBytes)}</b></span></div>
              </div>
              <Sparkline history={trafficHistory} />
            </section>
            <section className="panel monitoring-table">
              <div className="panel-heading"><div><h2>节点实时资源</h2><p>最近一次心跳状态</p></div></div>
              <div className="table-wrap"><table><thead><tr><th>节点</th><th>状态</th><th>CPU</th><th>内存</th><th>磁盘</th><th>网络收 / 发</th><th>连接/会话</th><th>最后心跳</th></tr></thead><tbody>
                {nodeItems.map((node) => <tr key={node.id}><td><b>{node.name}</b><br /><small>{node.publicIp}{node.tunnelAddress ? ` · WG ${node.tunnelAddress}` : ''}</small></td><td><span className={node.status === 'online' ? 'route-state healthy' : 'route-state stopped'}><i />{node.status === 'online' ? '在线' : '离线'}</span></td><td>{node.cpuPercent.toFixed(1)}%</td><td>{node.memoryPercent.toFixed(1)}%</td><td>{node.diskPercent.toFixed(1)}%</td><td>{formatBytes(node.networkRxBps)} / {formatBytes(node.networkTxBps)} 每秒</td><td>{node.activeConnections.toLocaleString()}</td><td>{new Date(node.lastHeartbeat).toLocaleString()}</td></tr>)}
              </tbody></table></div>
            </section>
          </>}
          {activeView === 'audit' && <>
            <section className="page-heading">
              <div><p className="eyebrow">安全与操作记录</p><h1>操作审计</h1><p>这里记录“谁在什么时间做了什么”，方便排查误操作和异常登录。</p></div>
              <button className="select-button" disabled={auditLoading} onClick={() => loadAudit()}>{auditLoading ? '正在刷新…' : '刷新记录'}</button>
            </section>
            <section className="panel audit-panel">
              <div className="panel-heading"><div><h2>最近操作记录</h2><p>最新记录排在最前面，仅管理员可以查看</p></div><span className="retention-badge">仅管理员</span></div>
              <div className="category-tabs" role="tablist" aria-label="审计记录分类">{[['all','全部'],['security','登录与安全'],['members','成员权限'],['nodes','节点管理'],['rules','转发线路'],['other','其他']].map(([value,label]) => <button key={value} className={auditCategoryFilter === value ? 'active' : ''} onClick={() => setAuditCategoryFilter(value)}>{label}<span>{value === 'all' ? auditItems.length : auditItems.filter((event) => auditCategory(event.action) === value).length}</span></button>)}</div>
              {auditError && <div className="auth-error audit-error">{auditError}</div>}
              <div className="table-wrap"><table><thead><tr><th>发生时间</th><th>做了什么</th><th>由谁执行</th><th>影响对象</th><th>来源地址</th><th>补充说明</th></tr></thead><tbody>
                {filteredAuditItems.map((event) => <tr key={event.id}><td>{new Date(event.createdAt).toLocaleString()}</td><td><b>{auditActionLabel(event.action)}</b><br /><small>{event.action}</small></td><td><b>{auditTypeLabel(event.actorType)}</b>{event.actorId && <small className="block-id">编号：{event.actorId}</small>}</td><td>{auditTypeLabel(event.targetType)}{event.targetId && <small className="block-id">编号：{event.targetId}</small>}</td><td><code>{event.remoteIp || '系统内部'}</code></td><td className="audit-details">{auditDetailsLabel(event.details)}</td></tr>)}
              </tbody></table></div>
              {filteredAuditItems.length === 0 && !auditLoading && !auditError && <div className="empty-state"><ListChecks size={22} /><b>这个分类暂无记录</b><span>新的安全和配置操作会自动保存在这里</span></div>}
              {auditNextBefore && <button className="load-more" disabled={auditLoading} onClick={() => loadAudit(auditNextBefore, true)}>{auditLoading ? '正在读取…' : '加载更早记录'}</button>}
            </section>
          </>}
          {activeView === 'members' && user.role === 'admin' && <>
            <section className="page-heading">
              <div><p className="eyebrow">团队账号与权限</p><h1>成员权限</h1><p>管理内部成员角色和登录状态；权限变更会立即让目标账号重新登录。</p></div>
              <button className="primary-button" onClick={openCreateMember}><Plus size={18} />添加成员</button>
            </section>
            <section className="stat-grid monitoring-stats">
              <article className="stat-card"><div className="stat-icon mint"><Users /></div><div className="stat-meta"><span>全部账号</span><b>{memberItems.length}</b><em>内部团队成员</em></div></article>
              <article className="stat-card"><div className="stat-icon blue"><ShieldCheck /></div><div className="stat-meta"><span>启用管理员</span><b>{memberItems.filter((item) => item.role === 'admin' && !item.disabled).length}</b><em>至少保留一名</em></div></article>
              <article className="stat-card"><div className="stat-icon violet"><Users /></div><div className="stat-meta"><span>启用成员</span><b>{memberItems.filter((item) => item.role === 'member' && !item.disabled).length}</b><em>可操作直连线路</em></div></article>
              <article className="stat-card"><div className="stat-icon amber"><Activity /></div><div className="stat-meta"><span>已禁用</span><b>{memberItems.filter((item) => item.disabled).length}</b><em>无法建立会话</em></div></article>
            </section>
            <section className="panel monitoring-table members-panel">
              <div className="panel-heading"><div><h2>团队账号</h2><p>管理员拥有成员、节点注册、中转和审计权限；普通成员可查看状态并管理直连线路</p></div><button className="select-button" disabled={memberLoading} onClick={() => loadMembers()}>{memberLoading ? '正在刷新…' : '刷新列表'}</button></div>
              {memberError && !showMemberEditor && <div className="auth-error audit-error">{memberError}</div>}
              <div className="table-wrap"><table><thead><tr><th>成员</th><th>角色</th><th>登录保护</th><th>账号状态</th><th>权限说明</th><th>操作</th></tr></thead><tbody>
                {memberItems.map((member) => <tr key={member.id}><td><div className="member-name"><span className="avatar">{member.username.slice(0, 1).toUpperCase()}</span><span><b>{member.username}</b><small>创建于 {new Date(member.createdAt).toLocaleDateString()}</small></span></div></td><td><span className={member.role === 'admin' ? 'member-role admin' : 'member-role'}>{member.role === 'admin' ? '管理员' : '普通成员'}</span></td><td><span className={member.mfaEnabled ? 'security-badge enabled' : 'security-badge'}><ShieldCheck size={14} />{member.mfaEnabled ? '已开启二次验证' : '仅密码登录'}</span></td><td><span className={member.disabled ? 'route-state stopped' : 'route-state healthy'}><i />{member.disabled ? '已禁用' : '已启用'}</span></td><td>{member.role === 'admin' ? '可管理成员、节点、线路和安全记录' : '可查看状态并管理单节点直连线路'}</td><td>{member.id === user.id ? <span className="current-account">当前账号请在右上角管理</span> : <div className="member-actions"><button className="member-edit-button" onClick={() => openEditMember(member)}>编辑</button><button className="member-delete-button" onClick={() => void deleteMember(member)}><Trash2 size={14} />删除</button></div>}</td></tr>)}
              </tbody></table></div>
              {memberItems.length === 0 && !memberLoading && !memberError && <div className="empty-state"><Users size={22} /><b>暂无成员</b><span>添加第一个内部协作账号</span></div>}
            </section>
          </>}
          {activeView === 'settings' && user.role === 'admin' && <>
            <section className="page-heading">
              <div><p className="eyebrow">运行与安全设置</p><h1>系统设置</h1><p>集中核对控制面正在执行的运行、安全、节点心跳和数据保留策略。</p></div>
              <button className="select-button" disabled={settingsLoading} onClick={() => loadSystemSettings()}>{settingsLoading ? '正在刷新…' : '刷新配置'}</button>
            </section>
            {settingsError && <div className="auth-error settings-error">{settingsError}</div>}
            {!systemSettings && settingsLoading && <div className="panel node-detail-placeholder"><CircleDot /><span>正在读取运行配置…</span></div>}
            {systemSettings && <>
              <section className="stat-grid monitoring-stats">
                <article className="stat-card"><div className="stat-icon mint"><Settings /></div><div className="stat-meta"><span>控制面版本</span><b>{systemSettings.runtime.version}</b><em>启动于 {new Date(systemSettings.runtime.startedAt).toLocaleString()}</em></div></article>
                <article className="stat-card"><div className="stat-icon blue"><Activity /></div><div className="stat-meta"><span>连续运行</span><b>{formatUptime(systemSettings.runtime.uptimeSeconds)}</b><em>服务器时间 {new Date(systemSettings.runtime.serverTime).toLocaleTimeString()}</em></div></article>
                <article className="stat-card"><div className="stat-icon violet"><Network /></div><div className="stat-meta"><span>Agent 心跳</span><b>{systemSettings.agents.heartbeatIntervalSeconds}<small> 秒</small></b><em>{systemSettings.agents.offlineAfterSeconds} 秒未上报判定离线</em></div></article>
                <article className="stat-card"><div className="stat-icon amber"><ShieldCheck /></div><div className="stat-meta"><span>安全 Cookie</span><b>{systemSettings.security.secureCookies ? '已启用' : '未启用'}</b><em className={systemSettings.security.secureCookies ? 'positive' : ''}>HttpOnly · SameSite {systemSettings.security.sameSite}</em></div></article>
              </section>
              {!systemSettings.security.secureCookies && <div className="node-warning settings-warning"><ShieldCheck size={16} /><span><b>Secure Cookie 当前未启用</b>开发环境可以使用；公网部署应设置 PORTFLOW_SECURE_COOKIES=true 并通过 HTTPS 访问。</span></div>}
              <section className={systemSettings.deployment.ready ? 'panel readiness-panel ready' : 'panel readiness-panel'}>
                <div className="readiness-summary"><span className="readiness-icon"><ShieldCheck size={22} /></span><div><p>部署状态检查</p><h2>{systemSettings.deployment.ready ? '控制面已满足发布条件' : '发布前仍有检查项未通过'}</h2><span>{systemSettings.deployment.ready ? '持久存储、版本、安全 Cookie、HTTPS 和管理员检查均正常。' : '处理下面的失败项后再将控制面对公网正式发布。'}</span></div><b>{systemSettings.deployment.ready ? '检查通过' : '需要处理'}</b></div>
                <div className="readiness-checks">{systemSettings.deployment.checks.map((check) => <div key={check.id} className={check.status === 'pass' ? 'readiness-check passed' : 'readiness-check failed'}><i /><span><b>{check.label}</b><small>{check.detail}</small></span></div>)}</div>
              </section>
              <section className="settings-grid">
                <article className="panel settings-panel"><div className="panel-heading"><div><h2>登录与会话安全</h2><p>这些规则由控制面自动执行</p></div><span className="retention-badge">安全策略</span></div><div className="settings-list">
                  <div><span>会话有效期</span><b>{systemSettings.security.sessionTtlSeconds / 3600} 小时</b></div>
                  <div><span>密码长度</span><b>{systemSettings.security.passwordMinLength}–{systemSettings.security.passwordMaxLength} 字符</b></div>
                  <div><span>登录失败限制</span><b>{systemSettings.security.loginFailureWindowSeconds / 60} 分钟内 {systemSettings.security.loginFailureLimit} 次</b></div>
                  <div><span>Cookie 保护</span><b>{systemSettings.security.httpOnlyCookies ? 'HttpOnly' : '未启用'} · SameSite {systemSettings.security.sameSite}</b></div>
                  <div><span>二次身份验证</span><b>{systemSettings.security.totpMfa ? '支持 TOTP 验证器与恢复码' : '未启用'}</b></div>
                </div></article>
                <article className="panel settings-panel"><div className="panel-heading"><div><h2>节点与快照上限</h2><p>限制单次上报数量，避免占用过多资源</p></div><span className="retention-badge">资源限制</span></div><div className="settings-list">
                  <div><span>离线判定</span><b>{systemSettings.agents.offlineAfterSeconds} 秒</b></div>
                  <div><span>单次连接快照</span><b>{systemSettings.agents.maxConnectionsPerHeartbeat.toLocaleString()} 条</b></div>
                  <div><span>每节点保留快照</span><b>{systemSettings.agents.maxStoredConnectionsPerNode.toLocaleString()} 条</b></div>
                  <div><span>单次日志批次</span><b>{systemSettings.agents.maxLogsPerHeartbeat} 条</b></div>
                </div></article>
                <article className="panel settings-panel settings-retention-panel"><div className="panel-heading"><div><h2>数据保留策略</h2><p>过期的节点指标和日志会由系统自动清理</p></div><span className="retention-badge">保留时间</span></div><div className="retention-cards">
                  <div><span>节点指标</span><b>{systemSettings.retention.nodeMetricsDays} 天</b><small>CPU、内存、磁盘、网络和流量历史</small></div>
                  <div><span>Agent 日志</span><b>{systemSettings.retention.agentLogsDays} 天</b><small>按接收时间自动清理</small></div>
                  <div><span>实时连接</span><b>最新快照</b><small>完整快照替换，截断快照有界保留</small></div>
                  <div><span>操作审计</span><b>{systemSettings.retention.auditEventsAutoCleanup ? '自动清理' : '长期保留'}</b><small>当前不会自动删除安全审计</small></div>
                </div></article>
              </section>
              <div className="capability-note settings-note"><ShieldCheck size={16} /><span>本页面展示控制面正在执行的真实配置。涉及 HTTPS、数据库和 Agent 部署的更改应通过部署配置完成，并在维护窗口内验证和回滚。</span></div>
            </>}
          </>}
          {activeView === 'logs' && <>
            <section className="page-heading">
              <div><p className="eyebrow">节点运行与故障排查</p><h1>运行日志</h1><p>“错误”通常需要处理，“警告”建议留意，“信息”只是正常运行记录；查看日志不会影响转发。</p></div>
              <button className="select-button" disabled={logLoading} onClick={() => loadLogs()}>{logLoading ? '正在刷新…' : '刷新日志'}</button>
            </section>
            <section className="panel log-panel">
              <div className="panel-heading"><div><h2>节点程序日志</h2><p>最新消息排在最前面，选择节点和级别可以更快定位问题</p></div><span className="retention-badge">自动保留 14 天</span></div>
              <div className="log-filters">
                <label><span>节点</span><select value={logNodeFilter} onChange={(event) => setLogNodeFilter(event.target.value)}><option value="">全部节点</option>{nodeItems.map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}</select></label>
                <label><span>级别</span><select value={logLevelFilter} onChange={(event) => setLogLevelFilter(event.target.value)}><option value="">全部级别</option><option value="error">错误</option><option value="warning">警告</option><option value="info">信息</option></select></label>
                <button className="select-button" disabled={logLoading} onClick={() => loadLogs()}>应用筛选</button>
              </div>
              {logError && <div className="auth-error audit-error">{logError}</div>}
              <div className="log-guide"><span><i className="error" />错误：可能影响服务</span><span><i className="warning" />警告：建议检查</span><span><i className="info" />信息：正常记录</span></div>
              <div className="table-wrap"><table className="log-table"><thead><tr><th>发生时间</th><th>重要程度</th><th>来自哪个节点</th><th>功能分类</th><th>具体内容</th></tr></thead><tbody>
                {logItems.map((entry) => <tr key={`${entry.nodeId}-${entry.id}`}><td>{new Date(entry.occurredAt).toLocaleString()}</td><td><span className={`log-level ${entry.level}`}>{entry.level === 'error' ? '错误' : entry.level === 'warning' ? '警告' : '信息'}</span></td><td><b>{nodeItems.find((node) => node.id === entry.nodeId)?.name ?? entry.nodeId}</b><br /><small>{entry.nodeId}</small></td><td><b>{logComponentLabel(entry.component)}</b><small className="block-id">程序标识：{entry.component}</small></td><td className="log-message">{entry.message}</td></tr>)}
              </tbody></table></div>
              {logItems.length === 0 && !logLoading && !logError && <div className="empty-state"><TerminalSquare size={22} /><b>暂无运行日志</b><span>Agent 上报的新事件将显示在这里</span></div>}
              {logNextBefore && <button className="load-more" disabled={logLoading} onClick={() => loadLogs(logNextBefore, true)}>{logLoading ? '正在读取…' : '加载更早日志'}</button>}
            </section>
          </>}
          <footer><span>PortFlow 控制面 · 1.1.2</span><span>数据面与控制面独立运行</span></footer>
        </div>
      </main>
      {showEnrollment && <div className="modal-layer" role="dialog" aria-modal="true" aria-label="添加新节点"><button className="modal-backdrop" onClick={closeEnrollment} aria-label="关闭" /><section className="modal-card">
        <div className="modal-heading"><div><p>安全注册节点</p><h2>添加新节点</h2><span>注册令牌仅显示一次，并在 30 分钟后失效。</span></div><button onClick={closeEnrollment}><X size={18} /></button></div>
        {!enrollmentToken ? <form onSubmit={createEnrollmentToken}><label><span>节点名称</span><input value={enrollmentNodeName} onChange={(event) => setEnrollmentNodeName(event.target.value)} maxLength={80} required /></label><label><span>节点地区（可选）</span><input value={enrollmentRegion} onChange={(event) => setEnrollmentRegion(event.target.value)} maxLength={80} placeholder="例如：上海" /></label><label><span>令牌备注</span><input value={enrollmentName} onChange={(event) => setEnrollmentName(event.target.value)} maxLength={80} required /></label>{enrollmentError && <div className="auth-error">{enrollmentError}</div>}<button className="auth-submit" disabled={creatingToken}>{creatingToken ? '正在创建…' : '生成一次性注册令牌'}</button></form> : <div className="enrollment-result">
          <div className="success-mark"><ShieldCheck size={21} /></div><b>令牌已生成</b><span>请在目标 Linux 节点运行以下命令，完成后关闭此窗口。</span>
          <pre>{enrollmentInstallCommand()}</pre>
          <div className="capability-note"><ShieldCheck size={16} /><span>命令会按节点架构从 GitHub 正式版本下载、校验并安装 Agent，再使用一次性令牌注册和启动服务；不会修改防火墙或云安全组。</span></div>
          <button className="copy-button" onClick={() => navigator.clipboard.writeText(enrollmentInstallCommand())}>复制安装并注册命令</button>
        </div>}
      </section></div>}
      {showSecurity && <div className="modal-layer" role="dialog" aria-modal="true" aria-label="账户安全"><button className="modal-backdrop" onClick={closeSecurityCenter} aria-label="关闭" /><section className="modal-card security-modal">
        <div className="modal-heading"><div><p>账户登录保护</p><h2>账户安全</h2><span>二次验证会在密码之外，再核对手机验证器中的动态验证码。</span></div><button onClick={closeSecurityCenter}><X size={18} /></button></div>
        {recoveryCodes.length > 0 ? <div className="security-flow">
          <div className="success-mark"><ShieldCheck size={22} /></div><h3>二次验证已开启</h3><p>请立即保存下面 8 个一次性恢复码。手机丢失时，每个恢复码只能登录一次，关闭窗口后不会再次显示。</p>
          <div className="recovery-grid">{recoveryCodes.map((code) => <code key={code}>{code}</code>)}</div>
          <button className="copy-button" onClick={() => navigator.clipboard.writeText(recoveryCodes.join('\n'))}><Copy size={16} />复制全部恢复码</button>
          <div className="security-warning"><ShieldCheck size={17} /><span>请保存在密码管理器等安全位置，不要与账号密码放在同一处。</span></div>
          <button className="auth-submit" onClick={closeSecurityCenter}>我已经安全保存</button>
        </div> : user.mfaEnabled ? <form className="security-flow" onSubmit={disableMFA}>
          <div className="security-status enabled"><ShieldCheck size={20} /><span><b>二次验证已开启</b><small>每次新登录都需要密码和 6 位动态验证码</small></span></div>
          <h3>关闭二次验证</h3><p>关闭后账号只使用密码登录，安全性会降低。为防止他人代操作，需要再次输入密码和验证码。</p>
          <label><span>当前密码</span><input type="password" autoComplete="current-password" value={securityPassword} onChange={(event) => setSecurityPassword(event.target.value)} required /></label>
          <label><span>{securityUseRecovery ? '一次性恢复码' : '验证器中的 6 位验证码'}</span><input className="otp-input" inputMode={securityUseRecovery ? 'text' : 'numeric'} autoComplete="one-time-code" value={securityCode} onChange={(event) => setSecurityCode(event.target.value)} placeholder={securityUseRecovery ? 'XXXX-XXXX-XXXX' : '000000'} minLength={6} required /></label>
          <button type="button" className="inline-switch" onClick={() => { setSecurityUseRecovery(!securityUseRecovery); setSecurityCode(''); setSecurityError('') }}>{securityUseRecovery ? '改用手机验证器' : '手机丢失？改用恢复码'}</button>
          {securityError && <div className="auth-error">{securityError}</div>}
          <div className="modal-actions"><button type="button" onClick={closeSecurityCenter}>取消</button><button className="danger-submit" disabled={securityBusy}>{securityBusy ? '正在验证…' : '确认关闭二次验证'}</button></div>
        </form> : securitySecret ? <form className="security-flow" onSubmit={enableMFA}>
          <div className="setup-steps"><span className="done">1</span><i /><span className="active">2</span><i /><span>3</span></div>
          <h3>在手机验证器中添加 PortFlow</h3><ol><li>打开任意支持 TOTP 的验证器应用。</li><li>选择“输入设置密钥”或“手动添加”。</li><li>账号填写 <b>PortFlow:{user.username}</b>，密钥填写下面内容。</li></ol>
          <div className="secret-box"><code>{securitySecret}</code><button type="button" onClick={() => navigator.clipboard.writeText(securitySecret)}><Copy size={15} />复制密钥</button></div>
          <details><summary>验证器支持通过链接添加？</summary><div className="secret-box uri"><code>{securityURI}</code><button type="button" onClick={() => navigator.clipboard.writeText(securityURI)}><Copy size={15} />复制链接</button></div></details>
          <label><span>输入验证器显示的 6 位数字</span><input className="otp-input" inputMode="numeric" autoComplete="one-time-code" value={securityCode} onChange={(event) => setSecurityCode(event.target.value)} placeholder="000000" minLength={6} required autoFocus /></label>
          {securityError && <div className="auth-error">{securityError}</div>}
          <div className="modal-actions"><button type="button" onClick={() => { setSecuritySecret(''); setSecurityURI(''); setSecurityCode('') }}>上一步</button><button className="auth-submit" disabled={securityBusy}>{securityBusy ? '正在验证…' : '验证并开启'}</button></div>
        </form> : <form className="security-flow" onSubmit={startMFASetup}>
          <div className="setup-steps"><span className="active">1</span><i /><span>2</span><i /><span>3</span></div>
          <div className="security-status"><KeyRound size={20} /><span><b>当前仅使用密码登录</b><small>建议管理员和有线路修改权限的账号开启二次验证</small></span></div>
          <h3>开始设置</h3><p>准备好手机验证器。为确认是你本人，请先输入当前账号密码。</p>
          <label><span>当前密码</span><input type="password" autoComplete="current-password" value={securityPassword} onChange={(event) => setSecurityPassword(event.target.value)} required autoFocus /></label>
          {securityError && <div className="auth-error">{securityError}</div>}
          <div className="modal-actions"><button type="button" onClick={closeSecurityCenter}>取消</button><button className="auth-submit" disabled={securityBusy}>{securityBusy ? '正在确认…' : '下一步'}</button></div>
        </form>}
      </section></div>}
      {showMemberEditor && <div className="modal-layer" role="dialog" aria-modal="true" aria-label={editingMemberId ? '编辑成员权限' : '添加成员'}><button className="modal-backdrop" onClick={closeMemberEditor} aria-label="关闭" /><section className="modal-card member-modal">
        <div className="modal-heading"><div><p>团队访问权限</p><h2>{editingMemberId ? '编辑成员权限' : '添加成员'}</h2><span>{editingMemberId ? '保存后该账号的全部旧会话会立即失效。' : '为内部协作人员创建独立登录账号。'}</span></div><button onClick={closeMemberEditor}><X size={18} /></button></div>
        <form onSubmit={saveMember}>
          <label><span>用户名</span><input value={memberUsername} onChange={(event) => setMemberUsername(event.target.value)} minLength={3} maxLength={32} disabled={Boolean(editingMemberId)} placeholder="teammate" required /></label>
          <label><span>{editingMemberId ? '重置密码（可留空）' : '初始密码'}</span><input type="password" autoComplete="new-password" value={memberPassword} onChange={(event) => setMemberPassword(event.target.value)} minLength={editingMemberId ? undefined : 12} maxLength={128} required={!editingMemberId} placeholder={editingMemberId ? '留空表示不修改' : '至少 12 个字符'} /></label>
          <label><span>账号角色</span><select value={memberRole} onChange={(event) => setMemberRole(event.target.value as 'admin' | 'member')}><option value="member">普通成员</option><option value="admin">管理员</option></select></label>
          {editingMemberId && <label className="toggle-label member-status-toggle"><span><b>禁用账号</b><small>禁用后不能登录，现有会话立即失效</small></span><button type="button" className={memberDisabled ? 'toggle active danger-toggle' : 'toggle'} onClick={() => setMemberDisabled(!memberDisabled)}><i /></button></label>}
          {editingMemberId && memberItems.find((item) => item.id === editingMemberId)?.mfaEnabled && <label className="toggle-label member-status-toggle"><span><b>重置二次验证</b><small>仅在成员丢失手机和恢复码时使用，成员下次登录前需重新设置</small></span><button type="button" className={memberResetMFA ? 'toggle active danger-toggle' : 'toggle'} onClick={() => setMemberResetMFA(!memberResetMFA)}><i /></button></label>}
          <div className="capability-note"><ShieldCheck size={16} /><span>{memberRole === 'admin' ? '管理员可以管理成员、注册节点、配置双节点中转，并查看日志和审计。' : '普通成员可以查看运行状态并创建、修改或删除单节点直连线路。'}</span></div>
          {memberError && <div className="auth-error">{memberError}</div>}
          <div className="modal-actions"><button type="button" onClick={closeMemberEditor}>取消</button><button className="auth-submit" disabled={savingMember}>{savingMember ? '正在保存…' : editingMemberId ? '保存权限' : '创建成员'}</button></div>
        </form>
      </section></div>}
      {showRuleEditor && <div className="modal-layer" role="dialog" aria-modal="true" aria-label={editingRuleId ? '编辑转发线路' : copyingRule ? '复制转发线路' : '创建转发线路'}><button className="modal-backdrop" onClick={closeRuleEditor} aria-label="关闭" /><section className="modal-card rule-modal">
        <div className="modal-heading"><div><p>{copyingRule ? '复制现有配置' : ruleForm.mode === 'relay' ? '双节点加密中转' : '单节点直接转发'}</p><h2>{editingRuleId ? '编辑转发线路' : copyingRule ? '复制转发线路' : '创建转发线路'}</h2><span>{copyingRule ? '目标地址和限制已复制，只需填写一个未占用的新监听端口。' : ruleForm.mode === 'relay' ? '先保存入口与出口关系，隧道就绪前保持停用。' : '支持 TCP、UDP 和 TCP+UDP 单节点直连。'}</span></div><button onClick={closeRuleEditor}><X size={18} /></button></div>
        <form className="rule-form" onSubmit={saveRule}>
          {copyingRule && <div className="copy-notice wide"><Copy size={18} /><span><b>这会新建一条线路，不会修改原线路</b><small>同一节点的同一协议不能重复使用监听端口，因此端口没有被自动复制。</small></span></div>}
          <label className="wide"><span>线路名称</span><input value={ruleForm.name} onChange={(event) => setRuleForm({ ...ruleForm, name: event.target.value })} maxLength={80} placeholder="例如：生产 SSH" required /></label>
          <label><span>转发协议</span><select value={ruleForm.protocol} onChange={(event) => setRuleForm({ ...ruleForm, protocol: event.target.value as RuleForm['protocol'] })}><option value="tcp">TCP</option><option value="udp">UDP</option><option value="tcp_udp">TCP + UDP</option></select></label>
          <label><span>转发模式</span><select value={ruleForm.mode} onChange={(event) => { const mode = event.target.value as RuleForm['mode']; setRuleForm({ ...ruleForm, mode, enabled: mode === 'relay' ? false : ruleForm.enabled, egressNodeId: mode === 'relay' ? ruleForm.egressNodeId : '' }) }}><option value="direct">单节点直连</option><option value="relay" disabled={user.role !== 'admin'}>双节点加密中转（预配置）</option></select></label>
          <label className={ruleForm.mode === 'direct' ? 'wide' : ''}><span>入口节点</span><select value={ruleForm.ingressNodeId} onChange={(event) => setRuleForm({ ...ruleForm, ingressNodeId: event.target.value, egressNodeId: event.target.value === ruleForm.egressNodeId ? '' : ruleForm.egressNodeId })} required><option value="">选择一个已注册节点</option>{nodeItems.filter((node) => node.status !== 'disabled').map((node) => <option key={node.id} value={node.id}>{node.region ? `${node.region} · ` : ''}{node.name} ({node.publicIp})</option>)}</select></label>
          {ruleForm.mode === 'relay' && <label><span>出口节点</span><select value={ruleForm.egressNodeId} onChange={(event) => setRuleForm({ ...ruleForm, egressNodeId: event.target.value })} required><option value="">选择不同的出口节点</option>{nodeItems.filter((node) => node.status !== 'disabled' && node.id !== ruleForm.ingressNodeId).map((node) => <option key={node.id} value={node.id}>{node.region ? `${node.region} · ` : ''}{node.name} ({node.publicIp})</option>)}</select></label>}
          <div className="form-section wide"><b>入口监听</b><span>Agent 将在所选节点监听该地址和端口</span></div>
          <label><span>监听地址</span><input value={ruleForm.listenHost} onChange={(event) => setRuleForm({ ...ruleForm, listenHost: event.target.value })} placeholder="0.0.0.0" required /></label>
          <label><span>监听端口</span><input type="number" min="1" max="65535" value={ruleForm.listenPort} onChange={(event) => setRuleForm({ ...ruleForm, listenPort: event.target.value })} placeholder="22022" required /></label>
          <div className="form-section wide"><b>目标服务</b><span>{ruleForm.mode === 'relay' ? '出口节点负责访问目标地址' : '入口节点必须能够直接访问目标地址'}</span></div>
          <label><span>目标主机</span><input value={ruleForm.targetHost} onChange={(event) => setRuleForm({ ...ruleForm, targetHost: event.target.value })} placeholder="10.0.0.8" required /></label>
          <label><span>目标端口</span><input type="number" min="1" max="65535" value={ruleForm.targetPort} onChange={(event) => setRuleForm({ ...ruleForm, targetPort: event.target.value })} placeholder="22" required /></label>
          <label><span>最大连接数</span><input type="number" min="0" max="1000000" value={ruleForm.maxConnections} onChange={(event) => setRuleForm({ ...ruleForm, maxConnections: event.target.value })} /><small>0 表示不限制</small></label>
          <label><span>带宽上限（Kbit/s）</span><input type="number" min="0" max="10000000" value={ruleForm.bandwidthKbps} onChange={(event) => setRuleForm({ ...ruleForm, bandwidthKbps: event.target.value })} /><small>整条线路双向合计；0 表示不限制</small></label>
          {ruleForm.mode === 'relay' && <label><span>隧道内部端口</span><input type="number" min="1" max="65535" value={ruleForm.relayPort} onChange={(event) => setRuleForm({ ...ruleForm, relayPort: event.target.value })} placeholder={ruleForm.listenPort || '24443'} /><small>留空时与入口端口相同</small></label>}
          <label className="toggle-label"><span>启用线路</span><button type="button" title={ruleForm.mode === 'relay' ? '只有两端 Agent 已上报隧道地址时才能启用' : undefined} className={ruleForm.enabled ? 'toggle active' : 'toggle'} onClick={() => setRuleForm({ ...ruleForm, enabled: !ruleForm.enabled })}><i /></button></label>
          <label><span>允许来源 CIDR</span><textarea value={ruleForm.allowCidrs} onChange={(event) => setRuleForm({ ...ruleForm, allowCidrs: event.target.value })} placeholder={'留空表示全部允许\n192.0.2.0/24'} /></label>
          <label><span>拒绝来源 CIDR</span><textarea value={ruleForm.denyCidrs} onChange={(event) => setRuleForm({ ...ruleForm, denyCidrs: event.target.value })} placeholder={'每行一个网段\n198.51.100.8/32'} /></label>
          <div className="capability-note wide"><ShieldCheck size={16} /><span>{ruleForm.mode === 'relay' ? '入口与出口 Agent 会通过已配置的 WireGuard 私网地址连接；带宽上限只在入口执行一次。' : 'UDP 会话空闲 60 秒后自动清理；TCP 与 UDP 共享同一条线路带宽上限。'}</span></div>
          {ruleError && <div className="auth-error wide">{ruleError}</div>}
          <div className="modal-actions wide"><button type="button" onClick={closeRuleEditor}>取消</button><button className="auth-submit" disabled={savingRule || nodeItems.length === 0}>{savingRule ? '正在保存…' : editingRuleId ? '保存并同步' : copyingRule ? '创建线路副本' : '创建并同步'}</button></div>
        </form>
      </section></div>}
    </div>
  )
}

export default App
