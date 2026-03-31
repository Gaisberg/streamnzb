import { useEffect, useRef } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Button } from '@/components/ui/button'
import { Download, FileText } from "lucide-react"
import { getApiUrl } from '../api'
import { cn } from "@/lib/utils"

export function LogsPage({ logs = [] }) {
	const scrollAreaRef = useRef(null)
	const stickToBottomRef = useRef(true)

	const handleDownloadLogs = () => {
		const link = document.createElement('a')
		link.href = getApiUrl('/api/logs/download')
		link.download = 'streamnzb.log'
		document.body.appendChild(link)
		link.click()
		link.remove()
	}

	useEffect(() => {
		const viewport = scrollAreaRef.current?.querySelector('[data-radix-scroll-area-viewport]')
		if (!viewport) return

		const updateStickToBottom = () => {
			const distanceFromBottom = viewport.scrollHeight - viewport.clientHeight - viewport.scrollTop
			stickToBottomRef.current = distanceFromBottom <= 48
		}

		updateStickToBottom()
		viewport.addEventListener('scroll', updateStickToBottom)
		return () => viewport.removeEventListener('scroll', updateStickToBottom)
	}, [])

	useEffect(() => {
		const viewport = scrollAreaRef.current?.querySelector('[data-radix-scroll-area-viewport]')
		if (!viewport || !stickToBottomRef.current) return
		viewport.scrollTop = viewport.scrollHeight
	}, [logs.length])

  return (
	  <div className={cn("flex flex-1 min-h-0 flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6")}>
      <Card className="flex flex-col overflow-hidden flex-1 min-h-0">
        <CardHeader>
			  <div className="flex items-start justify-between gap-4">
				<div className="min-w-0 flex-1 max-w-[34rem] space-y-0.5">
				  <CardTitle className="flex items-center gap-2">
					<FileText className="size-5" />
					Logs
				  </CardTitle>
				  <CardDescription>
					Live recent logs are shown below. Download the current log file when reporting issues.
				  </CardDescription>
				</div>
				<Button type="button" variant="outline" size="sm" onClick={handleDownloadLogs} className="shrink-0">
				  <Download className="size-4" />
				  Download logs
				</Button>
			  </div>
        </CardHeader>
	        <CardContent className="flex flex-1 min-h-0 flex-col overflow-hidden">
	          <div className="flex flex-1 min-h-0 overflow-hidden rounded-lg border border-border/60 bg-muted/30">
	            <ScrollArea ref={scrollAreaRef} className="flex-1 min-h-0 px-4 py-4">
            <div className="font-mono text-xs space-y-1 pr-4">
              {logs.length === 0 && (
                <div className="text-muted-foreground italic py-4">Waiting for logs...</div>
              )}
              {logs.map((log, i) => (
                <div
                  key={i}
                  className={cn(
                    "whitespace-pre-wrap break-all border-b border-border/40 pb-0.5 mb-0.5 last:border-0",
                    "text-foreground"
                  )}
                >
                  {log}
                </div>
              ))}
            </div>
          </ScrollArea>
            </div>
        </CardContent>
      </Card>
    </div>
  )
}
