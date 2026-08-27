import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import Settings from '@/Settings'
import Login from '@/components/Login'
import ChangePassword from '@/components/ChangePassword'
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar"
import { AppSidebar } from "@/components/AppSidebar"
import { SiteHeader } from "@/components/SiteHeader"
import { DashboardPage } from "@/components/DashboardPage"
import { StatisticsPage } from "@/components/StatisticsPage"
import { LogsPage } from "@/components/LogsPage"
import { FiltersPage } from "@/components/FiltersPage"
import { FormattingPage } from "@/components/FormattingPage"
import { MetadataPage } from "@/components/MetadataPage"
import { NZBHistoryPage } from "@/components/NZBHistoryPage"
import { LibraryPage } from "@/components/LibraryPage"
import { ProfilePage } from "@/components/ProfilePage"
import { DirectPlayPage } from "@/components/DirectPlayPage"
import StreamManagement from '@/components/StreamManagement'
import { apiFetch, UNAUTHORIZED_EVENT } from '@/api'
import { streamSeriesKey } from '@/lib/utils'
import { AlertCircle, Loader2 } from "lucide-react"

import ErrorBoundary from '@/components/ErrorBoundary'
import { useAdminRuntime } from '@/hooks/useAdminRuntime'
import { isAvailNZBEnabled } from '@/lib/availnzb'

// Stream speeds are namespaced so a stream name can never shadow the chart's own
// `time` / `speed` / `conns` keys.
function streamSeries(speeds) {
  const series = {}
  Object.entries(speeds || {}).forEach(([name, mbps]) => {
    series[streamSeriesKey(name)] = mbps
  })
  return series
}

function App() {
  const [authChecked, setAuthChecked] = useState(false)
  const [authenticated, setAuthenticated] = useState(false)
  const [currentUser, setCurrentUser] = useState(null)
  const [mustChangePassword, setMustChangePassword] = useState(false)
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'system')
  const hasLoggedOutRef = useRef(false)
  const getInitialPageFromHash = () => {
    const hash = window.location.hash.replace(/^#\/?/, '')
    return hash || 'dashboard'
  }

  const [activePage, setActivePage] = useState(getInitialPageFromHash)

  useEffect(() => {
    const handleHashChange = () => {
      const hash = window.location.hash.replace(/^#\/?/, '')
      setActivePage(hash || 'dashboard')
    }
    window.addEventListener('hashchange', handleHashChange)
    return () => {
      window.removeEventListener('hashchange', handleHashChange)
    }
  }, [])

  const handleNavigate = useCallback((page) => {
    setActivePage(page)
    if (window.location.hash.replace(/^#\/?/, '') !== page) {
      window.location.hash = page
    }
  }, [])
  const [availNZBStatus, setAvailNZBStatus] = useState(null)
  // Library item handed to Direct Play when the user hits Play in the Library.
  const [directPlayLibraryItem, setDirectPlayLibraryItem] = useState(null)
  const [availNZBStatusLoading, setAvailNZBStatusLoading] = useState(false)
  const [availNZBStatusError, setAvailNZBStatusError] = useState('')
  const availNZBStatusLoadedRef = useRef(false)
  const availNZBStatusLoadingRef = useRef(false)

  const {
    stats,
    config,
    applyStreams,
    saveStatus,
    clearSaveStatus,
    isSaving,
    isRestarting,
    error,
    history,
    connHistory,
    streamHistory,
    wsStatus,
    ws,
    version,
    logs,
    indexerCaps,
    componentHealth,
    refreshComponentHealth,
    nzbAttemptsRefreshTrigger,
    sendCommand,
  } = useAdminRuntime({
    authenticated,
    hasLoggedOutRef,
    setAuthenticated,
    setCurrentUser,
    setMustChangePassword,
  })

  const chartData = useMemo(() => {
    const totalPoints = 20
    const points = []
    const count = history.length
    const missing = Math.max(0, totalPoints - count)

    for (let i = 0; i < missing; i++) {
      const secAgo = totalPoints - i - 1
      points.push({
        time: `-${secAgo}s`,
        speed: 0,
        conns: 0,
      })
    }

    for (let i = 0; i < count; i++) {
      const secAgo = count - i - 1
      const label = secAgo === 0 ? 'now' : `-${secAgo}s`
      // Per-stream speeds ride along under their stream name so the chart can pick
      // out any subset of streams without a second data pass.
      points.push({
        time: history[i]?.time || label,
        speed: history[i]?.speed ?? 0,
        conns: connHistory[i]?.conns ?? 0,
        ...streamSeries(streamHistory[i]?.speeds),
      })
    }

    return points
  }, [history, connHistory, streamHistory])

  useEffect(() => {
    const pathParts = window.location.pathname.split('/').filter(p => p !== '')
    const isLegacyPath = pathParts.length > 0 && pathParts[0] !== 'api'

    // Not authenticated as the admin. Under a path prefix that is the Stremio
    // token-in-URL case: the token in the path is the credential and there is
    // no admin session to establish. Anywhere else it means the login screen.
    //
    // This used to be decided up front from a token in localStorage, which had
    // it backwards — an admin reaching the UI through a reverse-proxy sub-path
    // was read as a stream client whenever that copy was missing. Asking the
    // server first answers it for both.
    const applyUnauthenticated = () => {
      if (isLegacyPath) {
        hasLoggedOutRef.current = false
        setAuthenticated(true)
        setCurrentUser('legacy')
        return
      }
      setAuthenticated(false)
    }

    // Ask the server before showing anything: a container restart generates a
    // new admin token, which leaves the browser holding a cookie that no longer
    // authenticates.
    apiFetch('/api/auth/check', { skipAuthNotify: true })
      .then(data => {
        if (data && data.authenticated) {
          hasLoggedOutRef.current = false
          setAuthenticated(true)
          setCurrentUser(data.username)
          setMustChangePassword(data.must_change_password || false)
          return
        }
        applyUnauthenticated()
      })
      // Also the 401 path: apiFetch rejects on a non-OK response.
      .catch(applyUnauthenticated)
      .finally(() => {
        setAuthChecked(true)
      })
  }, [])

  const handleLogin = (username, mustChange) => {
    hasLoggedOutRef.current = false
    setAuthenticated(true)
    setCurrentUser(username)
    setMustChangePassword(mustChange)
  }

  const clearAuthState = useCallback(() => {
    hasLoggedOutRef.current = true
    setAuthenticated(false)
    setCurrentUser(null)
    setMustChangePassword(false)
    if (ws) {
      ws.close()
    }
    window.ws = null
  }, [ws])

  const handleLogout = useCallback(() => {
    hasLoggedOutRef.current = true
    apiFetch('/api/auth/logout', { method: 'POST', skipAuthNotify: true }).catch(() => {})
    clearAuthState()
  }, [clearAuthState])

  useEffect(() => {
    const handleUnauthorized = () => {
      if (!authenticated || hasLoggedOutRef.current) return
      clearAuthState()
    }

    window.addEventListener(UNAUTHORIZED_EVENT, handleUnauthorized)
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, handleUnauthorized)
    }
  }, [authenticated, clearAuthState])

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

  const settingsTabMap = {
    'settings': 'general',
    'settings-general': 'general',
    'settings-indexers': 'indexers',
    'settings-providers': 'providers',
    'settings-search': 'search_query',
    'settings-integrations': 'integrations',
    'settings-advanced': 'advanced',
  }
  const isSettingsPage = activePage in settingsTabMap
  const availNZBEnabled = isAvailNZBEnabled(config?.availnzb_mode)

  const fetchAvailNZBStatus = useCallback(async (force = false) => {
    if (!authenticated || !config || !availNZBEnabled) return
    if (availNZBStatusLoadingRef.current) return
    if (!force && availNZBStatusLoadedRef.current) return

    availNZBStatusLoadingRef.current = true
    setAvailNZBStatusLoading(true)
    setAvailNZBStatusError('')
    try {
      const data = await apiFetch('/api/availnzb/status')
      setAvailNZBStatus(data || null)
      availNZBStatusLoadedRef.current = true
    } catch (error) {
      setAvailNZBStatus(null)
      setAvailNZBStatusError(error.message || 'Failed to load AvailNZB status.')
      availNZBStatusLoadedRef.current = true
    } finally {
      availNZBStatusLoadingRef.current = false
      setAvailNZBStatusLoading(false)
    }
  }, [authenticated, config, availNZBEnabled])

  useEffect(() => {
    if (!authenticated || !config || !availNZBEnabled) {
      availNZBStatusLoadedRef.current = false
      availNZBStatusLoadingRef.current = false
      setAvailNZBStatus(null)
      setAvailNZBStatusError('')
      setAvailNZBStatusLoading(false)
      return
    }
    void fetchAvailNZBStatus(false)
  }, [authenticated, config, availNZBEnabled, fetchAvailNZBStatus])

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
    }} requireCurrentPassword={false} />
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

  return (
    <SidebarProvider>
      <AppSidebar
        activePage={activePage}
        onNavigate={handleNavigate}
        version={version}
        currentUser={currentUser}
        forcePasswordResetEnabled={Boolean(config?.env_overrides?.includes('admin_must_change_password'))}
        onLogout={handleLogout}
        theme={theme}
        onThemeChange={setTheme}
      />
      <SidebarInset className="min-h-0 min-w-0 overflow-x-hidden">
        <SiteHeader activePage={activePage} />
        {/* Keyed on the page so navigating away clears a crashed one: React
            never resets a boundary on its own, and without the key the fallback
            would follow the user to every other page. */}
        <ErrorBoundary key={activePage} label={activePage.replace(/-/g, ' ')}>
          <div className="flex min-w-0 flex-1 min-h-0 flex-col overflow-x-hidden">
            {activePage === 'dashboard' && (
              <DashboardPage
                stats={stats}
                chartData={chartData}
                sendCommand={sendCommand}
                config={config}
                onNavigate={handleNavigate}
                availNZBStatus={availNZBStatus}
                availNZBStatusLoading={availNZBStatusLoading}
                availNZBStatusError={availNZBStatusError}
                componentHealth={componentHealth}
                onRefreshComponentHealth={refreshComponentHealth}
              />
            )}
            {activePage === 'statistics' && (
              <StatisticsPage />
            )}
            {activePage === 'nzb-history' && (
              <NZBHistoryPage refreshTrigger={nzbAttemptsRefreshTrigger} />
            )}
            {activePage === 'library' && (
              <LibraryPage
                onPlay={(item) => {
                  setDirectPlayLibraryItem(item)
                  handleNavigate('direct-play')
                }}
              />
            )}
            {activePage === 'direct-play' && (
              <DirectPlayPage
                libraryItem={directPlayLibraryItem}
                onLibraryItemConsumed={() => setDirectPlayLibraryItem(null)}
              />
            )}
            {activePage === 'install' && (
              <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
                <StreamManagement
                  globalConfig={config}
                  movieSearchQueries={config?.movie_search_queries || []}
                  seriesSearchQueries={config?.series_search_queries || []}
                  initialStreamsByName={config?.streams || {}}
                  // Stream saves go through /api/streams/*, not the config
                  // endpoint, so the fresh bindings are folded into the shared
                  // config here — the "In use" hints on the Filters,
                  // Formatting and Metadata pages read config.streams.
                  onStreamsChange={applyStreams}
                />
              </div>
            )}
            {activePage === 'logs' && (
              <LogsPage logs={logs} />
            )}
            {activePage === 'filters' && (
              <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
                <FiltersPage
                  config={config}
                  onSave={(filterProfiles) => sendCommand('save_config', { filter_profiles: filterProfiles })}
                  onSaveLibraries={(defineLibraries) => sendCommand('save_config', { define_libraries: defineLibraries })}
                  isSaving={isSaving}
                  saveStatus={saveStatus}
                />
              </div>
            )}
            {activePage === 'metadata' && (
              <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
                <MetadataPage
                  config={config}
                  onPersist={(patch) => sendCommand('save_config', patch)}
                  isSaving={isSaving}
                  saveStatus={saveStatus}
                />
              </div>
            )}
            {activePage === 'formatting' && (
              <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
                <FormattingPage
                  config={config}
                  onPersist={(patch) => sendCommand('save_config', patch)}
                  isSaving={isSaving}
                  saveStatus={saveStatus}
                />
              </div>
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
                  indexerCaps={indexerCaps}
                  componentHealth={componentHealth}
                  activeTab={settingsTabMap[activePage] || 'general'}
                  hideTabs={true}
                />
              </div>
            )}
          </div>
        </ErrorBoundary>
      </SidebarInset>
    </SidebarProvider>
  )
}

export default App
