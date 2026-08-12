import React, { useState, useEffect, useCallback } from 'react'
import {
  Search, Pin, PinOff, Trash2, Loader2, ChevronLeft, ChevronRight,
  Film, Tv, Library, RefreshCw, CheckCircle2, Clock, XCircle, Play
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from '@/api'
import { cn, formatBytes } from '@/lib/utils'

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

function SummaryTile({ label, value, tone, children }) {
  return (
    <div className="rounded-lg border border-border/60 bg-muted/30 px-3 py-2">
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div
        className={cn(
          'mt-1 text-lg font-semibold',
          tone === 'warning' && 'text-amber-500'
        )}
      >
        {value}
      </div>
      {children}
    </div>
  )
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
  const hasActiveFilters = Boolean(debouncedSearch) || contentType !== 'all' || statusFilter !== 'all' || pinnedOnly

  return (
    <div className={cn('flex min-w-0 flex-1 min-h-0 flex-col gap-4 overflow-x-hidden px-4 py-4 md:gap-6 md:py-6 lg:px-6')}>
      <Card className="flex min-w-0 flex-1 min-h-0 flex-col overflow-hidden">
        <CardHeader>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1 max-w-[42rem] space-y-0.5">
              <CardTitle className="flex items-center gap-2">
                <Library className="size-5" />
                Library &amp; Pre-Indexer Storage
              </CardTitle>
              <CardDescription>
                Manage cached NZBs and archive blueprints stored in the local database.
              </CardDescription>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => { fetchStats(); fetchItems(); }}
              disabled={loading}
              className="shrink-0"
            >
              {loading ? <Loader2 className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
              Refresh
            </Button>
          </div>
        </CardHeader>
        <CardContent className="flex min-w-0 flex-1 min-h-0 flex-col gap-4 overflow-hidden">
          {/* Storage Summary */}
          <div className="grid grid-cols-2 gap-3 rounded-lg border border-border/60 bg-muted/20 p-3 md:grid-cols-4">
            <SummaryTile label="Cached Releases" value={stats?.total_items ?? '-'}>
              <div className="text-xs text-muted-foreground">
                Max {stats?.max_items ?? 5000}
                {(stats?.bad_items ?? 0) > 0 && (
                  <span className="text-destructive"> • {stats.bad_items} bad</span>
                )}
                {(stats?.pending_items ?? 0) > 0 && (
                  <span className="text-amber-500"> • {stats.pending_items} pending</span>
                )}
              </div>
              <div className="mt-1.5 h-1.5 w-full rounded-full bg-secondary overflow-hidden">
                <div
                  className={cn(
                    'h-full transition-all',
                    quotaPercent >= 90 ? 'bg-destructive' : quotaPercent >= 75 ? 'bg-amber-500' : 'bg-primary'
                  )}
                  style={{ width: `${quotaPercent}%` }}
                />
              </div>
            </SummaryTile>
            <SummaryTile label="NZB Storage" value={formatBytes(stats?.total_nzb_bytes)}>
              <div className="text-xs text-muted-foreground">Gzip compressed NZB XML</div>
            </SummaryTile>
            <SummaryTile label="Media Footprint" value={formatBytes(stats?.total_size_bytes)}>
              <div className="text-xs text-muted-foreground">Total media release size</div>
            </SummaryTile>
            <SummaryTile label="Pinned Releases" value={stats?.pinned_items ?? 0} tone="warning">
              <div className="text-xs text-muted-foreground">Protected from LRU eviction</div>
            </SummaryTile>
          </div>

          {/* Search & Filters */}
          <div className="flex flex-col gap-3 rounded-lg border border-border/60 bg-muted/20 p-3">
            <label className="space-y-1.5">
              <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Search</span>
              <div className="flex h-11 min-w-0 items-center gap-2 rounded-lg border border-border/60 bg-background px-3 shadow-xs">
                <Search className="size-4 shrink-0 text-muted-foreground" />
                <Input
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  placeholder="Search by title, ID, indexer..."
                  className="h-auto min-w-0 flex-1 border-0 bg-transparent p-0 shadow-none focus-visible:ring-0"
                />
              </div>
            </label>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
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

          {/* Release List */}
          {loading ? (
            <div className="flex items-center justify-center gap-2 py-12 text-muted-foreground">
              <Loader2 className="size-5 animate-spin" />
              Loading…
            </div>
          ) : items.length === 0 ? (
            <div className="flex flex-1 min-h-[320px] items-center justify-center rounded-lg border border-dashed px-6 py-8 text-center text-sm text-muted-foreground">
              {hasActiveFilters
                ? 'No cached releases match your active filters.'
                : 'Releases played or probed will automatically be cached here for instant playback.'}
            </div>
          ) : (
            <div className="flex min-w-0 flex-1 min-h-0 flex-col overflow-y-auto rounded-lg border border-border/60 bg-background/40 p-3 md:p-4">
              <div className="flex min-w-0 flex-col gap-3">
                {items.map((item) => (
                  <div
                    key={item.id}
                    className={cn(
                      'overflow-hidden rounded-xl border bg-background/95 shadow-sm transition-all duration-200 hover:bg-muted/10',
                      item.status === 'bad'
                        ? 'border-destructive/40 bg-destructive/[0.03]'
                        : item.pinned
                          ? 'border-amber-500/40 bg-amber-500/[0.03]'
                          : 'border-border/60'
                    )}
                  >
                    <div className="flex w-full min-w-0 flex-col gap-3 px-4 py-4 md:px-5">
                      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
                        <div className="flex min-w-0 items-center gap-2">
                          <div className="inline-flex items-center rounded-md border border-border/60 bg-muted/30 px-2.5 py-0.5 text-xs text-muted-foreground">
                            {formatDate(item.created_at)}
                          </div>
                          {item.pinned && (
                            <Pin className="h-4 w-4 shrink-0 text-amber-500 fill-amber-500" />
                          )}
                        </div>
                        <div className="flex items-center gap-1">
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
                      </div>
                      <div className="space-y-1.5">
                        <div className="line-clamp-2 text-base font-semibold leading-snug [overflow-wrap:anywhere]" title={item.release_title || item.releaseTitle}>
                          {item.release_title || item.releaseTitle}
                        </div>
                        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                          <Badge variant="secondary" className="capitalize text-[11px]">
                            {item.content_type === 'series' ? (
                              <><Tv className="h-3 w-3 mr-1" /> TV</>
                            ) : (
                              <><Film className="h-3 w-3 mr-1" /> Movie</>
                            )}
                          </Badge>
                          {item.has_blueprint && (
                            <Badge variant="outline" className="text-[9px] py-0 px-1 border-blue-500/40 text-blue-500">
                              Blueprint
                            </Badge>
                          )}
                          <StatusBadge status={item.status} reason={item.status_reason} />
                          <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
                            {[
                              item.content_id,
                              item.season > 0 ? `S${String(item.season).padStart(2, '0')}E${String(item.episode).padStart(2, '0')}` : null,
                              item.indexer_name || 'Library',
                              formatBytes(item.size_bytes),
                            ].filter(Boolean).join(' • ')}
                          </span>
                        </div>
                        {item.media_file_name && (
                          <div className="text-xs text-emerald-600 dark:text-emerald-400 [overflow-wrap:anywhere]" title={item.media_file_name}>
                            {item.media_file_name}
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Pagination */}
          {totalPages > 1 && (
            <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 text-xs text-muted-foreground">
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
        </CardContent>
      </Card>

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
