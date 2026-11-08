"use client";

import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsOverviewList } from "@/components/settings/SettingsNav";
import { SettingsOverview } from "@/components/settings/SettingsOverview";
import { useCan, Perms } from "@/features/auth";

export default function SettingsIndex() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const canUsers = useCan(Perms.UserWrite);
  const hasStatus = isAdmin || canUsers;
  return (
    <SettingsPage
      title="Settings"
      description="Everything that isn't scoped to a single project. Project settings live on each project's canvas."
    >
      <SettingsOverview />
      {/* On desktop the sidebar is the navigation; repeating it here only
          made sense when there was nothing else to show. */}
      <div className={hasStatus ? "lg:hidden" : undefined}>
        <SettingsOverviewList />
      </div>
    </SettingsPage>
  );
}
