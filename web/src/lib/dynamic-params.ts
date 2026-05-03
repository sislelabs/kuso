"use client";

import { useParams, usePathname } from "next/navigation";
import { useEffect, useState } from "react";

// useRouteParams reads dynamic route params from the URL pathname
// instead of trusting Next's useParams() output. Static export with
// generateStaticParams emits the placeholder "_" for unknown segments,
// so a build-time render of /projects/[project] sees { project: "_" }
// and triggers a 404 against /api/projects/_ before client-side
// hydration replaces it with the real value. Reading the pathname
// directly skips that bad first-render entirely.
//
// keys is the ordered list of dynamic segment names matching the
// route's path. For /projects/[project] pass ["project"]; for
// /projects/[project]/services/[service] pass ["project", "service"]
// and the hook will pick the corresponding pathname segments in order
// from right to left up the route.
//
// During SSR (no window) and the very first render after hydration,
// returns an empty object so the consuming page can show its skeleton
// state and avoid firing a query against bogus inputs.
export function useRouteParams<T extends Record<string, string>>(
	keys: readonly (keyof T)[],
): Partial<T> {
	const pathname = usePathname();
	const params = useParams() as Record<string, string | string[]>;
	const [hydrated, setHydrated] = useState(false);

	useEffect(() => {
		setHydrated(true);
	}, []);

	// Server-render and prehydrated render: empty object. The page will
	// re-render with values once we're in the browser.
	if (!hydrated || typeof window === "undefined") {
		return {};
	}

	const fromPathname = parsePathname(pathname, keys as string[]);
	const out: Record<string, string> = {};
	for (const k of keys) {
		const key = k as string;
		const fromUrl = fromPathname[key];
		const fromBuild = paramAsString(params[key]);
		// Prefer the URL value. Fall back to Next's params when the URL
		// value is empty (shouldn't happen for matching routes) or when
		// it's the build-time placeholder "_".
		if (fromUrl && fromUrl !== "_") {
			out[key] = fromUrl;
		} else if (fromBuild && fromBuild !== "_") {
			out[key] = fromBuild;
		}
	}
	return out as Partial<T>;
}

// parsePathname extracts dynamic segment values from a concrete
// pathname given the ordered list of dynamic keys. The mapping
// assumes dynamic segments are interleaved with static literals in
// the same order kuso uses today: /projects/[project] →
// ["project"]; /projects/[project]/services/[service] →
// ["project", "service"]. Any segment that's a known literal
// (matching the keys' positional context) is skipped.
function parsePathname(pathname: string | null, keys: string[]): Record<string, string> {
	if (!pathname) return {};
	const segments = pathname.replace(/^\/+|\/+$/g, "").split("/");
	const out: Record<string, string> = {};
	if (keys.length === 0) return out;

	// We only ever want to extract a "project" / "service" param from
	// the canonical project route shape. Everything else (e.g.
	// /settings/nodes, /login) shouldn't yield a dynamic value, even
	// though textually "nodes" looks like one. Without this gate the
	// TopNav saw "nodes" as the current project and the project view
	// fired useProject("nodes") → 404 banner.
	if (segments[0] !== "projects" || segments.length < 2) {
		return out;
	}

	// /projects/<project>(/services/<service>)?(/...) — segments at
	// odd positions are static (services/, envs/, addons/, settings/,
	// logs/) and segments at even positions are dynamic.
	const dynamicValues: string[] = [];
	for (let i = 0; i < segments.length; i++) {
		const seg = segments[i];
		if (
			seg === "projects" ||
			seg === "services" ||
			seg === "envs" ||
			seg === "addons" ||
			seg === "settings" ||
			seg === "logs"
		) {
			continue;
		}
		dynamicValues.push(seg);
	}
	for (let i = 0; i < keys.length && i < dynamicValues.length; i++) {
		out[keys[i]] = dynamicValues[i];
	}
	return out;
}

function paramAsString(v: string | string[] | undefined): string | undefined {
	if (Array.isArray(v)) return v[0];
	return v;
}
