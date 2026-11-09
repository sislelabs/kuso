-- Where in the image a language package was found (Trivy's PkgPath, e.g.
-- "app/node_modules/tar/package.json"). Empty for OS packages and for
-- findings stored before this column existed.
ALTER TABLE "VulnFinding" ADD COLUMN IF NOT EXISTS "pkgPath" TEXT NOT NULL DEFAULT '';
