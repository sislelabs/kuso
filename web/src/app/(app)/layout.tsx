import { AuthGate } from "@/features/auth/components/AuthGate";
import { DashboardShell } from "@/components/layout/DashboardShell";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  return (
    <AuthGate>
      <DashboardShell>{children}</DashboardShell>
    </AuthGate>
  );
}
