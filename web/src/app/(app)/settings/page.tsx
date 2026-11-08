"use client";

import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsOverviewList } from "@/components/settings/SettingsNav";

export default function SettingsIndex() {
  return (
    <SettingsPage
      title="Settings"
      description="Everything that isn't scoped to a single project. Project settings live on each project's canvas."
    >
      <SettingsOverviewList />
    </SettingsPage>
  );
}
