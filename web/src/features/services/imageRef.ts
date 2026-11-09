// parseImageRef splits a pasted image reference ("ghcr.io/acme/api:1.4",
// "nginx", "localhost:5000/app@sha256:…") into the repository and the
// tag/digest the create body takes. A registry port is not a tag.
export interface ImageRef {
  repository: string;
  // Tag, or "sha256:…" for a digest; "" when the reference has neither.
  tag: string;
  // Last path segment, for deriving names ("api").
  name: string;
}

const REPO_RE = /^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::\d+)?(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$/;

export function parseImageRef(raw: string): ImageRef | null {
  const ref = raw.trim().replace(/^docker:\/\//, "");
  if (!ref || /\s/.test(ref)) return null;
  let repository = ref;
  let tag = "";
  const at = ref.indexOf("@");
  if (at >= 0) {
    repository = ref.slice(0, at);
    tag = ref.slice(at + 1);
    if (!/^sha256:[a-f0-9]{64}$/.test(tag)) return null;
  } else {
    const colon = ref.lastIndexOf(":");
    if (colon > ref.lastIndexOf("/")) {
      repository = ref.slice(0, colon);
      tag = ref.slice(colon + 1);
      if (!/^[\w][\w.-]{0,127}$/.test(tag)) return null;
    }
  }
  if (!REPO_RE.test(repository)) return null;
  return { repository, tag, name: repository.split("/").pop() ?? repository };
}
