"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { sessionQueryKey } from "@/features/auth";
import {
  changePassword,
  getMyProfile,
  updateProfile,
  type UpdateProfileBody,
} from "@/features/profile/api";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { ErrorBoundary } from "@/components/shared/ErrorBoundary";
import { toast } from "sonner";
import { Save, KeyRound } from "lucide-react";

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
        <div className="mx-auto max-w-2xl p-6 lg:p-8">
          <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-sm">
            <p className="font-medium text-[var(--text-primary)]">Something broke on the profile page</p>
            <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
              An unexpected error happened while rendering. Try reloading; if it keeps failing,
              file a bug.
            </p>
            <div className="mt-3">
              <Button size="sm" variant="outline" onClick={() => window.location.reload()}>
                Reload
              </Button>
            </div>
          </div>
        </div>
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
    return (
      <div className="mx-auto max-w-2xl p-6 text-sm text-[var(--text-tertiary)] lg:p-8">
        Loading…
      </div>
    );
  }
  if (profile.isError || !profile.data) {
    return (
      <div className="mx-auto max-w-2xl p-6 lg:p-8">
        <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 text-sm">
          <p className="font-medium text-[var(--text-primary)]">Couldn&apos;t load your profile</p>
          <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
            {profile.error instanceof Error
              ? profile.error.message
              : "The /api/users/profile request failed. Try reloading; if it keeps failing, sign out and back in."}
          </p>
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="outline" onClick={() => profile.refetch()}>
              Retry
            </Button>
          </div>
        </div>
      </div>
    );
  }

  const user = profile.data;
  const fullName = [user?.firstName, user?.lastName].filter(Boolean).join(" ").trim();
  const displayName = fullName || user?.username || "Profile";
  const initial = (fullName?.[0] ?? user?.username?.[0] ?? user?.email?.[0] ?? "U").toUpperCase();

  return (
    <div className="mx-auto max-w-2xl p-6 lg:p-8">
      <header className="mb-6 flex items-center gap-4">
        <Avatar className="h-12 w-12 border border-[var(--border-subtle)]">
          {user?.image && <AvatarImage src={user.image} alt={displayName} />}
          <AvatarFallback className="bg-[var(--bg-tertiary)] text-base font-medium">
            {initial}
          </AvatarFallback>
        </Avatar>
        <div className="min-w-0">
          <h1 className="font-heading text-xl font-semibold tracking-tight truncate">
            {displayName}
          </h1>
          <p className="font-mono text-[11px] text-[var(--text-tertiary)] truncate">
            {user?.email ?? ""}
          </p>
        </div>
      </header>

      <section className="mb-4 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Identity</h2>
          <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            local account
          </span>
        </div>
        <form onSubmit={onSaveProfile} className="px-4 py-3">
          <div className="grid grid-cols-2 gap-x-3 gap-y-3">
            <Field label="First name" htmlFor="firstName">
              <Input
                id="firstName"
                value={firstName}
                onChange={(e) => setFirstName(e.target.value)}
                className="h-8 text-[13px]"
              />
            </Field>
            <Field label="Last name" htmlFor="lastName">
              <Input
                id="lastName"
                value={lastName}
                onChange={(e) => setLastName(e.target.value)}
                className="h-8 text-[13px]"
              />
            </Field>
            <Field label="Email" htmlFor="email" colSpan={2}>
              <Input
                id="email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="h-8 text-[13px]"
              />
            </Field>
          </div>
          <div className="mt-3 flex justify-end">
            <Button type="submit" size="sm" disabled={savingProfile}>
              <Save className="h-3.5 w-3.5" />
              {savingProfile ? "Saving…" : "Save"}
            </Button>
          </div>
        </form>
      </section>

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Password</h2>
          <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            min 8 chars
          </span>
        </div>
        <form onSubmit={onChangePassword} className="px-4 py-3">
          <Field label="Current" htmlFor="pwOld">
            <Input
              id="pwOld"
              type="password"
              value={pwOld}
              onChange={(e) => setPwOld(e.target.value)}
              required
              className="h-8 text-[13px]"
            />
          </Field>
          <div className="mt-3 grid grid-cols-2 gap-3">
            <Field label="New" htmlFor="pwNew">
              <Input
                id="pwNew"
                type="password"
                value={pwNew}
                onChange={(e) => setPwNew(e.target.value)}
                required
                className="h-8 text-[13px]"
              />
            </Field>
            <Field label="Confirm" htmlFor="pwNew2">
              <Input
                id="pwNew2"
                type="password"
                value={pwNew2}
                onChange={(e) => setPwNew2(e.target.value)}
                required
                className="h-8 text-[13px]"
              />
            </Field>
          </div>
          <div className="mt-3 flex justify-end">
            <Button type="submit" size="sm" disabled={savingPw}>
              <KeyRound className="h-3.5 w-3.5" />
              {savingPw ? "Changing…" : "Change password"}
            </Button>
          </div>
        </form>
      </section>
    </div>
  );
}

function Field({
  label,
  htmlFor,
  colSpan,
  children,
}: {
  label: string;
  htmlFor: string;
  colSpan?: 2;
  children: React.ReactNode;
}) {
  return (
    <div className={colSpan === 2 ? "col-span-2 space-y-1" : "space-y-1"}>
      <label
        htmlFor={htmlFor}
        className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
      >
        {label}
      </label>
      {children}
    </div>
  );
}
