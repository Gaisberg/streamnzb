import { AlertTriangle } from "lucide-react"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"

export function EnvOverrideIndicator({ show, message = 'Overwritten by environment variable on restart.' }) {
  if (!show) return null
  return (
    <TooltipProvider delayDuration={100}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button type="button" className="inline-flex items-center text-amber-600 hover:text-amber-700 align-middle" aria-label={message}>
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" align="start">{message}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
