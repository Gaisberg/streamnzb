import React, { useState } from 'react'
import { AlertTriangle, Clock, Loader2, RefreshCw } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { apiFetch } from '@/api'
import { cn } from '@/lib/utils'
import { formatSince, healthReasonHint, healthReasonLabel, isBlocked } from '@/lib/health'

// ComponentHealthBadge is the inline marker on an indexer or provider card.
// Nothing renders when the component is fine — a healthy row should look like
// an ordinary row, not like a row that passed an inspection.
export function ComponentHealthBadge({ record, className }) {
  if (!record || record.state === 'ok') return null

  const blocked = isBlocked(record)
  const label = healthReasonLabel(record.reason)
  const hint = healthReasonHint(record.reason)
  const since = formatSince(record.since)

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge
          variant="outline"
          className={cn(
            'gap-1 whitespace-nowrap',
            blocked
              ? 'border-destructive/50 text-destructive'
              : 'border-amber-500/50 text-amber-500',
            className
          )}
        >
          {blocked ? <AlertTriangle className="h-3 w-3" /> : <Clock className="h-3 w-3" />}
          {label}
        </Badge>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        <div className="space-y-1">
          <div className="font-medium">
            {label}
            {since ? <span className="font-normal text-muted-foreground"> · {since}</span> : null}
          </div>
          {hint ? <div className="text-xs">{hint}</div> : null}
          {record.detail ? (
            <div className="text-xs text-muted-foreground [overflow-wrap:anywhere]">{record.detail}</div>
          ) : null}
        </div>
      </TooltipContent>
    </Tooltip>
  )
}

// ComponentHealthNotice sits inside the component's own dashboard card — the
// warning lives on the thing it is about, not in a separate list the reader
// has to correlate by name. Renders nothing while the component is healthy.
export function ComponentHealthNotice({ record, onRefresh }) {
  const [retrying, setRetrying] = useState(false)
  if (!record || record.state === 'ok') return null

  const blocked = isBlocked(record)
  const since = formatSince(record.since)
  const hint = healthReasonHint(record.reason)

  // The verdict itself arrives over the websocket rather than from this
  // response, so a component that recovered updates everywhere at once; the
  // refresh is the backstop for a socket that dropped.
  const retry = async () => {
    if (retrying) return
    setRetrying(true)
    try {
      await apiFetch('/api/health/components/retry', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind: record.kind, name: record.name }),
      })
    } catch (err) {
      console.error('Failed to retry component', err)
    } finally {
      setRetrying(false)
      onRefresh?.()
    }
  }

  return (
    <div
      className={cn(
        'mt-2 rounded-md border px-2 py-1.5 text-[11px] leading-snug',
        blocked ? 'border-destructive/40 bg-destructive/[0.06] text-destructive' : 'border-amber-500/40 bg-amber-500/[0.06] text-amber-600 dark:text-amber-400'
      )}
    >
      <div className="flex items-center gap-1.5">
        {blocked ? <AlertTriangle className="h-3 w-3 shrink-0" /> : <Clock className="h-3 w-3 shrink-0" />}
        <span className="font-medium">{healthReasonLabel(record.reason)}</span>
        {since ? <span className="opacity-70">· {since}</span> : null}
        {/* Only blocked components have anything to re-check: a quota or a
            cooldown ends on a clock, and probing it early cannot change that. */}
        {blocked && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={retrying}
            onClick={retry}
            className="ml-auto h-5 shrink-0 gap-1 px-1.5 text-[10px] text-current hover:text-current"
          >
            {retrying ? <Loader2 className="size-3 animate-spin" /> : <RefreshCw className="size-3" />}
            Check again
          </Button>
        )}
      </div>
      {hint ? <div className="mt-0.5 text-muted-foreground [overflow-wrap:anywhere]">{hint}</div> : null}
    </div>
  )
}
