import { Routes, Route, Navigate } from "react-router-dom";
import { useAuth } from "@/components/auth-provider";
import { Layout } from "@/components/layout/layout";
import { LoginPage } from "@/pages/login";
import { DashboardPage } from "@/pages/dashboard";
import { InstancesPage } from "@/pages/instances";
import { InstanceDetailPage } from "@/pages/instance-detail";
import { RunsPage } from "@/pages/runs";
import { RunDetailPage } from "@/pages/run-detail";
import { PoliciesPage } from "@/pages/policies";
import { SkillsPage } from "@/pages/skills";
import { SchedulesPage } from "@/pages/schedules";
import { PersonasPage } from "@/pages/personas";
import { PersonaDetailPage } from "@/pages/persona-detail";
import { GatewayPage } from "@/pages/gateway";
import { McpServersPage } from "@/pages/mcp-servers";
import { McpServerDetailPage } from "@/pages/mcp-server-detail";
import { SettingsPage } from "@/pages/settings";

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuth();
  if (!isAuthenticated) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

export default function App() {
  const { isAuthenticated } = useAuth();

  if (!isAuthenticated) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  return (
    <Routes>
      <Route
        element={
          <ProtectedRoute>
            <Layout />
          </ProtectedRoute>
        }
      >
        <Route path="/" element={<Navigate to="/dashboard" replace />} />
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/instances" element={<InstancesPage />} />
        <Route path="/instances/:name" element={<InstanceDetailPage />} />
        <Route path="/runs" element={<RunsPage />} />
        <Route path="/runs/:name" element={<RunDetailPage />} />
        <Route path="/policies" element={<PoliciesPage />} />
        <Route path="/skills" element={<SkillsPage />} />
        <Route path="/schedules" element={<SchedulesPage />} />
        <Route path="/personas" element={<PersonasPage />} />
        <Route path="/personas/:name" element={<PersonaDetailPage />} />
        <Route path="/mcp-servers" element={<McpServersPage />} />
        <Route path="/mcp-servers/:name" element={<McpServerDetailPage />} />
        <Route path="/gateway" element={<GatewayPage />} />
        <Route path="/settings" element={<SettingsPage />} />
      </Route>
      <Route path="/login" element={<LoginPage />} />
      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
