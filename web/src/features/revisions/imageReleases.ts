import type { Revision } from "./api";

export interface ImageRelease {
  revision: Revision;
  repository: string;
  tag: string;
  ref: string;
  // The release the service runs now: the newest one whose ref matches
  // the service's image.
  current: boolean;
}

function patchImage(snapshot: unknown): { repository: string; tag: string } | null {
  if (!snapshot || typeof snapshot !== "object") return null;
  const patch = (snapshot as Record<string, unknown>).patch;
  if (!patch || typeof patch !== "object") return null;
  const image = (patch as Record<string, unknown>).image;
  if (!image || typeof image !== "object") return null;
  const { repository, tag } = image as Record<string, unknown>;
  if (typeof repository !== "string" || !repository.trim()) return null;
  return { repository: repository.trim(), tag: typeof tag === "string" && tag.trim() ? tag.trim() : "latest" };
}

export function imageRef(repository: string, tag: string | undefined): string {
  return `${repository}:${tag || "latest"}`;
}

// imageReleases picks the revisions that set a runtime=image service's
// image (each saved tag, plus the create-time one on newer servers),
// newest first. Everything else stays a plain config change.
export function imageReleases(
  revisions: Revision[],
  currentImage: { repository?: string; tag?: string } | undefined,
): { releases: ImageRelease[]; other: Revision[] } {
  const currentRef = currentImage?.repository ? imageRef(currentImage.repository, currentImage.tag) : "";
  const sorted = [...revisions].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
  const releases: ImageRelease[] = [];
  const other: Revision[] = [];
  let currentFound = false;
  for (const revision of sorted) {
    const img = patchImage(revision.snapshot);
    if (!img) {
      other.push(revision);
      continue;
    }
    const ref = imageRef(img.repository, img.tag);
    const current = !currentFound && ref === currentRef;
    if (current) currentFound = true;
    releases.push({ revision, repository: img.repository, tag: img.tag, ref, current });
  }
  return { releases, other };
}

// shortDigest turns a kubelet imageID ("ghcr.io/x/app@sha256:abc…") into
// "sha256:abc1234567ab"; "" when there's no digest in it.
export function shortDigest(imageID: string | undefined): string {
  const m = /sha256:([0-9a-f]{12})/.exec(imageID ?? "");
  return m ? `sha256:${m[1]}` : "";
}
