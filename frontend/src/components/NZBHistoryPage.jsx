import { useState, useEffect, useCallback } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { History, Loader2, ExternalLink, RefreshCw, Copy, Check } from 'lucide-react'
import { getApiUrl, apiFetch } from '../api'
import { cn } from '@/lib/utils'

function formatSize(bytes) {
  if (bytes <= 0) return '—'
  const gb = bytes / (1024 * 1024 * 1024)
  if (gb >= 1) return `${gb.toFixed(1)} GB`
  const mb = bytes / (1024 * 1024)
  if (mb >= 1) return `${mb.toFixed(0)} MB`
  const kb = bytes / 1024
  return `${kb.toFixed(0)} KB`
}

function formatAttemptResult(attempt) {
	if (attempt.preload) return 'Preload'
	return attempt.success ? 'OK' : 'Failed'
}

function buildBadMatchReport(attempt) {
	const details = [
		['Time', attempt.tried_at ? new Date(attempt.tried_at).toISOString() : '—'],
		['Content type', attempt.content_type || '—'],
		['Content title', attempt.content_title || '—'],
		['Content ID', attempt.content_id || '—'],
		['Release title', attempt.release_title || '—'],
		['Served file', attempt.served_file || '—'],
		['Release size', formatSize(attempt.release_size)],
		['Result', formatAttemptResult(attempt)],
		['Failure reason', attempt.failure_reason || '—'],
		['Release URL', attempt.release_url || '—'],
		['Slot path', attempt.slot_path || '—'],
	].map(([label, value]) => `- ${label}: ${value}`)

	return [
		'Bad match report',
		'',
		'Why this is a bad match:',
		'- ',
		'',
		'Details:',
		...details,
	].join('\n')
}

export function NZBHistoryPage({ refreshTrigger }) {
  const [attempts, setAttempts] = useState([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState(null)
	const [copyError, setCopyError] = useState(null)
	const [copiedAttemptId, setCopiedAttemptId] = useState(null)

  const fetchAttempts = useCallback((showLoadingSpinner = true) => {
    if (showLoadingSpinner) setLoading(true)
    else setRefreshing(true)
    setError(null)
    const url = getApiUrl('/api/nzb-attempts?limit=200')
    apiFetch(url)
      .then((data) => {
        if (Array.isArray(data)) setAttempts(data)
      })
      .catch((err) => {
        setError(err.message || 'Failed to load NZB history')
      })
      .finally(() => {
        setLoading(false)
        setRefreshing(false)
      })
  }, [])

  useEffect(() => {
    fetchAttempts(true)
  }, [fetchAttempts])

  useEffect(() => {
    if (refreshTrigger == null || refreshTrigger === 0) return
    fetchAttempts(false)
  }, [refreshTrigger, fetchAttempts])

	const handleCopyBadMatch = useCallback(async (attempt) => {
		if (!navigator?.clipboard?.writeText) {
			setCopyError('Clipboard access is unavailable in this browser.')
			return
		}
		try {
			await navigator.clipboard.writeText(buildBadMatchReport(attempt))
			setCopyError(null)
			setCopiedAttemptId(attempt.id)
			setTimeout(() => {
				setCopiedAttemptId((current) => (current === attempt.id ? null : current))
			}, 2000)
		} catch {
			setCopyError('Failed to copy bad match details.')
		}
	}, [])

  return (
    <div className={cn('flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6')}>
      <Card className="flex flex-col overflow-hidden flex-1 min-h-0">
        <CardHeader>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1 max-w-[38rem] space-y-0.5">
              <CardTitle className="flex items-center gap-2">
                <History className="size-5" />
                NZB play attempts
              </CardTitle>
              <CardDescription>
						Recent attempts to play releases: title requested, release tried, and whether it succeeded or failed. Use the copy action to grab a report for Discord or GitHub.
              </CardDescription>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => fetchAttempts(false)}
              disabled={refreshing || loading}
              className="shrink-0"
            >
              {refreshing ? (
                <Loader2 className="size-4 animate-spin" />
              ) : (
                <RefreshCw className="size-4" />
              )}
              Refresh
            </Button>
          </div>
        </CardHeader>
        <CardContent className="flex flex-1 min-h-0 flex-col overflow-hidden">
          {loading && (
            <div className="flex items-center justify-center gap-2 py-12 text-muted-foreground">
              <Loader2 className="size-5 animate-spin" />
              Loading…
            </div>
          )}
          {error && (
            <div className="px-6 pb-4 text-destructive">{error}</div>
          )}
			{copyError && !error && (
			  <div className="px-6 pb-4 text-destructive">{copyError}</div>
          )}
          {!loading && !error && (
            attempts.length === 0 ? (
              <div className="flex flex-1 min-h-[360px]">
                <div className="flex w-full flex-1 items-center justify-center rounded-lg border border-dashed px-6 py-8 text-center text-sm text-muted-foreground">
                  No NZB attempts recorded yet. Play something from Stremio to see history here.
                </div>
              </div>
            ) : (
              <div className="flex flex-1 min-h-0 overflow-hidden rounded-lg border border-border/60 bg-muted/30">
                <ScrollArea className="flex-1 min-h-[360px]">
                  <div className="p-4">
                    <table className="w-full text-sm border-collapse">
                      <thead>
                        <tr className="border-b border-border">
                          <th className="text-left py-2 font-medium">Time</th>
                          <th className="text-left py-2 font-medium">Content</th>
                          <th className="text-left py-2 font-medium">Release</th>
                          <th className="text-left py-2 font-medium">Served file</th>
                          <th className="text-right py-2 font-medium">Size</th>
                          <th className="text-center py-2 font-medium">Result</th>
                          <th className="text-left py-2 font-medium">Reason</th>
                          <th className="text-center py-2 font-medium">Actions</th>
                        </tr>
                      </thead>
                      <tbody>
                        {attempts.map((a) => (
                          <tr key={a.id} className="border-b border-border/60 hover:bg-muted/50">
                            <td className="py-2 text-muted-foreground whitespace-nowrap">
                              {new Date(a.tried_at).toLocaleString()}
                            </td>
                            <td className="py-2 max-w-[140px] truncate" title={a.content_title || a.content_id}>
                              {a.content_title || a.content_id || '—'}
                            </td>
                            <td className="py-2 max-w-[220px] truncate" title={a.release_title}>
                              {a.release_title}
                            </td>
                            <td className="py-2 max-w-[220px] truncate text-muted-foreground" title={a.served_file}>
                              {a.served_file || '—'}
                            </td>
                            <td className="py-2 text-right text-muted-foreground">
                              {formatSize(a.release_size)}
                            </td>
                            <td className="py-2 text-center">
                              {a.preload ? (
                                <Badge variant="secondary" className="font-normal">Preload</Badge>
                              ) : a.success ? (
                                <Badge variant="default" className="bg-green-600 hover:bg-green-600">OK</Badge>
                              ) : (
                                <Badge variant="destructive">Failed</Badge>
                              )}
                            </td>
                            <td className="py-2 max-w-[200px] truncate text-muted-foreground" title={a.failure_reason}>
                              {a.failure_reason || '—'}
                            </td>
                            <td className="py-2 text-center">
                              <div className="flex items-center justify-center gap-1">
                                <Button
                                  type="button"
                                  variant="ghost"
                                  size="sm"
                                  onClick={() => handleCopyBadMatch(a)}
                                  className="h-8 w-8 p-0"
                                  aria-label={copiedAttemptId === a.id ? 'Bad match report copied' : 'Copy bad match report'}
                                  title={copiedAttemptId === a.id ? 'Bad match report copied' : 'Copy bad match report'}
                                >
                                  {copiedAttemptId === a.id ? <Check className="size-4" /> : <Copy className="size-4" />}
                                </Button>
                                {a.release_url ? (
                                  <a
                                    href={a.release_url}
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-primary"
                                    title="Open release details"
                                  >
                                    <ExternalLink className="size-4" />
                                  </a>
                                ) : null}
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </ScrollArea>
              </div>
            )
          )}
        </CardContent>
      </Card>
    </div>
  )
}
