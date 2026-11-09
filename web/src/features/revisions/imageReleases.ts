import type { Revision } from "./api";

export interface ImageRelease {
  revision: Revision;
  repository: string;
  tag: string;
  ref: string;
  // The release the service runs now: the newest one whose ref matches
  // the service's image.
  current: boolean;
  // Why the image changed: a rollback/revert from History, the image the
  // service was created with, or a tag someone set by hand.
  source: "rollback" | "create" | "edit";
  // Older releases folded into this row: a back-and-forth between the
  // same two tags collapses into its newest step.
  folded?: ImageRelease[];
}

function sourceOf(rev: Revision): ImageRelease["source"] {
  const s = rev.snapshot;
  if (s && typeof s === "object" && typeof (s as Record<string, unknown>).revertOf === "string") return "rollback";
  if (rev.summary === "create") return "create";
  return "edit";
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
    releases.push({ revision, repository: img.repository, tag: img.tag, ref, current, source: sourceOf(revision) });
  }
  return { releases, other };
}

// collapseFlipFlops folds a run of 3+ consecutive releases that only
// alternate between two refs (1.28 → 1.27 → 1.28 → …) into its newest
// row, so a few quick rollbacks don't bury the history. Input and output
// are newest first; a folded run keeps the current release on top.
export function collapseFlipFlops(releases: ImageRelease[]): ImageRelease[] {
  const out: ImageRelease[] = [];
  let i = 0;
  while (i < releases.length) {
    const head = releases[i];
    let j = i + 1;
    if (j < releases.length && releases[j].ref !== head.ref) {
      const other = releases[j].ref;
      j++;
      while (j < releases.length && releases[j].ref === (((j - i) % 2 === 0) ? head.ref : other)) j++;
    }
    const run = releases.slice(i, j);
    if (run.length >= 3) {
      out.push({ ...head, folded: run.slice(1) });
      i = j;
    } else {
      out.push(head);
      i++;
    }
  }
  return out;
}

// shortDigest turns a kubelet imageID ("ghcr.io/x/app@sha256:abc…") into
// "sha256:abc1234567ab"; "" when there's no digest in it.
export function shortDigest(imageID: string | undefined): string {
  const m = /sha256:([0-9a-f]{12})/.exec(imageID ?? "");
  return m ? `sha256:${m[1]}` : "";
}
