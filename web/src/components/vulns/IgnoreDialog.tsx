"use client";

import { useId, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { MAX_IGNORE_DAYS, parseExpiryDays, useAddVulnIgnore, type VulnFinding, type VulnOwner } from "@/features/vulns";

export function IgnoreDialog({
  owner,
  finding,
  onClose,
}: {
  owner: VulnOwner;
  // null = closed.
  finding: VulnFinding | null;
  onClose: () => void;
}) {
  return (
    <Dialog open={!!finding} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        {finding && <IgnoreForm key={finding.vulnId} owner={owner} finding={finding} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  );
}

function IgnoreForm({ owner, finding, onClose }: { owner: VulnOwner; finding: VulnFinding; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const [expiry, setExpiry] = useState("");
  const [touched, setTouched] = useState(false);
  const add = useAddVulnIgnore(owner);
  const reasonId = useId();
  const expiryId = useId();

  const expiryParse = parseExpiryDays(expiry);
  const reasonError = reason.trim() === "" ? "A reason is required." : undefined;
  const expiryError = expiryParse.ok ? undefined : expiryParse.error;
  const invalid = !!reasonError || !!expiryError;

  const submit = async () => {
    setTouched(true);
    if (invalid || !expiryParse.ok) return;
    try {
      await add.mutateAsync({ vulnId: finding.vulnId, reason: reason.trim(), expiresInDays: expiryParse.days });
      toast.success(`Ignoring ${finding.vulnId}`);
      onClose();
    } catch {
      // Global mutation toast reports the server message.
    }
  };

  return (
    <form
      className="grid gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <DialogHeader>
        <DialogTitle>Ignore {finding.vulnId}</DialogTitle>
        <DialogDescription>
          Ignored findings stay visible but no longer block deploys or trigger alerts. Applies to every package with
          this ID in this {owner.kind}.
        </DialogDescription>
      </DialogHeader>
      <div className="grid gap-1.5">
        <Label htmlFor={reasonId}>Reason</Label>
        <Input
          id={reasonId}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="e.g. not reachable: we never parse untrusted XML"
          aria-invalid={touched && !!reasonError}
          autoFocus
        />
        {touched && reasonError && <p className="text-xs text-[var(--error)]">{reasonError}</p>}
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor={expiryId}>Expires after (days, optional)</Label>
        <Input
          id={expiryId}
          inputMode="numeric"
          value={expiry}
          onChange={(e) => setExpiry(e.target.value)}
          placeholder={`blank = never, max ${MAX_IGNORE_DAYS}`}
          aria-invalid={!!expiryError}
          className="w-48"
        />
        {expiryError && <p className="text-xs text-[var(--error)]">{expiryError}</p>}
      </div>
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" disabled={add.isPending || (touched && invalid)}>
          {add.isPending ? "Saving…" : "Ignore"}
        </Button>
      </DialogFooter>
    </form>
  );
}
