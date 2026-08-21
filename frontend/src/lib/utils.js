import { clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs) {
    return twMerge(clsx(inputs))
}

export function formatBytes(bytes, decimals = 2) {
    if (!bytes || bytes === 0) return '0 B'
    const k = 1024
    const dm = decimals < 0 ? 0 : decimals
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i]}`
}

// streamSeriesKey namespaces a stream name used as a recharts dataKey, so a
// user-chosen name can never collide with the chart's own series names.
export function streamSeriesKey(name) {
    return `stream:${name}`
}

// selectClass styles a native <select> to match the Input primitive. Native
// selects are used wherever a Radix popover would be more machinery than the
// choice deserves; this keeps them looking like every other control.
export const selectClass = "flex h-9 w-full min-w-0 max-w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:opacity-60"
