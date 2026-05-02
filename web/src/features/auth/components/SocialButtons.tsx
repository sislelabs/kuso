"use client";

import { Github } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAuthMethods } from "../hooks";

export function SocialButtons() {
  const { data } = useAuthMethods();
  if (!data) return null;
  if (!data.github && !data.oauth2) return null;
  return (
    <div className="space-y-2">
      {data.github && (
        <Button
          variant="outline"
          className="w-full"
          onClick={() => {
            window.location.href = "/api/auth/github";
          }}
          type="button"
        >
          <Github className="h-4 w-4" />
          Continue with GitHub
        </Button>
      )}
      {data.oauth2 && (
        <Button
          variant="outline"
          className="w-full"
          onClick={() => {
            window.location.href = "/api/auth/oauth2";
          }}
          type="button"
        >
          Continue with SSO
        </Button>
      )}
    </div>
  );
}
