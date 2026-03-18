import { NavLink } from "react-router-dom";
import {
  LayoutDashboard,
  Server,
  Play,
  Shield,
  Wrench,
  Clock,
  Users,
  Github,
  Heart,
  Globe,
  Plug,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { ScrollArea } from "@/components/ui/scroll-area";

const navItems = [
  { to: "/dashboard", label: "Dashboard", icon: LayoutDashboard },
  { to: "/gateway", label: "Gateway", icon: Globe },
  { to: "/mcp-servers", label: "MCP Servers", icon: Plug },
  { to: "/instances", label: "Instances", icon: Server },
  { to: "/personas", label: "Persona Packs", icon: Users },
  { to: "/policies", label: "Policies", icon: Shield },
  { to: "/runs", label: "Runs", icon: Play },
  { to: "/schedules", label: "Schedules", icon: Clock },
  { to: "/skills", label: "Skills", icon: Wrench },
];

interface AppSidebarProps {
  collapsed: boolean;
  onToggle: () => void;
}

export function AppSidebar({ collapsed, onToggle }: AppSidebarProps) {
  return (
    <aside
      className={cn(
        "flex h-full flex-col border-r border-border/50 bg-card transition-[width] duration-200 ease-in-out",
        collapsed ? "w-14" : "w-60"
      )}
    >
      {/* Logo */}
      <div className={cn(
        "flex h-14 items-center border-b border-border/50 overflow-hidden",
        collapsed ? "justify-center px-0" : "-ml-5"
      )}>
        {collapsed ? (
          <img src="/icon.png" alt="Sympozium" className="h-8 w-8 shrink-0" />
        ) : (
          <img src="/logo.png" alt="Sympozium" className="h-50" />
        )}
      </div>

      {/* Navigation */}
      <ScrollArea className="flex-1 py-2">
        <nav className="flex flex-col gap-1 px-2">
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              title={collapsed ? item.label : undefined}
              className={({ isActive }) =>
                cn(
                  "flex items-center rounded-md text-sm font-medium transition-colors",
                  collapsed ? "justify-center px-0 py-2" : "gap-3 px-3 py-2",
                  isActive
                    ? "bg-blue-500/10 text-blue-400 border border-blue-500/20"
                    : "text-muted-foreground hover:bg-white/5 hover:text-foreground border border-transparent"
                )
              }
            >
              <item.icon className="h-4 w-4 shrink-0" />
              {!collapsed && item.label}
            </NavLink>
          ))}
        </nav>
      </ScrollArea>

      {/* Help & Contribute */}
      {!collapsed && (
        <div className="border-t border-border/50 px-4 py-3 space-y-2">
          <a
            href="https://github.com/sympozium-ai/sympozium"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
          >
            <Github className="h-3.5 w-3.5" />
            Star on GitHub
          </a>
          <a
            href="https://github.com/sympozium-ai/sympozium/blob/main/CONTRIBUTING.md"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
          >
            <Heart className="h-3.5 w-3.5" />
            Contribute
          </a>
          <p className="px-2 text-[10px] text-muted-foreground/60">
            Kubernetes-native AI agents
          </p>
        </div>
      )}

      {/* Collapse toggle */}
      <div className={cn("border-t border-border/50 py-2", collapsed ? "px-2" : "px-4")}>
        <button
          onClick={onToggle}
          className={cn(
            "flex items-center rounded-md py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors w-full",
            collapsed ? "justify-center px-0" : "gap-2 px-2"
          )}
          title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
        >
          {collapsed ? (
            <PanelLeftOpen className="h-4 w-4" />
          ) : (
            <>
              <PanelLeftClose className="h-4 w-4" />
              Collapse
            </>
          )}
        </button>
      </div>
    </aside>
  );
}
