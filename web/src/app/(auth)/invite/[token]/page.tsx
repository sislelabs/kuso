import { InviteRedeemView } from "./view";

// Static export: emit a placeholder and let the Go SPA fallback +
// the runtime router pick up any /invite/<token> path. The token is
// read client-side via useParams.
export function generateStaticParams() {
  return [{ token: "_" }];
}

export default function InvitePage() {
  return <InviteRedeemView />;
}
