import React, { useState, useEffect, useCallback } from 'react'
import {
  Search, Pin, PinOff, Trash2, HardDrive, FileArchive, Database,
  Loader2, ChevronLeft, ChevronRight, Film, Tv, Library, Layers, RefreshCw,
  CheckCircle2, Clock, XCircle, Play
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from '@/api'

function formatBytes(bytes, decimals = 2) {
  if (!bytes || bytes === 0) return '0 B'
  const k = 1024
  const dm = decimals < 0 ? 0 : decimals
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i]}`
}

function StatusBadge({ status, reason }) {
  switch (status) {
    case 'good':
      return (
        <Badge variant="outline" className="text-[9px] py-0 px-1 border-emerald-500/40 text-emerald-500 gap-0.5">
          <CheckCircle2 className="h-2.5 w-2.5" /> Good
        </Badge>
      )
    case 'bad':
      return (
        <Badge
          variant="outline"
          className="text-[9px] py-0 px-1 border-destructive/50 text-destructive gap-0.5"
          title={reason || 'Marked bad'}
        >
          <XCircle className="h-2.5 w-2.5" /> Bad
        </Badge>
      )
    case 'pending':
    default:
      return (
        <Badge variant="outline" className="text-[9px] py-0 px-1 border-amber-500/40 text-amber-500 gap-0.5">
          <Clock className="h-2.5 w-2.5" /> Pending
        </Badge>
      )
  }
}

function formatDate(dateStr) {
  if (!dateStr) return '-'
  try {
    const d = new Date(dateStr)
    return d.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    })
  } catch {
    return dateStr
  }
}

export function LibraryPage({ onPlay } = {}) {
  const [items, setItems] = useState([])
  const [total, setTotal] = useState(0)
  const [stats, setStats] = useState(null)
  const [loading, setLoading] = useState(true)
  const [searchQuery, setSearchQuery] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [contentType, setContentType] = useState('all')
  const [statusFilter, setStatusFilter] = useState('all')
  const [pinnedOnly, setPinnedOnly] = useState(false)
  const [page, setPage] = useState(1)
  const limit = 25

  const [deleteTarget, setDeleteTarget] = useState(null)
  const [actionInFlight, setActionInFlight] = useState('')

  // Debounce search query
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedSearch(searchQuery)
      setPage(1)
    }, 300)
    return () => clearTimeout(timer)
  }, [searchQuery])

  const fetchStats = useCallback(async () => {
    try {
      const data = await apiFetch('/api/library/stats')
      if (data) {
        setStats(data)
      }
    } catch (err) {
      console.error('Failed to fetch library stats', err)
    }
  }, [])

  const fetchItems = useCallback(async () => {
    setLoading(true)
    try {
      const offset = (page - 1) * limit
      const params = new URLSearchParams({
        offset: offset.toString(),
        limit: limit.toString(),
      })
      if (debouncedSearch) params.set('q', debouncedSearch)
      if (contentType !== 'all') params.set('type', contentType)
      if (statusFilter !== 'all') params.set('status', statusFilter)
      if (pinnedOnly) params.set('pinned', 'true')

      const data = await apiFetch(`/api/library?${params.toString()}`)
      if (data) {
        setItems(data.items || [])
        setTotal(data.total || 0)
      }
    } catch (err) {
      console.error('Failed to fetch library items', err)
    } finally {
      setLoading(false)
    }
  }, [page, debouncedSearch, contentType, statusFilter, pinnedOnly])

  useEffect(() => {
    fetchStats()
    fetchItems()
  }, [fetchStats, fetchItems])

  const handleTogglePin = async (item) => {
    const nextPinned = !item.pinned
    setActionInFlight(item.id)
    try {
      const data = await apiFetch('/api/library/pin', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: item.id, pinned: nextPinned }),
      })
      if (data) {
        setItems(prev => prev.map(i => i.id === item.id ? { ...i, pinned: nextPinned } : i))
        fetchStats()
      }
    } catch (err) {
      console.error('Failed to toggle pin', err)
    } finally {
      setActionInFlight('')
    }
  }

  const handleDeleteConfirm = async () => {
    if (!deleteTarget) return
    const id = deleteTarget.id
    setActionInFlight(id)
    try {
      const data = await apiFetch(`/api/library/delete?id=${encodeURIComponent(id)}`, {
        method: 'DELETE',
      })
      if (data) {
        setItems(prev => prev.filter(i => i.id !== id))
        setTotal(prev => Math.max(0, prev - 1))
        fetchStats()
      }
    } catch (err) {
      console.error('Failed to delete library item', err)
    } finally {
      setActionInFlight('')
      setDeleteTarget(null)
    }
  }

  const totalPages = Math.ceil(total / limit) || 1
  const quotaPercent = stats && stats.max_items > 0
    ? Math.min(100, Math.round((stats.total_items / stats.max_items) * 100))
    : 0

  return (
    <div className="space-y-6 p-4 md:p-6 max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold tracking-tight flex items-center gap-2">
            <Library className="h-6 w-6 text-primary" />
            Library & Pre-Indexer Storage
          </h1>
          <p className="text-sm text-muted-foreground mt-1">
            Manage cached NZBs and archive blueprints stored in the local database to serve playback in &lt; 5ms.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => { fetchStats(); fetchItems(); }}
          className="shrink-0 gap-1.5 self-start sm:self-center"
        >
          <RefreshCw className="h-4 w-4" />
          Refresh
        </Button>
      </div>

      {/* Storage Summary Cards */}
      <div className="grid gap-4 md:grid-cols-4">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Cached Releases</CardTitle>
            <Database className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{stats?.total_items ?? '-'}</div>
            <p className="text-xs text-muted-foreground mt-1">
              Max capacity: {stats?.max_items ?? 5000} items
              {(stats?.bad_items ?? 0) > 0 && (
                <span className="text-destructive"> • {stats.bad_items} bad</span>
              )}
              {(stats?.pending_items ?? 0) > 0 && (
                <span className="text-amber-500"> • {stats.pending_items} pending</span>
              )}
            </p>
            <div className="mt-2 h-1.5 w-full rounded-full bg-secondary overflow-hidden">
              <div
                className={`h-full transition-all ${
                  quotaPercent >= 90 ? 'bg-destructive' : quotaPercent >= 75 ? 'bg-amber-500' : 'bg-primary'
                }`}
                style={{ width: `${quotaPercent}%` }}
              />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">NZB Storage</CardTitle>
            <FileArchive className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{formatBytes(stats?.total_nzb_bytes)}</div>
            <p className="text-xs text-muted-foreground mt-1">
              Gzip compressed NZB XML
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Media Footprint</CardTitle>
            <HardDrive className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{formatBytes(stats?.total_size_bytes)}</div>
            <p className="text-xs text-muted-foreground mt-1">
              Total media release size
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Pinned Releases</CardTitle>
            <Pin className="h-4 w-4 text-amber-500" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-amber-500">{stats?.pinned_items ?? 0}</div>
            <p className="text-xs text-muted-foreground mt-1">
              Protected from LRU eviction
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Search & Filter Toolbar */}
      <Card>
        <CardContent className="pt-6">
          <div className="flex flex-col md:flex-row items-center gap-4 justify-between">
            <div className="relative w-full md:w-96">
              <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
              <Input
                type="text"
                placeholder="Search by title, ID, indexer..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="pl-9 h-9"
              />
            </div>

            <div className="flex items-center gap-4 w-full md:w-auto flex-wrap">
              {/* Content Type Filter */}
              <div className="flex items-center gap-2">
                <span className="text-xs font-medium text-muted-foreground">Type:</span>
                <select
                  value={contentType}
                  onChange={(e) => { setContentType(e.target.value); setPage(1); }}
                  className="h-9 rounded-md border border-input bg-background px-3 py-1 text-xs font-medium shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                >
                  <option value="all">All Types</option>
                  <option value="movie">Movies</option>
                  <option value="series">TV Shows</option>
                </select>
              </div>

              {/* Status Filter */}
              <div className="flex items-center gap-2">
                <span className="text-xs font-medium text-muted-foreground">Status:</span>
                <select
                  value={statusFilter}
                  onChange={(e) => { setStatusFilter(e.target.value); setPage(1); }}
                  className="h-9 rounded-md border border-input bg-background px-3 py-1 text-xs font-medium shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                >
                  <option value="all">All Statuses</option>
                  <option value="good">Good</option>
                  <option value="pending">Pending</option>
                  <option value="bad">Bad</option>
                </select>
              </div>

              {/* Pinned Switch */}
              <div className="flex items-center gap-2">
                <Switch
                  id="pinned-only"
                  checked={pinnedOnly}
                  onCheckedChange={(val) => { setPinnedOnly(val); setPage(1); }}
                />
                <label htmlFor="pinned-only" className="text-xs font-medium text-muted-foreground cursor-pointer flex items-center gap-1">
                  <Pin className="h-3.5 w-3.5 text-amber-500" />
                  Pinned Only
                </label>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Main Data Table */}
      <Card>
        <CardContent className="p-0">
          {loading ? (
            <div className="flex items-center justify-center p-12">
              <Loader2 className="h-8 w-8 animate-spin text-primary" />
            </div>
          ) : items.length === 0 ? (
            <div className="text-center p-12 text-muted-foreground space-y-2">
              <Database className="h-10 w-10 mx-auto text-muted-foreground/50" />
              <p className="text-base font-medium">No library items found</p>
              <p className="text-xs text-muted-foreground max-w-sm mx-auto">
                {debouncedSearch || contentType !== 'all' || pinnedOnly
                  ? 'No cached releases match your active filters.'
                  : 'Releases played or probed will automatically be cached here for < 5ms instant playback.'}
              </p>
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm text-left border-collapse">
                <thead>
                  <tr className="border-b border-border text-xs text-muted-foreground font-medium bg-muted/30">
                    <th className="w-[40px] px-3 py-3"></th>
                    <th className="px-4 py-3">Release Title</th>
                    <th className="w-[100px] px-3 py-3">Type</th>
                    <th className="w-[120px] px-3 py-3">Indexer</th>
                    <th className="w-[110px] px-3 py-3">Size</th>
                    <th className="w-[160px] px-3 py-3">Cached Date</th>
                    <th className="w-[100px] px-3 py-3 text-right">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/40">
                  {items.map((item) => (
                    <tr key={item.id} className={
                      item.status === 'bad'
                        ? "bg-destructive/5 dark:bg-destructive/10 hover:bg-destructive/10 transition-colors"
                        : item.pinned
                          ? "bg-amber-500/5 dark:bg-amber-500/10 hover:bg-amber-500/10"
                          : "hover:bg-muted/40 transition-colors"
                    }>
                      <td className="px-3 py-3 text-center">
                        {item.pinned ? (
                          <Pin className="h-4 w-4 text-amber-500 fill-amber-500 inline-block" />
                        ) : (
                          <span className="h-4 w-4 inline-block" />
                        )}
                      </td>
                      <td className="px-4 py-3 font-medium max-w-md">
                        <div className="truncate font-mono text-xs text-foreground" title={item.release_title || item.releaseTitle}>
                          {item.release_title || item.releaseTitle}
                        </div>
                        <div className="flex items-center gap-2 mt-0.5 text-[11px] text-muted-foreground">
                          {item.content_id && <span className="font-mono">{item.content_id}</span>}
                          {item.season > 0 && <span>S{String(item.season).padStart(2, '0')}E{String(item.episode).padStart(2, '0')}</span>}
                          {item.media_file_name && (
                            <span className="truncate max-w-[200px] text-emerald-600 dark:text-emerald-400" title={item.media_file_name}>
                              • {item.media_file_name}
                            </span>
                          )}
                          {item.has_blueprint && (
                            <Badge variant="outline" className="text-[9px] py-0 px-1 border-blue-500/40 text-blue-500">
                              Blueprint
                            </Badge>
                          )}
                          <StatusBadge status={item.status} reason={item.status_reason} />
                        </div>
                      </td>
                      <td className="px-3 py-3">
                        <Badge variant="secondary" className="capitalize text-[11px]">
                          {item.content_type === 'series' ? (
                            <><Tv className="h-3 w-3 mr-1" /> TV</>
                          ) : (
                            <><Film className="h-3 w-3 mr-1" /> Movie</>
                          )}
                        </Badge>
                      </td>
                      <td className="px-3 py-3 text-xs text-muted-foreground">
                        {item.indexer_name || 'Library'}
                      </td>
                      <td className="px-3 py-3 text-xs font-mono">
                        {formatBytes(item.size_bytes)}
                      </td>
                      <td className="px-3 py-3 text-xs text-muted-foreground whitespace-nowrap">
                        {formatDate(item.created_at)}
                      </td>
                      <td className="px-3 py-3 text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={actionInFlight === item.id || !item.has_nzb || !onPlay}
                            onClick={() => onPlay?.(item)}
                            title={item.has_nzb ? "Play now via Direct Play" : "No NZB stored for this item"}
                            className="h-8 w-8 text-muted-foreground hover:text-primary"
                          >
                            <Play className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={actionInFlight === item.id}
                            onClick={() => handleTogglePin(item)}
                            title={item.pinned ? "Unpin release" : "Pin release (prevent deletion)"}
                            className="h-8 w-8 text-muted-foreground hover:text-amber-500"
                          >
                            {item.pinned ? <PinOff className="h-4 w-4 text-amber-500" /> : <Pin className="h-4 w-4" />}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={actionInFlight === item.id}
                            onClick={() => setDeleteTarget(item)}
                            title="Delete release from library"
                            className="h-8 w-8 text-muted-foreground hover:text-destructive"
                          >
                            <Trash2 className="h-4 w-4" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Pagination Footer */}
      {totalPages > 1 && (
        <div className="flex items-center justify-between text-xs text-muted-foreground">
          <div>
            Showing {((page - 1) * limit) + 1} to {Math.min(total, page * limit)} of {total} releases
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={page <= 1}
              onClick={() => setPage(prev => Math.max(1, prev - 1))}
              className="h-8 gap-1 px-2.5"
            >
              <ChevronLeft className="h-4 w-4" /> Previous
            </Button>
            <span className="font-medium text-foreground">
              Page {page} of {totalPages}
            </span>
            <Button
              variant="outline"
              size="sm"
              disabled={page >= totalPages}
              onClick={() => setPage(prev => Math.min(totalPages, prev + 1))}
              className="h-8 gap-1 px-2.5"
            >
              Next <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </div>
      )}

      {/* Delete Confirmation Dialog */}
      {deleteTarget && (
        <ConfirmDialog
          open={!!deleteTarget}
          onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}
          title="Delete Cached Release"
          description={`Are you sure you want to remove "${deleteTarget.release_title || deleteTarget.releaseTitle}" from the local library?`}
          confirmText="Delete"
          destructive
          onConfirm={handleDeleteConfirm}
        />
      )}
    </div>
  )
}
