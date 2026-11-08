// Audit rows are written for operators reading the database: dotted
// action ids, raw user ids, CR names in parentheses. These helpers turn
// them into what a person reads in the UI; the stripped internals stay
// available as "details".

const ACTION_LABELS: Record<string, string> = {
  login: "Signed in",
  deploy: "Deployed",
  "deploy.rolled": "Rolled out",
  migrate: "Ran a migration",
  "addon.backup_download": "Downloaded a backup",
  "addon.backup_run": "Started a backup",
  "addon.create": "Created a database",
  "addon.delete": "Deleted a database",
  "addon.portforward": "Opened a port-forward",
  "addon.public_tcp_disable": "Closed the public port",
  "addon.public_tcp_enable": "Opened a public port",
  "addon.restore": "Restored a backup",
  "addon.secret_read": "Revealed database credentials",
  "addon.sql_query": "Ran a SQL query",
  "addon.sql_write": "Ran a SQL write",
  "backup.settings": "Changed backup settings",
  "build.retry_release": "Retried the release step",
  "build.rollback": "Rolled back",
  "controlplane.restore": "Restored the control plane",
  "cron.delete": "Deleted a cron job",
  "deployhook.disable": "Disabled the deploy hook",
  "deployhook.enable": "Enabled the deploy hook",
  "deployhook.rotate": "Rotated the deploy hook",
  "envgroup.delete": "Deleted an environment group",
  "environment.delete": "Deleted an environment",
  "environment.domains": "Changed domains",
  "group.delete": "Deleted a group",
  "group.instance-role.update": "Changed a group's role",
  "group.member.remove": "Removed a group member",
  "incident.approve": "Approved an incident fix",
  "incident.feedback": "Left incident feedback",
  "incident.reject": "Rejected an incident fix",
  "incident.resolve": "Resolved an incident",
  "instancepg.disable": "Disabled the shared Postgres",
  "node.bootstrap.joined": "Joined a node",
  "node.bootstrap.mint": "Created a node join token",
  "node.bootstrap.revoke": "Revoked a node join token",
  "node.cleanup": "Cleaned up a node",
  "node.patch": "Changed a node",
  "node.remove": "Removed a node",
  "node.uncordon": "Uncordoned a node",
  "node.updates-applied": "Applied node updates",
  "orphan.delete": "Deleted an orphaned resource",
  "project.delete": "Deleted a project",
  "project.export": "Exported a project",
  "project.grant.add": "Granted project access",
  "project.grant.remove": "Removed project access",
  "project.start": "Started a project",
  "project.stop": "Stopped a project",
  "registry.login": "Signed in to a registry",
  "registry.logout": "Signed out of a registry",
  "role.create": "Created a role",
  "role.delete": "Deleted a role",
  "role.update": "Changed a role",
  "run.cancel": "Cancelled a run",
  "run.create": "Started a run",
  "run.delete": "Deleted a run",
  "secret.set": "Set a secret",
  "secret.unset": "Removed a secret",
  "service.delete": "Deleted a service",
  "service.restart": "Restarted",
  "service.shell": "Opened a shell",
  "service.start": "Started",
  "service.stop": "Stopped",
  "service.terminal": "Opened a terminal",
  "system.update": "Updated kuso",
  "user.deactivate": "Deactivated a user",
  "user.delete": "Deleted a user",
  "user.instance-role.update": "Changed a user's role",
  "user.password.change": "Changed a user's password",
  "user.password.change.self": "Changed their password",
  "user.role.change": "Changed a user's role",
  "user.update": "Updated a user",
};

// auditActionLabel names an action for people. Unknown ids still read as
// words ("foo.bar_baz" → "Foo bar baz") rather than as an identifier.
export function auditActionLabel(action: string | undefined): string {
  const a = (action ?? "").trim();
  if (!a) return "Event";
  const known = ACTION_LABELS[a];
  if (known) return known;
  const words = a.replace(/[._-]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export interface AuditMessage {
  text: string;
  details: string[];
}

const HEX_ID = /\b[0-9a-f]{16,}\b/;
// Parentheticals that only matter to operators: key=value pairs or a
// kube object name.
const INTERNAL_PAREN = /\s*\(((?:env CR|deployment|container|pod)\b[^()]*|[^()]*=[^()]*)\)/g;

// humanizeAuditMessage rewrites a stored audit message for display:
// user ids become names, internal parentheticals and job/hash tags move
// to details, and messages that only repeat the action id are dropped.
export function humanizeAuditMessage(
  action: string | undefined,
  message: string | undefined,
  nameOf: (id: string) => string | null,
): AuditMessage {
  let text = (message ?? "").trim();
  const details: string[] = [];
  if (!text) return { text, details };

  // Some writers just echo the action ("deployhook.enable for a/b").
  if (action && text.startsWith(`${action} `)) text = text.slice(action.length).replace(/^\s*for\s+/, "");

  text = text.replace(/^\[(sha256:[0-9a-f]+)\]\s*/, (_m, h: string) => {
    details.push(h);
    return "";
  });
  text = text.replace(INTERNAL_PAREN, (_m, inner: string) => {
    details.push(inner.trim());
    return "";
  });
  text = text.replace(/\s+(job=\S+)/g, (_m, tag: string) => {
    details.push(tag);
    return "";
  });
  text = text.replace(/\buser id=([0-9a-f]+)/g, (_m, id: string) => {
    const name = nameOf(id);
    if (!name) details.push(`user id=${id}`);
    return name ?? `user ${id.slice(0, 8)}…`;
  });
  text = text.replace(/\bkey="([^"]*)"/g, "$1").replace(/\s+env=""/g, "");
  text = text.replace(/\benv="([^"]+)"/g, "in $1");

  return { text: text.replace(/\s{2,}/g, " ").trim(), details };
}

// looksLikeUserId reports whether an actor value is an opaque id rather
// than a name, so callers can avoid printing it.
export function looksLikeUserId(id: string): boolean {
  return HEX_ID.test(id);
}
