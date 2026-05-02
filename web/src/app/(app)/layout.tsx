import { AuthGate } from "@/features/auth/components/AuthGate";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  return <AuthGate>{children}</AuthGate>;
}
