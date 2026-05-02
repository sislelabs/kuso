"use client";

import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useSession, sessionQueryKey } from "@/features/auth";
import {
  changePassword,
  updateProfile,
  type UpdateProfileBody,
} from "@/features/profile/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { Save, KeyRound } from "lucide-react";

export default function ProfilePage() {
  const qc = useQueryClient();
  const { data, refetch } = useSession();
  const [firstName, setFirstName] = useState("");
  const [lastName, setLastName] = useState("");
  const [email, setEmail] = useState("");
  const [savingProfile, setSavingProfile] = useState(false);

  const [pwOld, setPwOld] = useState("");
  const [pwNew, setPwNew] = useState("");
  const [pwNew2, setPwNew2] = useState("");
  const [savingPw, setSavingPw] = useState(false);

  useEffect(() => {
    if (data?.user) {
      const parts = data.user.name.split(" ");
      setFirstName(parts[0] ?? "");
      setLastName(parts.slice(1).join(" "));
      setEmail(data.user.email);
    }
  }, [data]);

  const onSaveProfile = async (e: React.FormEvent) => {
    e.preventDefault();
    setSavingProfile(true);
    try {
      const body: UpdateProfileBody = { firstName, lastName, email };
      await updateProfile(body);
      await qc.invalidateQueries({ queryKey: sessionQueryKey });
      await refetch();
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

  return (
    <div className="mx-auto max-w-xl p-6 lg:p-8 space-y-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Profile</h1>
      </div>

      <form onSubmit={onSaveProfile}>
        <Card>
          <CardHeader>
            <CardTitle>Identity</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="firstName">First name</Label>
                <Input id="firstName" value={firstName} onChange={(e) => setFirstName(e.target.value)} />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="lastName">Last name</Label>
                <Input id="lastName" value={lastName} onChange={(e) => setLastName(e.target.value)} />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="email">Email</Label>
              <Input id="email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
            </div>
            <div className="flex justify-end">
              <Button type="submit" disabled={savingProfile}>
                <Save className="h-4 w-4" />
                {savingProfile ? "Saving…" : "Save"}
              </Button>
            </div>
          </CardContent>
        </Card>
      </form>

      <form onSubmit={onChangePassword}>
        <Card>
          <CardHeader>
            <CardTitle>Password</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="pwOld">Current</Label>
              <Input id="pwOld" type="password" value={pwOld} onChange={(e) => setPwOld(e.target.value)} required />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="pwNew">New</Label>
                <Input id="pwNew" type="password" value={pwNew} onChange={(e) => setPwNew(e.target.value)} required />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="pwNew2">Confirm</Label>
                <Input id="pwNew2" type="password" value={pwNew2} onChange={(e) => setPwNew2(e.target.value)} required />
              </div>
            </div>
            <div className="flex justify-end">
              <Button type="submit" disabled={savingPw}>
                <KeyRound className="h-4 w-4" />
                {savingPw ? "Changing…" : "Change password"}
              </Button>
            </div>
          </CardContent>
        </Card>
      </form>
    </div>
  );
}
