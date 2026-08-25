import { useCallback, useEffect, useRef, useState } from 'react'

import { apiFetch, getApiUrl, notifySpeedTestProgress, notifyUnauthorized } from '@/api'

export const DEFAULT_STREAM_NAME = 'default'

const MAX_HISTORY = 20
const MAX_LOGS = 200

export function useAdminRuntime({
  authenticated,
  hasLoggedOutRef,
  setAuthenticated,
  setCurrentUser,
  setMustChangePassword,
}) {
  const [stats, setStats] = useState(null)
  const [config, setConfig] = useState(null)
  const [saveStatus, setSaveStatus] = useState({ type: '', msg: '', errors: null })
  const [isSaving, setIsSaving] = useState(false)
  const [isRestarting, setIsRestarting] = useState(false)
  const isRestartingRef = useRef(false)
  const [error, setError] = useState(null)
  const [history, setHistory] = useState([])
  const [connHistory, setConnHistory] = useState([])
  const [streamHistory, setStreamHistory] = useState([])
  const [wsStatus, setWsStatus] = useState('connecting')
  const [ws, setWs] = useState(null)
  const [version, setVersion] = useState(null)
  const authCheckTimeoutRef = useRef(null)
  const activeSocketRef = useRef(null)
  const reconnectTimeoutRef = useRef(null)
  const [logs, setLogs] = useState([])
  const [indexerCaps, setIndexerCaps] = useState({})
  const [componentHealth, setComponentHealth] = useState([])
  const [nzbAttemptsRefreshTrigger, setNzbAttemptsRefreshTrigger] = useState(0)

  const resetRuntime = useCallback(() => {
    setStats(null)
    setConfig(null)
    setSaveStatus({ type: '', msg: '', errors: null })
    setIsSaving(false)
    setIsRestarting(false)
    isRestartingRef.current = false
    setError(null)
    setHistory([])
    setConnHistory([])
    setStreamHistory([])
    setWsStatus('connecting')
    setWs(null)
    window.ws = null
    setLogs([])
    setIndexerCaps({})
    setComponentHealth([])
    setNzbAttemptsRefreshTrigger(0)
  }, [])

  const refreshComponentHealth = useCallback(async () => {
    try {
      const data = await apiFetch('/api/health/components')
      if (data) setComponentHealth(data.components || [])
    } catch (err) {
      console.error('Failed to fetch component health', err)
    }
  }, [])

  const clearSaveStatus = useCallback(() => {
    setSaveStatus({ type: '', msg: '', errors: null })
  }, [])

  useEffect(() => {
    apiFetch('/api/info', { skipAuthNotify: true })
      .then((data) => data?.version && setVersion(data.version))
      .catch(() => {})
  }, [])

  useEffect(() => {
    if (!authenticated || hasLoggedOutRef.current) {
      resetRuntime()
    }
  }, [authenticated, hasLoggedOutRef, resetRuntime])

  useEffect(() => {
    if (!authenticated) return
    if (hasLoggedOutRef.current) return
    let cancelled = false

    const isActiveSocket = (socket) => !cancelled && !hasLoggedOutRef.current && activeSocketRef.current === socket

    const connect = () => {
      if (cancelled || hasLoggedOutRef.current) return
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
        reconnectTimeoutRef.current = null
      }
      const existingSocket = activeSocketRef.current
      if (existingSocket && existingSocket.readyState !== WebSocket.CLOSED) {
        return
      }

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      // Same origin as the page, so the session cookie rides along with the
      // handshake. It used to append ?token=, which put a credential in a URL
      // for no gain — after a reload the token was gone anyway and the cookie
      // was doing the work.
      const wsEndpoint = new URL(getApiUrl('/api/ws'), window.location.origin)
      wsEndpoint.protocol = protocol
      const socket = new WebSocket(wsEndpoint.toString())
      activeSocketRef.current = socket

      socket.onopen = () => {
        if (!isActiveSocket(socket)) return
        if (isRestartingRef.current) {
          window.location.reload()
          return
        }
        if (hasLoggedOutRef.current) {
          socket.close()
          return
        }
        setWsStatus('connected')
        setError(null)
        setWs(socket)
        window.ws = socket
        setLogs([])
      }

      socket.onmessage = (event) => {
        if (!isActiveSocket(socket)) return

        let msg
        try {
          msg = JSON.parse(event.data)
        } catch (err) {
          console.error('Failed to parse websocket message', err)
          return
        }
        switch (msg.type) {
          case 'stats': {
            const data = msg.payload
            setStats(data)
            const timestamp = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
            setHistory((prev) => [...prev, { time: timestamp, speed: data.total_speed_mbps }].slice(-MAX_HISTORY))
            setConnHistory((prev) => [...prev, { time: timestamp, conns: data.active_connections }].slice(-MAX_HISTORY))
            // Metered server-side alongside total speed; plotted as given, so the
            // two lines never drift apart on websocket jitter.
            setStreamHistory((prev) => [...prev, { time: timestamp, speeds: data.stream_speeds_mbps || {} }].slice(-MAX_HISTORY))
            break
          }
          case 'log_entry':
            setLogs((prev) => [...prev, msg.payload].slice(-MAX_LOGS))
            break
          case 'log_history':
            setLogs(msg.payload.slice(-MAX_LOGS))
            break
          case 'component_health': {
            const record = msg.payload
            if (!record?.kind || !record?.name) break
            setComponentHealth((prev) => {
              const rest = prev.filter((c) => !(c.kind === record.kind && c.name === record.name))
              return record.state === 'ok' ? rest : [...rest, record]
            })
            break
          }
          case 'nzb_attempts_updated':
            setNzbAttemptsRefreshTrigger((value) => value + 1)
            break
          case 'speedtest_progress':
            notifySpeedTestProgress(msg.payload)
            break
          case 'auth_info': {
            if (!isActiveSocket(socket)) return
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
              const currentSocket = socket
              apiFetch('/api/config')
                .then((data) => {
                  if (data && isActiveSocket(currentSocket)) setConfig(data)
                })
                .catch(() => {})
              apiFetch('/api/indexer/caps')
                .then((data) => {
                  if (data && isActiveSocket(currentSocket)) setIndexerCaps(data)
                })
                .catch(() => {})
              apiFetch('/api/health/components')
                .then((data) => {
                  if (data && isActiveSocket(currentSocket)) setComponentHealth(data.components || [])
                })
                .catch(() => {})
            } else {
              notifyUnauthorized({ source: 'ws_auth_info' })
              socket.close()
            }
            break
          }
          default:
            break
        }
      }

      socket.onclose = () => {
        if (cancelled || activeSocketRef.current !== socket) return
        activeSocketRef.current = null
        setWsStatus('disconnected')
        setWs(null)
        window.ws = null
        if (!hasLoggedOutRef.current) {
          reconnectTimeoutRef.current = setTimeout(() => {
            if (!cancelled && authenticated && !hasLoggedOutRef.current && !activeSocketRef.current) {
              connect()
            }
          }, 3000)
        }
      }

      socket.onerror = () => {
        if (!isActiveSocket(socket)) return
        setError('Network Error: Could not connect to API')
        socket.close()
      }
    }

    connect()
    return () => {
      cancelled = true
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
        reconnectTimeoutRef.current = null
      }
      if (activeSocketRef.current) {
        const socket = activeSocketRef.current
        activeSocketRef.current = null
        resetRuntime()
        setWsStatus('disconnected')
        socket.close()
      }
    }
  }, [authenticated, hasLoggedOutRef, resetRuntime, setAuthenticated, setCurrentUser, setMustChangePassword])

  const sendCommand = useCallback((type, payload) => {
    if (type === 'save_config') {
      setSaveStatus({ type: 'normal', msg: 'Validating and saving...', errors: null })
      setIsSaving(true)
      return apiFetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload || {}),
      })
        .then((data) => {
          setSaveStatus({ type: 'success', msg: data.message || 'Saved.', errors: data.errors || null })
          return apiFetch(`/api/config?_=${Date.now()}`)
            .then((cfg) => {
              if (cfg) setConfig(cfg)
              return data
            })
            .catch(() => data)
        })
        .catch((err) => {
          const msg = err.message || 'Save failed'
          setSaveStatus({ type: 'error', msg, errors: err.fieldErrors || null })
          throw err
        })
        .finally(() => setIsSaving(false))
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

    if (type === 'close_session') {
      apiFetch('/api/sessions/close', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: payload?.id || '' }),
      }).catch(() => {})
      return
    }

    if (type === 'update_password') {
      return apiFetch('/api/auth/change-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: payload?.username, password: payload?.password }),
      })
    }

    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type, payload }))
    }
  }, [ws])

  return {
    stats,
    config,
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
  }
}
