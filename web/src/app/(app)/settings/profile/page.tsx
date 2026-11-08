"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { sessionQueryKey } from "@/features/auth";
import {
  changePassword,
  AVATAR_TYPES,
  getMyProfile,
  updateProfile,
  uploadAvatar,
  validateAvatarFile,
  type UpdateProfileBody,
} from "@/features/profile/api";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { ErrorBoundary } from "@/components/shared/ErrorBoundary";
import { SettingsPage, SettingsPageLoading } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { toast } from "sonner";
import { Save, KeyRound, ImageUp } from "lucide-react";

// ProfilePage is the user's account home: identity + password. Two
// stacked cards with a thin border, dense rows, and trailing action
// buttons — matches the canvas/overlay aesthetic. Avatar lives in the
// header so the page feels owned by the signed-in user rather than a
// generic form.
// Top-level export wraps the page in an ErrorBoundary so any
// synchronous render-throw (a downstream component blowing up on a
// schema mismatch, a base-ui edge case, etc.) shows our own card
// instead of bubbling up to the Next.js runtime overlay's "This page
// couldn't load" splash. The earlier in-page error guards only catch
// React Query failures — they don't catch render exceptions.
export default function ProfilePageWithBoundary() {
  return (
    <ErrorBoundary
      fallback={
        <SettingsPage title="Profile">
          <SettingsNotice
            tone="error"
            title="Something broke on the profile page"
            action={
              <Button size="sm" variant="outline" onClick={() => window.location.reload()}>
                Reload
              </Button>
            }
          >
            An unexpected error happened while rendering. Try reloading; if it keeps failing, file a bug.
          </SettingsNotice>
        </SettingsPage>
      }
    >
      <ProfilePage />
    </ErrorBoundary>
  );
}

function ProfilePage() {
  const qc = useQueryClient();
  // /api/users/profile is the source of truth for editable identity
  // fields. The session payload only carries username + perms; firstName
  // / lastName / email aren't on it, and an earlier version of this page
  // crashed because it tried to read data.user.name (which doesn't exist
  // on AuthSession) and split it. Fetching the proper UserProfile here
  // makes the form match what the PUT endpoint round-trips.
  const profile = useQuery({ queryKey: ["users", "me"], queryFn: getMyProfile });
  const [firstName, setFirstName] = useState("");
  const [lastName, setLastName] = useState("");
  const [email, setEmail] = useState("");
  const [savingProfile, setSavingProfile] = useState(false);

  const [pwOld, setPwOld] = useState("");
  const [pwNew, setPwNew] = useState("");
  const [pwNew2, setPwNew2] = useState("");
  const [savingPw, setSavingPw] = useState(false);
  const [uploadingAvatar, setUploadingAvatar] = useState(false);
  const avatarInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (profile.data) {
      setFirstName(profile.data.firstName ?? "");
      setLastName(profile.data.lastName ?? "");
      setEmail(profile.data.email ?? "");
    }
  }, [profile.data]);

  const onSaveProfile = async (e: React.FormEvent) => {
    e.preventDefault();
    setSavingProfile(true);
    try {
      const body: UpdateProfileBody = { firstName, lastName, email };
      await updateProfile(body);
      await Promise.all([
        qc.invalidateQueries({ queryKey: sessionQueryKey }),
        qc.invalidateQueries({ queryKey: ["users", "me"] }),
      ]);
      toast.success("Profile saved");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save profile");
    } finally {
      setSavingProfile(false);
    }
  };

  const onAvatarPicked = async (file: File | undefined) => {
    if (avatarInput.current) avatarInput.current.value = "";
    if (!file) return;
    const problem = validateAvatarFile(file);
    if (problem) {
      toast.error(problem);
      return;
    }
    setUploadingAvatar(true);
    try {
      await uploadAvatar(file);
      await Promise.all([
        qc.invalidateQueries({ queryKey: sessionQueryKey }),
        qc.invalidateQueries({ queryKey: ["users", "me"] }),
      ]);
      toast.success("Avatar updated");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to upload avatar");
    } finally {
      setUploadingAvatar(false);
    }
  };

  const onChangePassword = async (e: React.FormEvent) => {
    e.preventDefault();
    if (pwNew !== pwNew2) {
      toast.error("Passwords don't match");
      return;
    }
    if (pwNew.length < 8) {
      toast.error("Password must be ≥ 8 chars");
      return;
    }
    setSavingPw(true);
    try {
      await changePassword({ currentPassword: pwOld, newPassword: pwNew });
      toast.success("Password changed");
      setPwOld("");
      setPwNew("");
      setPwNew2("");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to change password");
    } finally {
      setSavingPw(false);
    }
  };

  // Loading + error states are handled explicitly so a non-2xx from
  // /api/users/profile (expired session, server hiccup, schema drift)
  // doesn't bubble up to the Next.js runtime overlay which renders as
  // a useless "This page couldn't load. Reload / Back" splash. With
  // an in-page state the user sees a real, contextual message.
  if (profile.isPending) {
    return <SettingsPageLoading title="Profile" rows={2} />;
  }
  if (profile.isError || !profile.data) {
    return (
      <SettingsPage title="Profile">
        <SettingsNotice
          tone="error"
          title="Couldn't load your profile"
          action={
            <Button size="sm" variant="outline" onClick={() => profile.refetch()}>
              Retry
            </Button>
          }
        >
          {profile.error instanceof Error
            ? profile.error.message
            : "The /api/users/profile request failed. Try reloading; if it keeps failing, sign out and back in."}
        </SettingsNotice>
      </SettingsPage>
    );
  }

  const user = profile.data;
  const fullName = [user?.firstName, user?.lastName].filter(Boolean).join(" ").trim();
  const displayName = fullName || user?.username || "Profile";
  const initial = (fullName?.[0] ?? user?.username?.[0] ?? user?.email?.[0] ?? "U").toUpperCase();

  return (
    <SettingsPage title="Profile" description="Your name, email, avatar and password.">
      <SettingsSection
        title="Avatar"
        description="Shown in the top bar and next to your actions in the activity log."
        footerHint="PNG, JPEG, GIF or WEBP, up to 1000 KiB."
      >
        <div className="flex items-center gap-4">
          <Avatar className="h-14 w-14 border border-[var(--border-subtle)]">
            {user?.image && <AvatarImage src={user.image} alt={displayName} />}
            <AvatarFallback className="bg-[var(--bg-tertiary)] text-lg font-medium">{initial}</AvatarFallback>
          </Avatar>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium text-[var(--text-primary)]">{displayName}</div>
            <div className="truncate text-sm text-[var(--text-secondary)]">
              {user?.username}
              {user?.email ? ` · ${user.email}` : ""}
            </div>
          </div>
          <input
            ref={avatarInput}
            type="file"
            accept={AVATAR_TYPES.join(",")}
            className="hidden"
            onChange={(e) => void onAvatarPicked(e.target.files?.[0])}
          />
          <Button
            size="sm"
            variant="outline"
            disabled={uploadingAvatar}
            onClick={() => avatarInput.current?.click()}
          >
            <ImageUp className="h-3.5 w-3.5" />
            {uploadingAvatar ? "Uploading…" : "Change"}
          </Button>
        </div>
      </SettingsSection>

      <form onSubmit={onSaveProfile}>
        <SettingsSection
          title="Identity"
          description="How you appear to the rest of your team."
          footer={
            <Button type="submit" size="sm" disabled={savingProfile}>
              <Save className="h-3.5 w-3.5" />
              {savingProfile ? "Saving…" : "Save"}
            </Button>
          }
        >
          <div className="grid gap-5 sm:grid-cols-2">
            <SettingsField label="First name" htmlFor="firstName">
              <Input id="firstName" value={firstName} onChange={(e) => setFirstName(e.target.value)} />
            </SettingsField>
            <SettingsField label="Last name" htmlFor="lastName">
              <Input id="lastName" value={lastName} onChange={(e) => setLastName(e.target.value)} />
            </SettingsField>
          </div>
          <SettingsField label="Email" htmlFor="email" hint="Used for notifications and invitations.">
            <Input id="email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          </SettingsField>
        </SettingsSection>
      </form>

      <form onSubmit={onChangePassword}>
        <SettingsSection
          title="Password"
          description="Applies to local sign-in. Accounts created through GitHub or OAuth sign in there instead."
          footerHint="At least 8 characters."
          footer={
            <Button type="submit" size="sm" disabled={savingPw}>
              <KeyRound className="h-3.5 w-3.5" />
              {savingPw ? "Changing…" : "Change password"}
            </Button>
          }
        >
          <SettingsField label="Current password" htmlFor="pwOld">
            <Input
              id="pwOld"
              type="password"
              autoComplete="current-password"
              value={pwOld}
              onChange={(e) => setPwOld(e.target.value)}
              required
            />
          </SettingsField>
          <div className="grid gap-5 sm:grid-cols-2">
            <SettingsField label="New password" htmlFor="pwNew">
              <Input
                id="pwNew"
                type="password"
                autoComplete="new-password"
                value={pwNew}
                onChange={(e) => setPwNew(e.target.value)}
                required
              />
            </SettingsField>
            <SettingsField label="Confirm new password" htmlFor="pwNew2">
              <Input
                id="pwNew2"
                type="password"
                autoComplete="new-password"
                value={pwNew2}
                onChange={(e) => setPwNew2(e.target.value)}
                required
              />
            </SettingsField>
          </div>
        </SettingsSection>
      </form>
    </SettingsPage>
  );
}
