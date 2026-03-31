import { useState, useEffect, useRef, useCallback } from 'react'
import Settings from './Settings'
import Login from './components/Login'
import ChangePassword from './components/ChangePassword'
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar"
import { AppSidebar } from "@/components/AppSidebar"
import { SiteHeader } from "@/components/SiteHeader"
import { DashboardPage } from "@/components/DashboardPage"
import { LogsPage } from "@/components/LogsPage"
import { NZBHistoryPage } from "@/components/NZBHistoryPage"
import { ProfilePage } from "@/components/ProfilePage"
import StreamManagement from './components/DeviceManagement'
import { AlertCircle, Loader2 } from "lucide-react"

import { getApiUrl, apiFetch } from './api'

const MAX_HISTORY = 60
const MAX_LOGS = 200

function App() {
  const [authChecked, setAuthChecked] = useState(false)
  const [authenticated, setAuthenticated] = useState(false)
  const [currentUser, setCurrentUser] = useState(null)
  const [authToken, setAuthToken] = useState(localStorage.getItem('auth_token') || '')
  const [mustChangePassword, setMustChangePassword] = useState(false)
  const [stats, setStats] = useState(null)
  const [config, setConfig] = useState(null)
  const [saveStatus, setSaveStatus] = useState({ type: '', msg: '', errors: null })
  const [isSaving, setIsSaving] = useState(false)
  const [isRestarting, setIsRestarting] = useState(false)
  const isRestartingRef = useRef(false)
  const [error, setError] = useState(null)
  const [history, setHistory] = useState([])
  const [connHistory, setConnHistory] = useState([])
  const [wsStatus, setWsStatus] = useState('connecting')
  const [ws, setWs] = useState(null)
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'system')
  const [version, setVersion] = useState(null)
  const hasLoggedOutRef = useRef(false)
  const authCheckTimeoutRef = useRef(null)
  const [logs, setLogs] = useState([])
  const [activePage, setActivePage] = useState('dashboard')
  const [indexerCaps, setIndexerCaps] = useState({})
  const [nzbAttemptsRefreshTrigger, setNzbAttemptsRefreshTrigger] = useState(0)

  const clearSaveStatus = useCallback(() => {
    setSaveStatus({ type: '', msg: '', errors: null })
  }, [])

  const chartData = history.map((h, i) => ({
    time: h.time,
    speed: h.speed,
    conns: connHistory[i]?.conns ?? 0,
  }))

  useEffect(() => {
    fetch('/api/info')
      .then(res => res.ok ? res.json() : null)
      .then(data => data?.version && setVersion(data.version))
      .catch(() => {})
  }, [])

  useEffect(() => {
    const token = localStorage.getItem('auth_token')
    const pathParts = window.location.pathname.split('/').filter(p => p !== '')
    const isLegacyPath = pathParts.length > 0 && pathParts[0] !== 'api'

    if (!token && isLegacyPath) {
      // Stremio token-in-URL path — no cookie/localStorage auth needed
      hasLoggedOutRef.current = false
      setAuthenticated(true)
      setCurrentUser('legacy')
      setAuthChecked(true)
      return
    }

    if (!token) {
      setAuthenticated(false)
      setAuthChecked(true)
      return
    }

    // Verify the stored token against the server before showing the UI.
    // This catches the case where the container was restarted and a new
    // AdminToken was generated, making the stored cookie/token stale.
    fetch('/api/auth/check', { credentials: 'include' })
      .then(res => res.json().then(data => ({ ok: res.ok, data })))
      .then(({ ok, data }) => {
        if (ok && data.authenticated) {
          hasLoggedOutRef.current = false
          setAuthToken(token)
          setAuthenticated(true)
          setCurrentUser(data.username)
          setMustChangePassword(data.must_change_password || false)
          if (data.version) setVersion(data.version)
        } else {
          // Server rejected the token — clear stale state and show login
          setAuthenticated(false)
          setAuthToken('')
          localStorage.removeItem('auth_token')
        }
      })
      .catch(() => {
        // Server unreachable on startup — fall back to login screen
        setAuthenticated(false)
      })
      .finally(() => {
        setAuthChecked(true)
      })
  }, [])

  const handleLogin = (username, token, mustChange) => {
    hasLoggedOutRef.current = false
    setAuthenticated(true)
    setCurrentUser(username)
    setAuthToken(token)
    setMustChangePassword(mustChange)
    localStorage.setItem('auth_token', token)
  }

  const handleLogout = () => {
    hasLoggedOutRef.current = true
    setAuthenticated(false)
    setCurrentUser(null)
    setAuthToken('')
    localStorage.removeItem('auth_token')
    if (ws) {
      ws.close()
    }
    setWs(null)
    window.ws = null
  }

  useEffect(() => {
    const root = window.document.documentElement;
    root.classList.remove("light", "dark");

    if (theme === "system") {
      const systemTheme = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
      root.classList.add(systemTheme);
    } else {
      root.classList.add(theme);
    }
    localStorage.setItem('theme', theme);
  }, [theme]);

  useEffect(() => {
    if (!authenticated) return
    if (hasLoggedOutRef.current) return

    let socket;
    let reconnectTimeout;

    const connect = () => {
      if (hasLoggedOutRef.current) return
      
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = window.location.host;
      const pathParts = window.location.pathname.split('/').filter(p => p !== '');
      const tokenPrefix = pathParts.length > 0 && pathParts[0] !== 'api' ? `/${pathParts[0]}` : '';
      const wsToken = authToken || (pathParts.length > 0 && pathParts[0] !== 'api' ? pathParts[0] : '');
      const wsUrl = `${protocol}//${host}${tokenPrefix}/api/ws${wsToken ? `?token=${wsToken}` : ''}`;
      socket = new WebSocket(wsUrl);

      socket.onopen = () => {
        if (isRestartingRef.current) {
            window.location.reload();
            return;
        }
        if (hasLoggedOutRef.current) {
          socket.close();
          return;
        }
        setWsStatus('connected');
        setError(null);
        setWs(socket);
        window.ws = socket;
        setLogs([]);
      };

      socket.onmessage = (event) => {
        if (hasLoggedOutRef.current) return
        const msg = JSON.parse(event.data)
        switch (msg.type) {
          case 'stats': {
            const data = msg.payload
            setStats(data)
            const timestamp = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
            setHistory(prev => [...prev, { time: timestamp, speed: data.total_speed_mbps }].slice(-MAX_HISTORY))
            setConnHistory(prev => [...prev, { time: timestamp, conns: data.active_connections }].slice(-MAX_HISTORY))
            break
          }
          case 'log_entry':
            setLogs(prev => [...prev, msg.payload].slice(-MAX_LOGS))
            break
          case 'log_history':
            setLogs(msg.payload.slice(-MAX_LOGS))
            break
          case 'nzb_attempts_updated':
            setNzbAttemptsRefreshTrigger((v) => v + 1)
            break
          case 'auth_info': {
            if (msg.payload?.version) setVersion(msg.payload.version)
            if (hasLoggedOutRef.current) {
              socket.close()
              return
            }
            if (msg.payload.authenticated) {
              if (authCheckTimeoutRef.current) {
                clearTimeout(authCheckTimeoutRef.current)
                authCheckTimeoutRef.current = null
              }
              setAuthenticated(true)
              setCurrentUser(msg.payload.username)
              setMustChangePassword(msg.payload.must_change_password || false)
              // Fetch config and indexer caps via API (no longer sent over WS)
              fetch(getApiUrl('/api/config'), { credentials: 'include' })
                .then(res => res.ok ? res.json() : null)
                .then(data => { if (data) setConfig(data) })
                .catch(() => {})
              fetch(getApiUrl('/api/indexer/caps'), { credentials: 'include' })
                .then(res => res.ok ? res.json() : null)
                .then(data => { if (data) setIndexerCaps(data) })
                .catch(() => {})
            } else {
              if (authCheckTimeoutRef.current) {
                clearTimeout(authCheckTimeoutRef.current)
                authCheckTimeoutRef.current = null
              }
              hasLoggedOutRef.current = false
              setAuthenticated(false)
              setCurrentUser(null)
              setAuthToken('')
              localStorage.removeItem('auth_token')
              socket.close()
            }
            break
          }
          default:
            break
        }
      }

      socket.onclose = () => {
        setWsStatus('disconnected');
        setWs(null);
        window.ws = null;
        if (!hasLoggedOutRef.current) {
          reconnectTimeout = setTimeout(() => {
            if (authenticated && !hasLoggedOutRef.current) {
              connect();
            }
          }, 3000);
        }
      };

      socket.onerror = () => {
        setError("Network Error: Could not connect to API");
        socket.close();
      };
    };

    connect();
    return () => {
      if (socket) socket.close();
      if (reconnectTimeout) clearTimeout(reconnectTimeout);
    }
  }, [authenticated, authToken]);

  const sendCommand = (type, payload) => {
    // API-backed commands (no WebSocket required)
    if (type === 'save_config') {
      setSaveStatus({ type: 'normal', msg: 'Validating and saving...', errors: null })
      setIsSaving(true)
      return apiFetch('/api/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload || {}) })
        .then((data) => {
          setSaveStatus({ type: 'success', msg: data.message || 'Saved.', errors: data.errors || null })
          if (window.profileUsernameCallback) {
            window.profileUsernameCallback(data)
            delete window.profileUsernameCallback
          }
          return apiFetch(`/api/config?_=${Date.now()}`)
        })
        .then((cfg) => { if (cfg) setConfig(cfg) })
        .catch((err) => {
          const msg = err.message || 'Save failed'
          setSaveStatus({ type: 'error', msg, errors: err.fieldErrors || null })
          if (window.profileUsernameCallback) {
            window.profileUsernameCallback({ status: 'error', message: msg })
            delete window.profileUsernameCallback
          }
          throw err
        })
        .finally(() => setIsSaving(false))
    }
    if (type === 'save_user_configs') {
      setSaveStatus({ type: 'normal', msg: 'Validating and saving...', errors: null })
      setIsSaving(true)
      apiFetch('/api/streams/configs', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload || {}) })
        .then((data) => {
          setSaveStatus({ type: data.status === 'success' ? 'success' : 'error', msg: data.message || '', errors: data.errors || null })
        })
        .catch((err) => setSaveStatus({ type: 'error', msg: err.message || 'Save failed', errors: null }))
        .finally(() => setIsSaving(false))
      return
    }
    if (type === 'restart') {
      setIsRestarting(true)
      isRestartingRef.current = true
      apiFetch('/api/restart', { method: 'POST' }).catch(() => {
        setIsRestarting(false)
        isRestartingRef.current = false
      })
      return
    }
    if (type === 'get_users') {
      apiFetch('/api/streams')
        .then((list) => { if (window.deviceManagementCallback) window.deviceManagementCallback(list) })
        .catch((err) => { if (window.deviceManagementCallback) window.deviceManagementCallback({ error: err.message }); delete window.deviceManagementCallback })
      return
    }
    if (type === 'create_user') {
      const username = payload?.username || ''
      apiFetch('/api/streams', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username }) })
        .then((data) => { if (window.deviceActionCallback) window.deviceActionCallback(data && data.user ? {} : { error: (data && data.error) || 'Create failed' }); delete window.deviceActionCallback })
        .catch((err) => { if (window.deviceActionCallback) window.deviceActionCallback({ error: err.message }); delete window.deviceActionCallback })
      return
    }
    if (type === 'delete_user') {
      const username = payload?.username || ''
      apiFetch(`/api/streams/${encodeURIComponent(username)}`, { method: 'DELETE' })
        .then(() => { if (window.deviceActionCallback) window.deviceActionCallback({}); delete window.deviceActionCallback })
        .catch((err) => { if (window.deviceActionCallback) window.deviceActionCallback({ error: err.message }); delete window.deviceActionCallback })
      return
    }
    if (type === 'regenerate_token') {
      const username = payload?.username || ''
      apiFetch(`/api/streams/${encodeURIComponent(username)}/regenerate-token`, { method: 'POST' })
        .then((data) => { if (window.deviceActionCallback) window.deviceActionCallback({ token: data.token }); delete window.deviceActionCallback })
        .catch((err) => { if (window.deviceActionCallback) window.deviceActionCallback({ error: err.message }); delete window.deviceActionCallback })
      return
    }
    if (type === 'close_session') {
      apiFetch('/api/sessions/close', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: payload?.id || '' }) }).catch(() => {})
      return
    }
    if (type === 'update_password') {
      apiFetch('/api/auth/change-password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: payload?.username, password: payload?.password }) })
        .then(() => { if (window.passwordChangeCallback) window.passwordChangeCallback({}); delete window.passwordChangeCallback })
        .catch((err) => { if (window.passwordChangeCallback) window.passwordChangeCallback({ error: err.message }); delete window.passwordChangeCallback })
      return
    }
    if (type === 'refresh_caps') {
      apiFetch('/api/indexer/caps/refresh', { method: 'POST' })
        .then((data) => setIndexerCaps(data || {}))
        .catch(() => {})
      return
    }
    // Legacy WS-only commands (ignored; WS is for stats/logs only)
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type, payload }))
    }
  }

  if (!authChecked) {
    return (
      <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-background/80 backdrop-blur-sm gap-4">
        <Loader2 className="h-12 w-12 text-primary animate-spin" />
        <div className="text-xl font-semibold tracking-tight">Verifying session...</div>
      </div>
    )
  }

  if (!authenticated) {
    return <Login onLogin={handleLogin} version={version} />
  }

  if (mustChangePassword && currentUser) {
    return <ChangePassword username={currentUser} onPasswordChanged={() => {
      setMustChangePassword(false)
    }} />
  }

  if (error && wsStatus === 'disconnected') {
      return (
        <div className="flex flex-col h-screen items-center justify-center gap-4">
            <AlertCircle className="h-12 w-12 text-destructive animate-pulse" />
            <div className="text-xl font-semibold text-destructive">{error}</div>
            <p className="text-muted-foreground">Retrying connection...</p>
        </div>
      )
  }

  if (!stats || isRestarting) return (
    <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-background/80 backdrop-blur-sm gap-4">
        <Loader2 className="h-12 w-12 text-primary animate-spin" />
        <div className="text-xl font-semibold tracking-tight">
            {isRestarting ? "Restarting StreamNZB..." : "Initializing StreamNZB Dashboard..."}
        </div>
        {isRestarting && <p className="text-muted-foreground animate-pulse">Redirecting to home shortly...</p>}
    </div>
  )

  const isSettingsPage = activePage === 'settings'

  return (
    <SidebarProvider>
      <AppSidebar
        activePage={activePage}
        onNavigate={setActivePage}
        version={version}
        currentUser={currentUser}
        onLogout={handleLogout}
        theme={theme}
        onThemeChange={setTheme}
      />
      <SidebarInset className="min-h-0">
        <SiteHeader activePage={activePage} />
        <div className="flex flex-1 min-h-0 flex-col">
          {activePage === 'dashboard' && (
            <DashboardPage
              stats={stats}
              chartData={chartData}
              sendCommand={sendCommand}
              config={config}
            />
          )}
          {activePage === 'nzb-history' && (
            <NZBHistoryPage refreshTrigger={nzbAttemptsRefreshTrigger} />
          )}
          {activePage === 'install' && (
            <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
              <StreamManagement
                globalConfig={config}
                movieSearchQueries={config?.movie_search_queries || []}
                seriesSearchQueries={config?.series_search_queries || []}
                initialDevicesByName={config?.devices || {}}
              />
            </div>
          )}
          {activePage === 'logs' && (
            <LogsPage logs={logs} />
          )}
          {activePage === 'profile' && (
            <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
              <ProfilePage
                currentUser={currentUser}
                config={config}
                sendCommand={sendCommand}
                ws={ws}
                onUsernameChanged={setCurrentUser}
              />
            </div>
          )}
          {isSettingsPage && (
            <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
              <Settings
                initialConfig={config}
                sendCommand={sendCommand}
                saveStatus={saveStatus}
                clearSaveStatus={clearSaveStatus}
                isSaving={isSaving}
                adminToken={currentUser && currentUser !== 'legacy' ? authToken : null}
                indexerCaps={indexerCaps}
                stats={stats}
              />
            </div>
          )}
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}

export default App
