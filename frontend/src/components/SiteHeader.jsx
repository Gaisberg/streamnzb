import { SidebarTrigger } from "@/components/ui/sidebar"

const pageTitles = {
  dashboard: "Dashboard",
  statistics: "Statistics",
  "nzb-history": "History",
  "direct-play": "Direct Play",
  logs: "Logs",
  "settings-general": "General Settings",
  "settings-indexers": "Indexers",
  "settings-providers": "Providers",
  filters: "Filters",
  metadata: "Metadata",
  formatting: "Formatting",
  "settings-search": "Search Settings",
  install: "Streams",
  streams: "Streams",
  "settings-advanced": "Advanced Settings",
  profile: "Profile",
  settings: "Settings",
}


export function SiteHeader({ activePage }) {
  return (
    <header className="flex h-14 shrink-0 items-center bg-background/80 backdrop-blur-sm transition-[width,height] ease-linear">
      <div className="flex w-full items-center gap-3 px-4 lg:px-6">
        <SidebarTrigger className="-ml-1 text-muted-foreground hover:text-foreground transition-colors" />
        <div className="h-5 w-px bg-muted-foreground/20" />
        <h1 className="text-sm font-headline font-semibold tracking-tight text-foreground">
          {pageTitles[activePage] || "Dashboard"}
        </h1>
      </div>
    </header>
  )
}
