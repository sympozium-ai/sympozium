import { useState } from "react";
import { Outlet } from "react-router-dom";
import { AppSidebar } from "./sidebar";
import { Header } from "./header";
import { ClusterChangeBanner } from "@/components/cluster-identity";
import { FeedPane } from "@/components/feed-pane";
import { useRunNotifications } from "@/hooks/use-run-notifications";

export function Layout() {
  const [feedOpen, setFeedOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  useRunNotifications();

  return (
    <div className="flex h-screen overflow-hidden">
      <AppSidebar
        collapsed={sidebarCollapsed}
        onToggle={() => setSidebarCollapsed((v) => !v)}
      />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header />
        <ClusterChangeBanner />
        <main className="flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
      <FeedPane open={feedOpen} onToggle={() => setFeedOpen((v) => !v)} />
    </div>
  );
}
