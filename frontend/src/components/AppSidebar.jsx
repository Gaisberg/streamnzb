import {
  LayoutDashboard, Settings, LogOut, Settings2, Server, Globe, Search,
  Sun, Moon, Monitor, Zap, FileText, Coffee, User, MoreVertical, History, ChartColumn, AlertTriangle, PlayCircle, SlidersHorizontal, Library
} from "lucide-react"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"

const navMain = [
  { id: "dashboard", title: "Dashboard", icon: LayoutDashboard },
  { id: "direct-play", title: "Direct Play", icon: PlayCircle },
  { id: "statistics", title: "Statistics", icon: ChartColumn },
  { id: "nzb-history", title: "History", icon: History },
  { id: "library", title: "Library", icon: Library },
  { id: "logs", title: "Logs", icon: FileText },
]

const navSettings = [
  { id: "settings-general", title: "General", icon: Settings2 },
  { id: "settings-indexers", title: "Indexers", icon: Server },
  { id: "settings-providers", title: "Providers", icon: Globe },
  { id: "filters", title: "Filters", icon: SlidersHorizontal },
  { id: "settings-search", title: "Search", icon: Search },
  { id: "install", title: "Streams", icon: Zap },
  { id: "settings-advanced", title: "Advanced", icon: Settings },
]

const DiscordIcon = (props) => (
  <svg role="img" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" fill="currentColor" {...props}>
    <path d="M20.317 4.37a19.791 19.791 0 0 0-4.885-1.515.074.074 0 0 0-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 0 0-5.487 0 12.64 12.64 0 0 0-.617-1.25.077.077 0 0 0-.079-.037A19.736 19.736 0 0 0 3.677 4.37a.07.07 0 0 0-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 0 0 .031.057 19.9 19.9 0 0 0 5.993 3.03.078.078 0 0 0 .084-.028 14.09 14.09 0 0 0 1.226-1.994.076.076 0 0 0-.041-.106 13.107 13.107 0 0 1-1.872-.892.077.077 0 0 1-.008-.128 10.2 10.2 0 0 0 .372-.292.074.074 0 0 1 .077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 0 1 .078.01c.12.098.246.198.373.292a.077.077 0 0 1-.006.127 12.299 12.299 0 0 1-1.873.892.077.077 0 0 0-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 0 0 .084.028 19.839 19.839 0 0 0 6.002-3.03.077.077 0 0 0 .032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 0 0-.031-.03zM8.02 15.33c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.418 2.157-2.418 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.956 2.418-2.157 2.418zm7.975 0c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.955-2.418 2.157-2.418 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.946 2.418-2.157 2.418z"/>
  </svg>
)

export function AppSidebar({
  activePage,
  onNavigate,
  version,
  currentUser,
  forcePasswordResetEnabled,
  onLogout,
  theme,
  onThemeChange,
  ...props
}) {
  const { setOpenMobile } = useSidebar()

  const handleNavigate = (page) => {
    onNavigate(page)
    setOpenMobile(false)
  }

  return (
    <Sidebar collapsible="offcanvas" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              size="lg"
              onClick={() => handleNavigate("dashboard")}
              className="h-auto py-3"
            >
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                <Zap className="size-6" />
              </div>
              <div className="grid min-w-0 flex-1 gap-0.5 text-left">
                <span className="truncate text-lg font-semibold leading-none">StreamNZB</span>
                {version && (
                  <span className="truncate text-xs text-muted-foreground leading-none">v{version}</span>
                )}
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        {/* Main nav */}
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {navMain.map((item) => (
                <SidebarMenuItem key={item.id}>
                  <SidebarMenuButton
                    isActive={activePage === item.id}
                    tooltip={item.title}
                    onClick={() => handleNavigate(item.id)}
                  >
                    <item.icon />
                    <span>{item.title}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        {/* Settings group */}
        <SidebarGroup>
          <SidebarGroupLabel>Settings</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {navSettings.map((item) => (
                <SidebarMenuItem key={item.id}>
                  <SidebarMenuButton
                    isActive={activePage === item.id}
                    tooltip={item.title}
                    onClick={() => handleNavigate(item.id)}
                  >
                    <item.icon />
                    <span>{item.title}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        {/* Theme selector - pushed to bottom */}
        <SidebarGroup className="mt-auto">
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <ToggleGroup
                  type="single"
                  value={theme}
                  onValueChange={(v) => v && onThemeChange(v)}
                  className="justify-center"
                >
                  <ToggleGroupItem value="light" size="sm" aria-label="Light">
                    <Sun className="size-4" />
                  </ToggleGroupItem>
                  <ToggleGroupItem value="dark" size="sm" aria-label="Dark">
                    <Moon className="size-4" />
                  </ToggleGroupItem>
                  <ToggleGroupItem value="system" size="sm" aria-label="System">
                    <Monitor className="size-4" />
                  </ToggleGroupItem>
                </ToggleGroup>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              tooltip="Discord"
              onClick={() => window.open('https://snzb.stream/discord', '_blank')}
            >
              <DiscordIcon className="size-4" />
              <span>Discord</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton
              tooltip="Buy me a coffee"
              onClick={() => window.open('https://buymeacoffee.com/gaisberg', '_blank')}
            >
              <Coffee className="size-4" />
              <span>Buy me a coffee</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
        {currentUser && currentUser !== 'legacy' && (
          <div className="mt-2 pt-2 border-t border-sidebar-border">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  className={cn(
                    "flex w-full items-center gap-3 rounded-lg p-2 text-left outline-none",
                    "hover:bg-sidebar-accent focus:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring"
                  )}
                  aria-label="Open account menu"
                >
                  <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground">
                    <Zap className="size-4" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <span className="truncate block text-sm font-medium flex items-center gap-1.5">
                      <span className="truncate">{currentUser}</span>
                      {forcePasswordResetEnabled && (
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span
                                className="inline-flex items-center text-amber-600"
                                aria-label="Forced password reset is enabled via environment variable"
                              >
                                <AlertTriangle className="size-3.5 shrink-0" />
                              </span>
                            </TooltipTrigger>
                            <TooltipContent side="top" align="start">
                              Forced password reset is enabled via env var. Disable it after the password is changed.
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      )}
                    </span>
                    <span className="truncate block text-xs text-muted-foreground">Account</span>
                  </div>
                  <MoreVertical className="size-4 shrink-0 text-muted-foreground" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent side="top" align="end" className="w-48">
                <DropdownMenuItem onClick={() => handleNavigate("profile")}>
                  <User className="mr-2 h-4 w-4" />
                  Profile
                </DropdownMenuItem>
                <DropdownMenuItem onClick={onLogout}>
                  <LogOut className="mr-2 h-4 w-4" />
                  Log out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        )}
      </SidebarFooter>
    </Sidebar>
  )
}
