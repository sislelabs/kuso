// parseImageRef splits a pasted image reference ("ghcr.io/acme/api:1.4",
// "nginx", "localhost:5000/app@sha256:…") into the repository and the
// tag/digest the create body takes. A registry port is not a tag.
export interface ImageRef {
  repository: string;
  // The Tag field's value: "1.4", "sha256:…" for a digest, "1.4@sha256:…"
  // for both; "" when the reference has neither.
  tag: string;
  // Last path segment, for deriving names ("api").
  name: string;
}

const REPO_RE = /^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::\d+)?(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$/;
const TAG_RE = /^[\w][\w.-]{0,127}$/;
const DIGEST_RE = /^sha256:[a-f0-9]{64}$/;

export function parseImageRef(raw: string): ImageRef | null {
  const ref = raw.trim().replace(/^docker:\/\//, "");
  if (!ref || /\s/.test(ref)) return null;
  let rest = ref;
  let digest = "";
  const at = ref.indexOf("@");
  if (at >= 0) {
    rest = ref.slice(0, at);
    digest = ref.slice(at + 1);
    if (!DIGEST_RE.test(digest)) return null;
  }
  let repository = rest;
  let tag = "";
  const colon = rest.lastIndexOf(":");
  if (colon > rest.lastIndexOf("/")) {
    repository = rest.slice(0, colon);
    tag = rest.slice(colon + 1);
    if (!TAG_RE.test(tag)) return null;
  }
  if (!REPO_RE.test(repository)) return null;
  const tagField = digest ? (tag ? `${tag}@${digest}` : digest) : tag;
  return { repository, tag: tagField, name: repository.split("/").pop() ?? repository };
}

// splitPastedImage returns the repository/tag pair when a full reference
// was typed or pasted into a repository-only field, so the form can move
// the tag into its own field. Null when there's nothing to split.
export function splitPastedImage(raw: string): { repository: string; tag: string } | null {
  const ref = parseImageRef(raw);
  if (!ref || !ref.tag) return null;
  return { repository: ref.repository, tag: ref.tag };
}

export function imageRepositoryProblem(repository: string): string | null {
  const v = repository.trim();
  if (!v) return "Enter the image repository, e.g. ghcr.io/acme/api.";
  if (/[A-Z]/.test(v)) return "Image names are lowercase.";
  if (!REPO_RE.test(v.replace(/^docker:\/\//, ""))) return "That isn't a repository a registry would take. Use registry/owner/name.";
  return null;
}

// imageTagProblem validates the Tag field: a tag, a digest, or tag@digest.
// It's required so a deploy never silently means "latest".
export function imageTagProblem(tag: string): string | null {
  const v = tag.trim();
  if (!v) return "Enter a tag or digest, e.g. 1.4.2. Use latest only if you mean it.";
  const at = v.indexOf("@");
  const t = at >= 0 ? v.slice(0, at) : v;
  const d = at >= 0 ? v.slice(at + 1) : "";
  if (at < 0 && v.startsWith("sha256:")) return DIGEST_RE.test(v) ? null : "A digest is sha256: followed by 64 hex characters.";
  if (!TAG_RE.test(t)) return "Tags use letters, digits, . _ and -, up to 128 characters.";
  if (at >= 0 && !DIGEST_RE.test(d)) return "A digest is sha256: followed by 64 hex characters.";
  return null;
}

// specImageTag maps the Tag field to the value the chart renders as
// "<repository>:<tag>". A bare digest has no tag to sit after the colon, so
// it rides on "latest@…": with a digest present the runtime pulls by digest.
export function specImageTag(tag: string): string {
  const v = tag.trim();
  return v.startsWith("sha256:") ? `latest@${v}` : v;
}

// displayImageTag is specImageTag's inverse for showing a stored tag.
export function displayImageTag(tag: string): string {
  return tag.startsWith("latest@sha256:") ? tag.slice("latest@".length) : tag;
}

// imageRefString renders repository + Tag field as one pullable reference.
export function imageRefString(repository: string, tag: string): string {
  const v = tag.trim();
  if (!v) return repository;
  return v.startsWith("sha256:") ? `${repository}@${v}` : `${repository}:${v}`;
}
