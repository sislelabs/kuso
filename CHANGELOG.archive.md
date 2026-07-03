# Changelog archive

Older release entries split out of the main CHANGELOG.md once it grew past 50 releases. Promoted out of the main file release-by-release.

## [0.18.55] — 2026-06-10

### Other
- Build(incidents): make targets for agent + bot images; pin bot deploy to v0.18.55 ([5ae2200](https://github.com/sislelabs/kuso/commit/5ae2200d591eed9fd4f029d59974c0e75b976e53))

### ✨ Features
- Feat(incidents): autonomous incident-response agent ([e5e3dd6](https://github.com/sislelabs/kuso/commit/e5e3dd67aea10296b727f3b86b4891369e01ce8b))

### 📝 Docs
- Docs(spec): autonomous incident-response agent design ([2d68882](https://github.com/sislelabs/kuso/commit/2d6888278e518a44b027a8757c0b3df4545ee6a2))

## [0.18.54] — 2026-06-10

### 🐛 Bug Fixes
- Fix(audit): use Postgres $N placeholders, not SQLite ? ([6658259](https://github.com/sislelabs/kuso/commit/6658259c3d44bd382083272ae3fc891d5d447b5d))

## [0.18.53] — 2026-06-10

### ✨ Features
- Feat(addons): data browser/editor on the postgres SQL tab ([1464108](https://github.com/sislelabs/kuso/commit/1464108543c9a684c62c322b9865f931e5d09983))

### 📝 Docs
- Docs(spec): addon data browser/editor design ([9b8e80d](https://github.com/sislelabs/kuso/commit/9b8e80d1a113af7ecd5819ebd4e65fbaad75f900))

## [0.18.52] — 2026-06-10

### 🐛 Bug Fixes
- Fix(pkgupdates): parse drain pod-list with jq, not fragile awk ([5c09f45](https://github.com/sislelabs/kuso/commit/5c09f4500cc104be82de487adfbccc420c93635d))

### 📝 Docs
- Docs(pkg-probe): note eviction RBAC is core-group, not policy (verified live) ([a3a497f](https://github.com/sislelabs/kuso/commit/a3a497f61b4c3731c900e8a1f242acb4ab01ffc3))

## [0.18.51] — 2026-06-10

### ✨ Features
- Feat(pkgupdates): daily aggregated update notification + rolling node drain ([4fd3094](https://github.com/sislelabs/kuso/commit/4fd3094a89fe4908ed936efca77dc16034f076a1))

## [0.18.50] — 2026-06-09

### 🐛 Bug Fixes
- Fix(cli): kuso upgrade falsely reported "timed out after 15m" on success ([c29fcb0](https://github.com/sislelabs/kuso/commit/c29fcb03be743c27269bab593c4ee202ac61ca1f))

## [0.18.49] — 2026-06-09

### 🐛 Bug Fixes
- Fix(addons): emit DIRECT_URL conn key so Prisma migrations skip the pooler ([94ddc12](https://github.com/sislelabs/kuso/commit/94ddc124651f073b182c829e949115c1b6c1d82f))

## [0.18.48] — 2026-06-08

### ✨ Features
- Feat(deploy): allow command override for any runtime, not just worker ([425a228](https://github.com/sislelabs/kuso/commit/425a2282182a02aa0c77e9032bb5172ca7b8e6fa))
- Feat(builds): custom Dockerfile filename per service ([034ecc7](https://github.com/sislelabs/kuso/commit/034ecc77a1c9d1340502d6082a583e6cbb61c128))
- Feat(addons): redpanda (Kafka) addon kind ([480fe97](https://github.com/sislelabs/kuso/commit/480fe970c1e787c49fdf2bd441734dbef88a77e7))
- Feat(import): docker-compose → kuso import (CLI + web) ([74aa708](https://github.com/sislelabs/kuso/commit/74aa708ed1be64d9141aaaa00684011d25ad0f06))

### 🐛 Bug Fixes
- Fix(rbac): grant kuso-server get/list services (node-bootstrap registry lookup) ([2bfd651](https://github.com/sislelabs/kuso/commit/2bfd651c006130eb71c3a7018bd35c29fbedb7fb))
- Fix(nodejoin): auto-wire in-cluster registry on joining nodes ([a5284cf](https://github.com/sislelabs/kuso/commit/a5284cfd95e26b65b063a164d6008cf6e616e647))
- Fix(nodejoin): drop 'set +o history' — dash aborts on it ([c1cdf3c](https://github.com/sislelabs/kuso/commit/c1cdf3c9b5aeeea028cd3717338f4f594c03a5e4))
- Fix(addons): clickhouse pass CLICKHOUSE_PASSWORD to container env ([f980867](https://github.com/sislelabs/kuso/commit/f980867e96a250906873d87fa4cf9ccd47c45467))
- Fix(migrate): normalize Coolify base_directory + dedupe/filter env vars ([ab5f766](https://github.com/sislelabs/kuso/commit/ab5f7667079a50ff4b0f2e1498e3454e9adc743d))
- Fix(crd): drop propertyNames from kusobuilds.buildEnv (unbreaks apply) ([22800de](https://github.com/sislelabs/kuso/commit/22800de49209694074ab9de332ec0ac20ad9900c))
- Fix(security): validate spec.dockerfile against shell-injection ([053be32](https://github.com/sislelabs/kuso/commit/053be32799e783b0ddf2769aaac0e90d656a89d6))
- Fix(addons): clickhouse writable users.d (non-root entrypoint rewrite) ([b6c3718](https://github.com/sislelabs/kuso/commit/b6c37188937fd5eefbd48e9452549efc19de23a1))
- Fix(addons): redpanda rpc-addr binds 0.0.0.0, advertises service DNS ([4d05fa5](https://github.com/sislelabs/kuso/commit/4d05fa5f1af8ffdfdd245db59c50bbd7f80e5c67))
- Fix(addons): redpanda writable /etc/redpanda + HOME (non-root) ([703a27f](https://github.com/sislelabs/kuso/commit/703a27f589192b92a63cca2ffef3ab66601d7bf3))
- Fix(addons): run redpanda via image entrypoint (rpk), not raw binary ([fbb88e5](https://github.com/sislelabs/kuso/commit/fbb88e50876ebe88b4d965149c1df2bd12d85a7f))
- Fix(addons): redpanda args as separate argv tokens ([c286aec](https://github.com/sislelabs/kuso/commit/c286aec11ff06bfbe530e9b8768e865e937e20fb))
- Fix(addons): redpanda args must be one flag+value string each ([98cca3f](https://github.com/sislelabs/kuso/commit/98cca3f3e5876374ee9bff87f00f348c6713d78f))
- Fix(crd): add redpanda to KusoAddon spec.kind enum ([70c4159](https://github.com/sislelabs/kuso/commit/70c4159783573e25d415ba004528b493427ef44f))
- Fix(import): map only implemented addon kinds + correct conn URL key ([10ce96e](https://github.com/sislelabs/kuso/commit/10ce96edc4a0a29cc316e122e9c7bbc4c15b2f0e))

### 📝 Docs
- Docs(nodejoin,skill): registry wiring, KUSO_K3S_URL, firewall ports + v0.18 features ([c47510a](https://github.com/sislelabs/kuso/commit/c47510aa2c9c29b9daf41d3775a17991e616dec9))
- Docs(import): docker-compose → kuso import design spec ([1847586](https://github.com/sislelabs/kuso/commit/1847586055fee3880379ce99b5dc031222165cd7))

## [0.18.33] — 2026-06-04

### 🐛 Bug Fixes
- Fix(crd): align volume schema field names with KusoVolume (sizeGi/accessMode) ([33a81d3](https://github.com/sislelabs/kuso/commit/33a81d3c15c8fc66a65a1fd0ad3d784f20b16a7b))

## [0.18.32] — 2026-06-04

### 🐛 Bug Fixes
- Fix(builds): nixpacks build-env values with spaces no longer break the build ([a287a4e](https://github.com/sislelabs/kuso/commit/a287a4e9281b2dd7457e4fe49bb2ee7d67a4c697))

### 📝 Docs
- Docs(migration): log NODE_ENV + dockerfile build-env fixes, full re-verify ([4bc6fb3](https://github.com/sislelabs/kuso/commit/4bc6fb3f1c295c9ff8d20d8059c724817574b352))

## [0.18.31] — 2026-06-04

### 🐛 Bug Fixes
- Fix(builds): pass build-time env to raw-Dockerfile builds as build-args ([c275215](https://github.com/sislelabs/kuso/commit/c2752157e741d21dce4c0924eadc2083c9e94d66))

## [0.18.30] — 2026-06-04

### 🐛 Bug Fixes
- Fix(builds): don't inject NODE_ENV (and runtime-only keys) into the build ([a4eb0ff](https://github.com/sislelabs/kuso/commit/a4eb0ff730d2c8a2714b5fa5fdd685f73ef8f2b1))

### 📝 Docs
- Docs(migration): log env-propagation fix + projects-grid feature ([f4a0342](https://github.com/sislelabs/kuso/commit/f4a0342136c9eb4fa2913f84cba05d1e09c2992a))

## [0.18.29] — 2026-06-04

### ✨ Features
- Feat(projects): per-user starring + folders + equal-height cards ([afd3002](https://github.com/sislelabs/kuso/commit/afd300225d8417fec930c48c2c3e29cd9fd6b05b))

## [0.18.28] — 2026-06-04

### 🐛 Bug Fixes
- Fix(env): explicit per-env override marker stops stale seeds shadowing service ([c98c47e](https://github.com/sislelabs/kuso/commit/c98c47e3306ca63346e9b9b601c8fd9aab65edf0))

## [0.18.27] — 2026-06-04

### 🐛 Bug Fixes
- Fix(ui): project card domain falls back to detected frontend's default host ([dc7ec8d](https://github.com/sislelabs/kuso/commit/dc7ec8db8aacfefccb17a8fb6aafad6cb06c8668))

### 📝 Docs
- Docs(migration): full e2e results — 11/14 apps verified (DB parity + pooler query + HTTP) ([8cefc78](https://github.com/sislelabs/kuso/commit/8cefc7894f9ce1f69c97067107757c0c2489b81b))
- Docs(migration): runtime blocker chain resolved (v0.18.25 env-ref, v0.18.26 netpolicy); all 13 apps Running ([cf53ac1](https://github.com/sislelabs/kuso/commit/cf53ac119631d6a59ad0d7297042487f3fa4bdf5))

## [0.18.26] — 2026-06-04

### 🐛 Bug Fixes
- Fix(netpolicy): allow project egress to the cluster-DB pooler/PG ([3eab6ba](https://github.com/sislelabs/kuso/commit/3eab6ba3f2480d71364d92553e89434987f6989b))

## [0.18.25] — 2026-06-04

### 🐛 Bug Fixes
- Fix(env): stale unresolved ${{ }} literal is not a per-env override ([0aa8936](https://github.com/sislelabs/kuso/commit/0aa89361d1024899aabf6b58b571fcd50f34fa9f))

### 📝 Docs
- Docs(migration): final session status — 14 apps migrated, env-ref propagation bug root-caused ([94512ce](https://github.com/sislelabs/kuso/commit/94512ce68d834d50ef70bdfadbb81333f18f262f))

## [0.18.24] — 2026-06-04

### 🐛 Bug Fixes
- Fix(apply): resolve ${{ addon.KEY }} refs in pending mode (kuso apply) ([3f0a3ff](https://github.com/sislelabs/kuso/commit/3f0a3ff6fc52dc6e463ee061c8a68b762bd77fd9))

### 📝 Docs
- Docs(migration): build-env feature shipped (v0.18.23); 6 apps fully migrated ([36325f7](https://github.com/sislelabs/kuso/commit/36325f73c30e0ffaa37603655ef05e1c6a729c5a))

## [0.18.23] — 2026-06-04

### 🐛 Bug Fixes
- Fix(builds): export NIXPACKS_* build-env into nixpacks process env (toolchain selection) ([e1af453](https://github.com/sislelabs/kuso/commit/e1af45347cf149e6f67e2fe3721642733e612033))

## [0.18.22] — 2026-06-04

### 🐛 Bug Fixes
- Fix(builds): pass build-time env to nixpacks --env (toolchain keys read at plan time) ([2af5788](https://github.com/sislelabs/kuso/commit/2af57888e7ccbffc9d5c973acdd9228d28ecbda8))

## [0.18.21] — 2026-06-04

### 🐛 Bug Fixes
- Fix(builds): inject build-time env in the REAL renderer (buildcontroller, not the dead chart) ([62bc4fb](https://github.com/sislelabs/kuso/commit/62bc4fb47c2faadc23289bc1946d2c1e4852e76a))

## [0.18.20] — 2026-06-04

### 🐛 Bug Fixes
- Fix(builds): validate build-env keys (command-injection hardening) ([c567636](https://github.com/sislelabs/kuso/commit/c567636efd7a8cc073d29c0034529b4624c8ba7f))

## [0.18.19] — 2026-06-04

### ✨ Features
- Feat(builds): build-time env injection (resolve service env → bake into image) ([7b279a9](https://github.com/sislelabs/kuso/commit/7b279a9e095d5ae812c476925f3d69400ad83b92))

### 📝 Docs
- Docs(migration): BLOCKER — kuso has no build-time env injection (3 Prisma/Next builds fail) ([c25f64a](https://github.com/sislelabs/kuso/commit/c25f64a1459e7994a91b1f4ee006d57edb6dd829))
- Docs(migration): 6 apps migrated (jira-mudira,boiler-code,db-masterclass,produktche,ilikata,bukvite30) + ownership fix ([41aafad](https://github.com/sislelabs/kuso/commit/41aafad5ad40691f5b578a4b265203f0583f1c86))
- Docs(migration): Phase 1 matrix + kuso.yml apply flow ([d6b0344](https://github.com/sislelabs/kuso/commit/d6b0344d9f4fb7761339344dc1cb92ce373a4857))
- Docs(migration): Phase 0 pooler shipped+verified; log PVC-drift follow-up ([7563b51](https://github.com/sislelabs/kuso/commit/7563b517bd5377daa28db9d3a59caf2e99c109a7))

### 🧹 Chores
- Chore: gitignore migration/ workdir (holds plaintext secrets) ([cf96bc4](https://github.com/sislelabs/kuso/commit/cf96bc4e3591fe92b8c67e620d01916be9ef449a))

## [0.18.17] — 2026-06-04

### ✨ Features
- Feat(cluster-db): shared auth_query PgBouncer in front of the cluster Postgres ([6658273](https://github.com/sislelabs/kuso/commit/66582733cfee861c71579ab09a752f35387e0e53))

## [0.18.16] — 2026-06-04

### ✨ Features
- Feat(settings): unify cluster database + instance-addons into one page ([bbcdca2](https://github.com/sislelabs/kuso/commit/bbcdca2db828d8d8854ab4bb0f532fe45642717a))

## [0.18.15] — 2026-06-04

### 🐛 Bug Fixes
- Fix(cluster-db): valid RFC-1123 name for the cluster PG addon + reserve kuso- prefix ([185bc66](https://github.com/sislelabs/kuso/commit/185bc66ec6b39a10877635c5fb53e1d605348368))

## [0.18.14] — 2026-06-04

### 🐛 Bug Fixes
- Fix(previews): wait for the env image before migrating (image-after-seed race) ([d3d37c9](https://github.com/sislelabs/kuso/commit/d3d37c90dfaa70f0a979ced1268850c1002759d3))

## [0.18.13] — 2026-06-04

### 🐛 Bug Fixes
- Fix(previews): dedupe redundant seed+migrate jobs per reopen ([3af1ff3](https://github.com/sislelabs/kuso/commit/3af1ff36ffc583a30c71dc8a791413a9562fdf1d))

## [0.18.12] — 2026-06-02

### 🐛 Bug Fixes
- Fix(previews): rewrite in-cluster service DNS so preview frontend SSRs its OWN API ([37f3122](https://github.com/sislelabs/kuso/commit/37f312200e8a05a0e28f687f80d2550d19f278a0))

## [0.18.11] — 2026-06-02

### 🐛 Bug Fixes
- Fix(previews): migrate Job must wait for the clone DB to accept connections ([749ae53](https://github.com/sislelabs/kuso/commit/749ae534c74bdac210961a914ae58f02fac61df2))

## [0.18.10] — 2026-06-02

### 🐛 Bug Fixes
- Fix(previews): run migrations after the seed, not at build-promote ([93f8f67](https://github.com/sislelabs/kuso/commit/93f8f67d18c95e1cee75952fc2fdb46b2d15148a))

### 📝 Docs
- Docs(spec): preview migrate-after-seed — couple migration to the seed, not the build ([cc616d2](https://github.com/sislelabs/kuso/commit/cc616d23416e7305accd86daa23531f6add58dd8))

## [0.18.9] — 2026-06-02

### ✨ Features
- Feat(previews): run service release hook against preview DB ([c539c1e](https://github.com/sislelabs/kuso/commit/c539c1ee81560942fb1f10e6ca173f59637dddd7))

### 🐛 Bug Fixes
- Fix(canvas): show only the env's DB, not both shared + per-PR clone ([5bf93e3](https://github.com/sislelabs/kuso/commit/5bf93e36695e7b4846a161abbb7719c2a49522c2))

### 📝 Docs
- Docs(spec): preview release hook — run service release Job against preview DB ([6af8e83](https://github.com/sislelabs/kuso/commit/6af8e8308eaac00f39d0d80e74d5045b468995cb))

## [0.18.8] — 2026-06-02

### ✨ Features
- Feat(ha): add spreadPolicy to kusoenvironments CRD schema + golden ([2621944](https://github.com/sislelabs/kuso/commit/2621944bf31b2594f87bb1aefd45539f2a6a420e))

### 🐛 Bug Fixes
- Fix(preview): repoint DB secretKeyRef envVars (READ_URL) to clone DB ([9ee394c](https://github.com/sislelabs/kuso/commit/9ee394c6a847bb23313b593a875e048392cc0d39))
- Fix(preview): seed job waits for Postgres readiness before dumping ([95d5beb](https://github.com/sislelabs/kuso/commit/95d5beb7746c70881d2939c09f1119ec0e1c279b))
- Fix(autoscale): guarantee a CPU request so the HPA can actually scale ([a219895](https://github.com/sislelabs/kuso/commit/a219895c54cf7568a7461693067bb81171b65a99))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.8 ship) ([a120803](https://github.com/sislelabs/kuso/commit/a120803f4901e50445ca6c2dd0ae330dbcb7f5a0))

## [0.18.7] — 2026-06-02

### ✨ Features
- Feat(ha): resolve + stamp spreadPolicy from live node count ([0af23eb](https://github.com/sislelabs/kuso/commit/0af23ebeba5f069532b313476c8551587fe4fac6))
- Feat(ha): spreadPolicy field + chart hard/soft topology spread ([119e53c](https://github.com/sislelabs/kuso/commit/119e53c9e4ec1d839efa0195bf8ba74d10390a70))

### 🐛 Bug Fixes
- Fix(pkg-updates): reboot path left node cordoned (post-reboot Job re-run) ([d072e56](https://github.com/sislelabs/kuso/commit/d072e561c372491ebc9a09093d07ac6f33f21041))

### 📝 Docs
- Docs(spec): guaranteed replica spread across nodes (HA) ([e74ae4b](https://github.com/sislelabs/kuso/commit/e74ae4b526125e58505d8d5bdddc519b262f689a))

### 🧪 Tests
- Test(ha): chart-render assertions for spreadPolicy ([a2258de](https://github.com/sislelabs/kuso/commit/a2258deb4d294583a2baf116e040aabc846511d0))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.7 ship) ([2685176](https://github.com/sislelabs/kuso/commit/2685176bad6ca9d98068bf8dde47d9c6fa132370))

## [0.18.6] — 2026-06-01

### Other
- Feat(pkg-updates) phase 3+4: apply patches + reboot orchestration ([a6430aa](https://github.com/sislelabs/kuso/commit/a6430aa0a97bbc98aa6c1a50435f466cec8a9fe5))
- Feat(pkg-updates) phase 2: advisory surface + notify ([e4ea1fd](https://github.com/sislelabs/kuso/commit/e4ea1fdfe4842ce30cb26fc8c6a079d5c6b4ae4b))
- Feat(pkg-updates) phase 1: host package-update probe DaemonSet ([47d2baf](https://github.com/sislelabs/kuso/commit/47d2baf9fa65d1f8fb8580d1d2f4ce14538a1fa3))

### 🐛 Bug Fixes
- Fix(backuphealth): don't @here-page when backups were never configured ([edd17c8](https://github.com/sislelabs/kuso/commit/edd17c8ba385fe2ca8211224ccc951b3057ada78))
- Fix(cron): http crons crashloop with CreateContainerConfigError ([3cdb1ec](https://github.com/sislelabs/kuso/commit/3cdb1ec4f6e399c1b13821a45947881d1671883c))

### 📝 Docs
- Docs(spec): host package-update advisory + patch orchestration ([4d8ec48](https://github.com/sislelabs/kuso/commit/4d8ec489573bea460101f65d8da92866b5bd4a46))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.6 ship) ([64c3815](https://github.com/sislelabs/kuso/commit/64c3815d8ce31bb1d0f9ef076cd993b983035755))

## [0.18.5] — 2026-05-30

### 🐛 Bug Fixes
- Fix(projects): card domain links to the base domain + add settings shortcut ([db84a2f](https://github.com/sislelabs/kuso/commit/db84a2f1aaa15318820ad6a8a8cb45fb3895ebef))
- Fix(auth): don't log out the admin when they change a group they're in ([a982977](https://github.com/sislelabs/kuso/commit/a98297778d8f96afb5691e3bcea7f15e5a102331))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.5 ship) ([5e63f1d](https://github.com/sislelabs/kuso/commit/5e63f1df259f6bc3451e277ec92b6a7c5645816f))

## [0.18.4] — 2026-05-30

### 🐛 Bug Fixes
- Fix(access): click-to-add multi-select popovers for members + grants ([2861c18](https://github.com/sislelabs/kuso/commit/2861c185d04c41e924bf61dee6546065b341a5c8))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.4 ship) ([e7e754b](https://github.com/sislelabs/kuso/commit/e7e754b642dce73e038036b66be15d6717265ded))

## [0.18.3] — 2026-05-30

### ✨ Features
- Feat(groups): show + manage group members (roster, not blind add/remove) ([2694dc4](https://github.com/sislelabs/kuso/commit/2694dc4f04339b5fb3fb38a905ad309bb6a030f9))
- Feat(users): admin can set a user's instance role from the Users page ([fedb1eb](https://github.com/sislelabs/kuso/commit/fedb1ebf2a935bc308e3e061026f4a4f2784e071))

### 🐛 Bug Fixes
- Fix(notifications): keep the Discord mention picker open on select ([ccc15f8](https://github.com/sislelabs/kuso/commit/ccc15f834ca907ecccba1bd0ad53065375399edf))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.3 ship) ([360e721](https://github.com/sislelabs/kuso/commit/360e7218e2ccd2c93ac74dac597f388f17be638e))

## [0.18.2] — 2026-05-30

### 🐛 Bug Fixes
- Fix(notifications): persist per-event Discord mention rules server-side ([b0e72d0](https://github.com/sislelabs/kuso/commit/b0e72d03940ed98f141646e325d1709355eab658))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.2 ship) ([2d7f1a9](https://github.com/sislelabs/kuso/commit/2d7f1a911b91595327bf3744eeefb7b39c434761))

## [0.18.1] — 2026-05-30

### 🐛 Bug Fixes
- Fix(db): drop carried-over indexes before recreating in LogLine partition migration ([146f16c](https://github.com/sislelabs/kuso/commit/146f16c1fc5a8030207fcaaab3753878bce98820))
- Fix(notifications): persist explicit "none" Discord mention over @here default ([3208b14](https://github.com/sislelabs/kuso/commit/3208b14d916ebb071cb7091c0e3c8fd0b82ded61))

### 🔨 Refactors
- Refactor: decouple served-app domain from the kuso.sislelabs.com literal ([04c3609](https://github.com/sislelabs/kuso/commit/04c3609d382b4f02b62adf8c5077446c72884977))
- Refactor(db): purge SQLite dialect — native Postgres $N + ON CONFLICT ([a9561c0](https://github.com/sislelabs/kuso/commit/a9561c00a4285c35d8e222755d8e0a454a9ea41b))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.1 ship) ([74c6ba9](https://github.com/sislelabs/kuso/commit/74c6ba9204e6d3b3677f950828a569f8460f43af))

## [0.18.0] — 2026-05-30

### Other
- M3: latency histograms + schema-migration gauges on /metrics ([2ef8384](https://github.com/sislelabs/kuso/commit/2ef8384b1a553e3c12a4d8bcf9c845857691cb25))
- M2: reconciler tests for the load-bearing untested paths ([4967bed](https://github.com/sislelabs/kuso/commit/4967bed39c91e820ad583aa9744b042e8a1d653e))
- M1: versioned DB migrations (runner + first migration + ops surfacing) ([afe5eb2](https://github.com/sislelabs/kuso/commit/afe5eb294b240bd69875f576a6a21f86bbe81a49))

### 🐛 Bug Fixes
- Fix(web): project card — stack repo/domain rows + service-domain fallback + correct link ([7d39973](https://github.com/sislelabs/kuso/commit/7d39973bda814ab358693470b033031d2b06cd39))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (pre-v0.18.0 ship) ([ec9e3d9](https://github.com/sislelabs/kuso/commit/ec9e3d905a7b781167079def9be376d489ecf2fd))

## [0.17.29] — 2026-05-30

### ✨ Features
- Feat(previews): clone instance-pg addons into isolated per-PR databases (tier 3) ([63a816c](https://github.com/sislelabs/kuso/commit/63a816ccdb571440d4df6e0281eb83ae1f15e069))
- Feat(addons): record + revert addon config revisions (tier 3) ([0b9b83e](https://github.com/sislelabs/kuso/commit/0b9b83eca67b7fbd851cfaf8170b95d4f35d4d6f))
- Feat(services): per-service CPU/memory requests+limits (tier 3) ([ba9f535](https://github.com/sislelabs/kuso/commit/ba9f5352c3bf40227bd9114882a2b7a06c782ed9))
- Feat(reliability): operator blind-spot signals — poller liveness, registry GC, webhook health (tier 2) ([0c780a9](https://github.com/sislelabs/kuso/commit/0c780a98ef2666f56da122e358ba19638c22d6a0))
- Feat(reliability): surface control-plane backup health + addon-PVC orphan trail (tier 1) ([91ff76e](https://github.com/sislelabs/kuso/commit/91ff76ea6c7b7cda9f60b9185c5935564d0c89f4))
- Feat(github): per-service repo routing + editable default repo + card fallback ([7b5d82f](https://github.com/sislelabs/kuso/commit/7b5d82f3f1964e786056f48c76d9cac2a91e952a))
- Feat(web): make the project card's GitHub repo row clickable ([ed97870](https://github.com/sislelabs/kuso/commit/ed97870309d2fad5494669a4b21a02a24833bd3d))

## [0.17.28] — 2026-05-30

### ✨ Features
- Feat(builds): rollback window — keep last N images, prune older, records persist ([c3002ed](https://github.com/sislelabs/kuso/commit/c3002ed242fb46d5bdefd755060019c6be307bb6))
- Feat(builds): archive build summaries so deployment history survives cleanup ([315535e](https://github.com/sislelabs/kuso/commit/315535ec49c8ead24f8caa849dfcb65a47a2e1f6))
- Feat(web): role v2 UI — 3-role pickers, project access panel, env masking (phase 5) ([bc7f971](https://github.com/sislelabs/kuso/commit/bc7f9717107b943ec6294a2466df82a346047753))
- Feat(authz): role v2 grant-management API (phase 4) ([1bcbbd0](https://github.com/sislelabs/kuso/commit/1bcbbd0ea87419b49a42c1d47cedbff5288b3e81))
- Feat(authz): role v2 data model — ProjectGrant + user roles + migration (phase 2) ([09c5a55](https://github.com/sislelabs/kuso/commit/09c5a5577efe9bb4fa46c250a016a51588e210ee))
- Feat(authz): role system v2 core — viewer/editor/admin (phase 1+3) ([eeb9f47](https://github.com/sislelabs/kuso/commit/eeb9f47ef49b34f2de7fb6c735bbcfc6be47a10f))

### 🐛 Bug Fixes
- Fix(authz): instance-addon picker gate — addons:write → authenticated ([8aa5d9b](https://github.com/sislelabs/kuso/commit/8aa5d9b216daab8bbe99456d3a9c99baec3787c0))
- Fix(authz): close role-v2 client + stale-gate gaps from code review ([9e1fe6e](https://github.com/sislelabs/kuso/commit/9e1fe6e3d19463caba477065e827198318312e76))

### 📝 Docs
- Docs(spec): role v2 — sql console + null-inherit clarifications ([181cf7d](https://github.com/sislelabs/kuso/commit/181cf7d543b5784be592eb16dba5703e97f9ddee))
- Docs(spec): role system v2 design (viewer/editor/admin, hybrid grants) ([74edc97](https://github.com/sislelabs/kuso/commit/74edc9712c9ac28bae9ac54ff1c172e9ca3ac8ae))

## [0.17.27] — 2026-05-30

### ✨ Features
- Feat(canvas): surface per-env custom domain on node + overlay URL ([9288cc8](https://github.com/sislelabs/kuso/commit/9288cc8a6c91201097cb0360b65cd730619951cb))

### 🐛 Bug Fixes
- Fix(previews): seed Job DNS/db-name + cascade-delete on PR close ([2f2f042](https://github.com/sislelabs/kuso/commit/2f2f042144c682c5fe75b5124228fdf2d5a2cadf))

### 📝 Docs
- Docs(skill): rewrite env/secrets handling + add domains/previews; full review ([403e184](https://github.com/sislelabs/kuso/commit/403e1849634e302620d2d0fef45678d6c46f9765))

## [0.17.26] — 2026-05-29

### 🐛 Bug Fixes
- Fix(projects): base-domain change now rewrites env hosts (FQN→short bug) ([f023dbf](https://github.com/sislelabs/kuso/commit/f023dbf42ccb081f2747dff10777779c5a1e285f))

## [0.17.25] — 2026-05-29

### 🐛 Bug Fixes
- Fix(previews): self-heal recreated preview env image + pin previews to 1 replica ([e260538](https://github.com/sislelabs/kuso/commit/e26053820c65a792402782ff7cf6780b0ca9b0bb))

## [0.17.24] — 2026-05-29

### 🐛 Bug Fixes
- Fix(previews): stop addon over-mount into preview + production envs ([1085649](https://github.com/sislelabs/kuso/commit/1085649c68282cc0f9bcfc0f1c0ff97bf44d717a))
- Fix(crd): add previews.baseDomain to KusoProject CRD schema ([2a6f03a](https://github.com/sislelabs/kuso/commit/2a6f03ab9b40cbb57d8011e8ea4ca6b4085fb4c3))

## [0.17.23] — 2026-05-29

### ✨ Features
- Feat(previews): custom preview base domain + fix preview-DB-clone password drift ([f85fc4c](https://github.com/sislelabs/kuso/commit/f85fc4c353ff00e0dcf4c0233df4cb59e4bb755c))

## [0.17.22] — 2026-05-29

### ✨ Features
- Feat(env): per-env env-var overrides + fix AddEnvironment service-ref rescope ([148ae71](https://github.com/sislelabs/kuso/commit/148ae711ded25824e8ee6bbef1a5d0a31b58da79))

### 🐛 Bug Fixes
- Fix(rbac): grant kuso-server delete on PVCs so `project delete --purge-data` works ([4b42e12](https://github.com/sislelabs/kuso/commit/4b42e12f57795146f48ed5b6decd5b357cde3ab6))

## [0.17.21] — 2026-05-29

### 🐛 Bug Fixes
- Fix(projects): use list+delete for project-scoped secret cleanup (no deletecollection RBAC) ([cc0f1b5](https://github.com/sislelabs/kuso/commit/cc0f1b5af305984feae20c58e97881b6be835b29))

## [0.17.20] — 2026-05-29

### 🐛 Bug Fixes
- Fix: env-unset data loss, ghcr tag pagination, updater image fallback, env-scoped domains CLI ([c255bdc](https://github.com/sislelabs/kuso/commit/c255bdcf430dedc1762a36a5cd1e117d457de6c8))

## [0.17.19] — 2026-05-29

### 🐛 Bug Fixes
- Fix(domains): `kuso domains add` propagates to the production env (ingress + cert) ([4000a98](https://github.com/sislelabs/kuso/commit/4000a98fbd262a2282c82b207dfdafe132619e39))

## [0.17.17] — 2026-05-29

### 🐛 Bug Fixes
- Fix(projects): empty subscription persists + project delete cleans orphaned secrets ([0cbce7f](https://github.com/sislelabs/kuso/commit/0cbce7fbd49a42e69d629c1d66d370737c9cb515))

## [0.17.16] — 2026-05-29

### 🐛 Bug Fixes
- Fix(addons): stop perpetual helm-operator reconcile churn + gate release-hook promotion ([f86205b](https://github.com/sislelabs/kuso/commit/f86205b745f7474844660e2f2b11d3fcd799ca4f))

## [0.17.15] — 2026-05-29

### Other
- Revert(web): drop per-env Secrets section from Variables editor ([cae09b9](https://github.com/sislelabs/kuso/commit/cae09b91da6819f417fc956d6bd5737f8602c3c6))

## [0.17.14] — 2026-05-29

### ✨ Features
- Feat(web): per-env secrets are editable; subscribed keys count as set ([efd1730](https://github.com/sislelabs/kuso/commit/efd173030d119d8082732b87d05cc0182fb4a3c1))

## [0.17.13] — 2026-05-29

### ✨ Features
- Feat(web/server): show per-env secrets in Variables tab + Command propagation ([434611f](https://github.com/sislelabs/kuso/commit/434611f590eda4bef4e5bafedf6def7fb633c63e))

## [0.17.12] — 2026-05-29

### 🐛 Bug Fixes
- Fix(projects): purge-data must also sweep StatefulSet-generated PVCs ([e3239ba](https://github.com/sislelabs/kuso/commit/e3239bad89132470ed0cd8142327c356c3192c06))

## [0.17.11] — 2026-05-29

### ✨ Features
- Feat(projects): kuso project delete --purge-data ([3da1299](https://github.com/sislelabs/kuso/commit/3da1299768ba29bd7f742c2593c6bb4f69706967))

## [0.17.10] — 2026-05-29

### ✨ Features
- Feat(cli): kuso service set --path / --branch ([4e2de50](https://github.com/sislelabs/kuso/commit/4e2de504cc06db5052b8049343b1236a467c3d04))

## [0.17.9] — 2026-05-29

### 🐛 Bug Fixes
- Fix(api): worker service creation drops FromService field ([ee53d4a](https://github.com/sislelabs/kuso/commit/ee53d4a9e49f622abeae13f7ef3dcbf6394d85ff))

## [0.17.8] — 2026-05-29

### 🐛 Bug Fixes
- Fix(previews): scope addons via canonical env label, not bespoke marker ([d651fec](https://github.com/sislelabs/kuso/commit/d651fecbce6a7c7bd2c059f260863870cc9461ca))

## [0.17.7] — 2026-05-29

### ✨ Features
- Feat(cli): kuso run --follow; fix(previews): drop service-name guessing ([633ade5](https://github.com/sislelabs/kuso/commit/633ade51ab34879a9330721c358d3387801efb7a))

## [0.17.6] — 2026-05-29

### 🐛 Bug Fixes
- Fix(previews): break addon-clone idempotency loop ([d070e4c](https://github.com/sislelabs/kuso/commit/d070e4caf75c6b9c88888fa72579407b94c89654))

## [0.17.5] — 2026-05-29

### 🐛 Bug Fixes
- Fix(previews): default per-PR Postgres clones to ON ([42dcc33](https://github.com/sislelabs/kuso/commit/42dcc333733296cec1ed2c42803d17604c873d0a))

## [0.17.4] — 2026-05-29

### 🐛 Bug Fixes
- Fix(previews): clone per-env secrets w/ URL rewrite + enable per-PR DB ([793c8da](https://github.com/sislelabs/kuso/commit/793c8da624a6c85f5f93ed0fca5ab81bf0dc942b))

## [0.17.3] — 2026-05-28

### 🐛 Bug Fixes
- Fix(canvas+redeploy): env-aware build labels and branch-correct redeploy ([76d518b](https://github.com/sislelabs/kuso/commit/76d518b2c848ffb021d0ab63d677671f9b1e7053))

## [0.17.2] — 2026-05-28

### 🐛 Bug Fixes
- Fix(previews): default baseEnv to production in legacy mode (B1 audit) ([fac5253](https://github.com/sislelabs/kuso/commit/fac5253407d7b516b92a20cbfe14914460aaad0a))

## [0.17.1] — 2026-05-28

### 🐛 Bug Fixes
- Fix(v0.17.1): secrets locking, missing-key warnings, env-var cascade tests ([81a0693](https://github.com/sislelabs/kuso/commit/81a069355a3971ddd3db3775ad2039694add538e))
- Fix(v0.17.0 audit): subscription wiring, env-scoped refs, canvas accuracy ([97d3d82](https://github.com/sislelabs/kuso/commit/97d3d82de24916130a0aea0074a30d3d1106da07))
- Fix(rollback): env-scoped rollback (B1.1) ([d7272dc](https://github.com/sislelabs/kuso/commit/d7272dc3ab4cb3ae6a7a51fbe0ae87fe1a664bbc))

## [0.17.0] — 2026-05-28

### ✨ Features
- Feat(previews): reviewer page + seed Job + PR-comment integration ([7780126](https://github.com/sislelabs/kuso/commit/7780126d2c080be7b7e69a1bb78bc5b01efe9014))
- Feat(previews): trigger gating + env-var inheritance from base env ([7f0c83b](https://github.com/sislelabs/kuso/commit/7f0c83ba63219d014b78155c35bfd4f005d4f478))

### 🐛 Bug Fixes
- Fix(previews/web): hash-route reviewer page so kuso/web static-export builds ([8d4b146](https://github.com/sislelabs/kuso/commit/8d4b1465a10c4c207bd174e04e1f2ccb44a36527))

## [0.16.27] — 2026-05-28

### 🐛 Bug Fixes
- Fix(propagate): drop env's shared-secret refs when a per-env override exists ([1d75716](https://github.com/sislelabs/kuso/commit/1d757164fd80ae9937c38e99b799746469f7e9ce))

## [0.16.26] — 2026-05-28

### 🐛 Bug Fixes
- Fix(propagate): per-env Secret overrides win over subscribed shared keys ([0243cd9](https://github.com/sislelabs/kuso/commit/0243cd99ba42f9c8f7397043a4d4e2de11c2f39a))

## [0.16.25] — 2026-05-28

### 🐛 Bug Fixes
- Fix(canvas): infer service→service edges from URL-named env keys ([f9ef9b1](https://github.com/sislelabs/kuso/commit/f9ef9b153396b19b84a92a0bc1bf31b9691f75e3))

## [0.16.24] — 2026-05-28

### 🐛 Bug Fixes
- Fix(subscribed-addons): respect project prefix on conn-secret allow-list ([3f628e7](https://github.com/sislelabs/kuso/commit/3f628e73a1ace7c47fed83eb2438dc4813152089))

## [0.16.23] — 2026-05-28

### ✨ Features
- Feat: per-service addon-mount subscription (auto-migrate from legacy) ([89525af](https://github.com/sislelabs/kuso/commit/89525af747589259692fb8e48485db48b16199a8))

## [0.16.22] — 2026-05-28

### 🐛 Bug Fixes
- Fix(propagate): drop leftover shared-secret refs when unsubscribing ([9feb0d3](https://github.com/sislelabs/kuso/commit/9feb0d3ef52a7c8952a1c40a0d92ce29ab6e6556))

## [0.16.21] — 2026-05-28

### 🐛 Bug Fixes
- Fix(ui): projects list sorted alphabetically by name ([6429c95](https://github.com/sislelabs/kuso/commit/6429c9556bd88ccd2080c924d641cc5ff27528ed))

## [0.16.20] — 2026-05-28

### 🐛 Bug Fixes
- Fix(overlay): use env label to match env CR — staging tab no longer shows production URL ([497dcd9](https://github.com/sislelabs/kuso/commit/497dcd92edc3a9078480b8424086c52bc45123dd))

## [0.16.19] — 2026-05-28

### ✨ Features
- Feat: per-env custom domains (server-side) + fix auto-host for user baseDomain ([83af977](https://github.com/sislelabs/kuso/commit/83af977e63a611bb8482c9f6633663706adc2d0b))

### 🐛 Bug Fixes
- Fix(ui): NetworkingSection binds to env-scoped custom domains ([a7da957](https://github.com/sislelabs/kuso/commit/a7da957ea83f4739ce4119fa6506c140e2d55a33))

## [0.16.18] — 2026-05-28

### 🐛 Bug Fixes
- Fix: always-visible canvas footer, per-env auto-domain, drift superset, eye-toggle dirty ([f0b5e91](https://github.com/sislelabs/kuso/commit/f0b5e917b9e32d9b4c34123b8f0222123e85983c))

## [0.16.17] — 2026-05-28

### 🐛 Bug Fixes
- Fix(propagate/canvas): preserve shadow overrides; canvas prefers production env ([19eba22](https://github.com/sislelabs/kuso/commit/19eba225a509a0c7fa12b7f05681a8aac6c9305e))

## [0.16.16] — 2026-05-28

### 🐛 Bug Fixes
- Fix(propagate): preserve per-env envVar overrides; fix overlay URL ([3a041ae](https://github.com/sislelabs/kuso/commit/3a041ae4250ebd000798e5a016135d6171da575d))
- Fix(crd): allow project-shared/instance-shared secrets in valueFrom.secretKeyRef ([fae6355](https://github.com/sislelabs/kuso/commit/fae635578bc6983b188c8317c8272493f6cede3e))

## [0.16.15] — 2026-05-28

### 🐛 Bug Fixes
- Fix(kube): bump default QPS/burst — propagation was rate-limited to 0 ([97ac356](https://github.com/sislelabs/kuso/commit/97ac3564a90ef9956ae1c8b1734500acecd11b16))

## [0.16.14] — 2026-05-28

### Other
- Debug: log env-propagation walk + per-env outcome ([04abfe5](https://github.com/sislelabs/kuso/commit/04abfe5a21e4972ef4e7171e8c6b3e033fe4ecff))

## [0.16.13] — 2026-05-28

### 🐛 Bug Fixes
- Fix(ui): chip toggle silently no-op'd — api() double-stringified the body ([5107577](https://github.com/sislelabs/kuso/commit/5107577e8431904a741682a0ce5ae5c20819c15f))

## [0.16.12] — 2026-05-28

### 🐛 Bug Fixes
- Fix(migration): use short service name for GetService/lockService ([8395600](https://github.com/sislelabs/kuso/commit/839560036cccf4eaa46aab58c4c6ee8087a3835b))

## [0.16.11] — 2026-05-28

### ✨ Features
- Feat: remove legacy mode for shared-secret subscription; auto-migrate on startup ([ab48328](https://github.com/sislelabs/kuso/commit/ab48328f9f2146a959fd8eb897ff3b9df7196717))

## [0.16.10] — 2026-05-28

### ✨ Features
- Feat: per-service opt-in subscription for project + instance shared secrets ([f30f9fa](https://github.com/sislelabs/kuso/commit/f30f9fa3074ae83241e320269d7a2cff88239559))
- Feat(crd): KusoService.spec.sharedEnvKeys + Env mirror (no behavior change yet) ([a9c45a2](https://github.com/sislelabs/kuso/commit/a9c45a2995379cc64b54d5a224edac135e7a04b1))

### 🐛 Bug Fixes
- Fix(ui): non-prod banner padding + canvas stops drawing implicit addon edges ([7db78f6](https://github.com/sislelabs/kuso/commit/7db78f6d0a52b7f1313589c021c3f3259aa3e481))

## [0.16.9] — 2026-05-28

### ✨ Features
- Feat(addon): opt-in WebUI proxy + dashboard chip for mailpit/nats consoles ([9ffdb3c](https://github.com/sislelabs/kuso/commit/9ffdb3c869dce20633f0f94dd4c6005be8922a16))

### 🐛 Bug Fixes
- Fix(cron): use curlimages/curl for kind=http probes (was missing curl) ([9720d0e](https://github.com/sislelabs/kuso/commit/9720d0ed91c73e926a58c603c5773a263501c131))

### 📝 Docs
- Docs: clarify per-env override path for build-time-inlined frontend envs ([80ee524](https://github.com/sislelabs/kuso/commit/80ee52450f62a25d545e7b764a7a8f6691b7199a))
- Docs: how to handle NEXT_PUBLIC_* (build-time-inlined) frontend envs ([b9a4d56](https://github.com/sislelabs/kuso/commit/b9a4d56d663fcf154aaccb2c757e2c0626caba19))

## [0.16.8] — 2026-05-28

### 🐛 Bug Fixes
- Fix: prevent worker ingresses + bound rollout history ([b698dbd](https://github.com/sislelabs/kuso/commit/b698dbdc29acc4580d26e2b0dac6725231a62ca1))

## [0.16.7] — 2026-05-28

### 🐛 Bug Fixes
- Fix(install): bump fs.inotify limits to avoid silent pod failures ([05d2676](https://github.com/sislelabs/kuso/commit/05d267664a93f52d9ef5ae2425925ec60cb35f5f))

## [0.16.6] — 2026-05-27

### 🐛 Bug Fixes
- Fix(env): custom envs get own hosts; chart prunes stale tlsHosts (#5) ([9bf320f](https://github.com/sislelabs/kuso/commit/9bf320f210fb6abfab32a6654a7101ef3c7fed7c))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (from v0.16.5 ship) ([98cfb8a](https://github.com/sislelabs/kuso/commit/98cfb8aa64264520586f0ffae339d5a96848bc8d))

## [0.16.5] — 2026-05-27

### 🐛 Bug Fixes
- Fix(web): project-shared addons render under every env tab (#4) ([25a79cb](https://github.com/sislelabs/kuso/commit/25a79cb2ec300107e6e0513ef5909a1b983073af))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (from v0.16.4 ship) ([3b2e93e](https://github.com/sislelabs/kuso/commit/3b2e93ee1d83a87de6a48e019532373bd48bca18))

## [0.16.4] — 2026-05-27

### 🐛 Bug Fixes
- Fix: node-bootstrap and addon defaults — TLS pg, NATS auth, worker fromService (#3) ([065a0ca](https://github.com/sislelabs/kuso/commit/065a0ca487cf1e9da59da704319dc3dbdf144a21))

## [0.16.3] — 2026-05-27

### 🐛 Bug Fixes
- Fix(crd): add 'image' to KusoService.spec.runtime enum ([f35dfca](https://github.com/sislelabs/kuso/commit/f35dfcae41bc66e070b1551f2774c47dad9bd21b))

### 📝 Docs
- Docs(skill): refresh kuso skill for v0.16.x ([807cc15](https://github.com/sislelabs/kuso/commit/807cc1521d61239c94f0cc135cde126a0880c999))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries ([9b72e47](https://github.com/sislelabs/kuso/commit/9b72e47e88a5bb264ab75960d9c935173eb6f1b9))

## [0.16.2] — 2026-05-27

### 🐛 Bug Fixes
- Fix(env): KusoEnvironmentSpec.ReplicaCount is *int — same omitempty bug ([8ca0bb5](https://github.com/sislelabs/kuso/commit/8ca0bb5df41dfeb4ec3e97c76bd6e39c6d9b6326))

## [0.16.1] — 2026-05-27

### 🐛 Bug Fixes
- Fix(scale): KusoScaleSpec.Min is *int — omitempty was dropping min=0 ([2eb6771](https://github.com/sislelabs/kuso/commit/2eb6771c2e1152b5e6275cf210572d8f3f73bb2e))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries ([e1aa859](https://github.com/sislelabs/kuso/commit/e1aa8599b61c296f12c16dd349b5f9f17c73f470))

## [0.16.0] — 2026-05-27

### ✨ Features
- Feat(crons): cronwatch.Watcher dispatches per-cron onFailure webhook ([7987d95](https://github.com/sislelabs/kuso/commit/7987d9596b302db4b70cc94dfcb9a27c10c9d92c))
- Feat(sleep): wakeOn.excludePaths keeps callback paths warm ([1dc06f7](https://github.com/sislelabs/kuso/commit/1dc06f7ed293e1c26b5716dce9bea32adaca809c))
- Feat(backups): external Postgres backup branch (PlanetScale / Neon / RDS) ([b55f388](https://github.com/sislelabs/kuso/commit/b55f388fdb00e785826239dc04d7dca2ac209e3b))
- Feat(release): KusoService.spec.release.command — Heroku-style hook ([ac30b83](https://github.com/sislelabs/kuso/commit/ac30b83ab4720baddec9da8cc664f70bbb7a8e97))
- Feat(addons): NATS JetStream HA — 3-replica clustered StatefulSet ([4bb6052](https://github.com/sislelabs/kuso/commit/4bb605282804c55e5000ac340002e5db5ac98e4e))
- Feat(addons): Redis + S3/MinIO scheduled backups to cluster S3 ([42dbf75](https://github.com/sislelabs/kuso/commit/42dbf75610fc23f649de9bd1b8ff6402eb68e0fa))
- Feat(crons): KusoCron.spec.onFailure CRD field (webhook + HMAC) ([0bbc670](https://github.com/sislelabs/kuso/commit/0bbc670d30a2642a6cf754968d78013e8244e51d))

### 📝 Docs
- Docs(plan): v0.16.0 — Tickero migration readiness ([4155546](https://github.com/sislelabs/kuso/commit/4155546c04b320c87576f5d83f3330330ac3db3b))
- Docs(specs): release hooks, wildcard certs, Loki log-sink designs ([f32561f](https://github.com/sislelabs/kuso/commit/f32561fb25670e172d03acd6e3aa8e44fd2f7ae2))
- Docs(sleep): warn against enabling sleep on webhook/callback services ([31729ae](https://github.com/sislelabs/kuso/commit/31729ae35910b783c405b1036f46a92811be316e))

### 🧹 Chores
- Chore: archive promoted CHANGELOG entries (v0.9.42-v0.9.47) ([6e5db69](https://github.com/sislelabs/kuso/commit/6e5db69406a953bcfd6ee82fc7a3cf84883234d3))

## [0.14.1] — 2026-05-23

### ✨ Features
- Feat(canvas): add cron / run command entries to service right-click menu ([e9a0c7e](https://github.com/sislelabs/kuso/commit/e9a0c7e34edc3b5d08fb94475178a90223e98676))

## [0.14.0] — 2026-05-23

### ✨ Features
- Feat(env): unit C — ref-picker type-ahead + first-deploy coachmark ([38130ea](https://github.com/sislelabs/kuso/commit/38130ea25d7249568c14e77ea2cd196651859515))
- Feat(overlay): unit B — data-driven Crons/Runs tab visibility ([ab77080](https://github.com/sislelabs/kuso/commit/ab77080fae32806d5ba957c32e1e4f88a6840961))
- Feat(failures): unit A — server-side failure classifier + bell deep-links ([edebde1](https://github.com/sislelabs/kuso/commit/edebde193ae568f38691960fb8b6bbda6d129973))

### 📝 Docs
- Docs: spec for UX deep-dive top-5 fix bundle ([a859fdc](https://github.com/sislelabs/kuso/commit/a859fdceea1bf2924368df1730787f6582d6d601))

## [0.13.18] — 2026-05-23

### ✨ Features
- Feat(web): surface public-TCP state on the addon Connection card ([51f5d26](https://github.com/sislelabs/kuso/commit/51f5d2656a6881da2c0eafb120574a1f6a4d9c37))

## [0.13.17] — 2026-05-23

### ✨ Features
- Feat(web): UI toggle for the addon Public TCP endpoint ([b3f368d](https://github.com/sislelabs/kuso/commit/b3f368d0a949ef075bb31d935ec2ab4286ed0a75))

## [0.13.16] — 2026-05-23

### ✨ Features
- Feat(addons): opt-in public TCP endpoint (admin-gated) ([8e5a858](https://github.com/sislelabs/kuso/commit/8e5a858708e18a8a7e78f12f72ff78e788cdfad8))
- Feat: kuso db port-forward / db connect — local tunnels to addons ([61405c8](https://github.com/sislelabs/kuso/commit/61405c8aad8f46a594ad157bde972825275c48c9))

## [0.13.14] — 2026-05-21

### 🐛 Bug Fixes
- Fix(addons): addon-add wires conn secret into existing services (closes watch-cache race) ([d5d150c](https://github.com/sislelabs/kuso/commit/d5d150ca71dec55dd4a53818cf3efc6a761adef5))
- Fix(addons): refreshEnvSecrets accepts explicit conn secrets ([e4002fe](https://github.com/sislelabs/kuso/commit/e4002fe8cf7b2e002b243eb9e24798570eb2fbf6))
- Fix(canvas): draw service→service edges for in-cluster DNS refs ([3181a04](https://github.com/sislelabs/kuso/commit/3181a046922a0bd281e81160c186c7b9ed49592e))

### 📝 Docs
- Docs: addon-add conn-secret race fix implementation plan ([5c54a74](https://github.com/sislelabs/kuso/commit/5c54a7429190dc06db183eecc50c9bc6103fb30d))
- Docs: addon-add conn-secret read-after-write race fix design ([5217b52](https://github.com/sislelabs/kuso/commit/5217b522c67f483b1edf3d4243a786f4bee45937))

### 🧹 Chores
- Chore: archive older changelog entries ([3d61102](https://github.com/sislelabs/kuso/commit/3d61102bcc87845068efca54391ada280fd20c2b))

## [0.13.13] — 2026-05-21

### ✨ Features
- Feat(web): blast-radius warnings on service config changes ([b05ddfe](https://github.com/sislelabs/kuso/commit/b05ddfe21cc8983f85ef27829ab72f2979a45fe5))
- Feat: browser terminal — interactive pod shell in the web UI ([2010779](https://github.com/sislelabs/kuso/commit/2010779416db28a8e31ac8ff857ed285197306b7))
- Feat(notify): Slack, Mattermost, Telegram, Pushover, Email channels ([039094d](https://github.com/sislelabs/kuso/commit/039094daaa693e68e49aeb08f6cbfc26787b73c1))
- Feat(github): apply kuso.yaml on push via GitHub Contents API ([1e80d0e](https://github.com/sislelabs/kuso/commit/1e80d0e7f78b512a9f275d37938f92a1389a9ea0))
- Feat(crd): KusoProject.spec.configAsCode.enabled ([e3ff9e6](https://github.com/sislelabs/kuso/commit/e3ff9e6fb7a052fc62d460d7e90ea318636c8041))
- Feat(web): config-as-code tab on the project view ([335fc17](https://github.com/sislelabs/kuso/commit/335fc178097cfc4d46bf020559e501f75f1ebaf6))
- Feat(cli): kuso apply and kuso project export ([a99f9d6](https://github.com/sislelabs/kuso/commit/a99f9d66e9ee494be92e8d1fbc8ad7dda0baef8b))
- Feat(http): GET /api/projects/{p}/spec export; wire crons into reconciler ([4fa233b](https://github.com/sislelabs/kuso/commit/4fa233b5a30b5313111a7d197c3e0ee9ef50ecad))
- Feat(spec): export live project state to kuso.yaml ([d71f42b](https://github.com/sislelabs/kuso/commit/d71f42b0f066643dda3a176adde37c9fba507acc))
- Feat(spec): reconciler applies full field set + crons ([21706ff](https://github.com/sislelabs/kuso/commit/21706ff75cd74a980ab547352dcbf091256e2431))
- Feat(spec): cron request mapping helpers for apply ([448a6cd](https://github.com/sislelabs/kuso/commit/448a6cd5d51a3412bc6f655438634916139ff342))
- Feat(spec): plan diffs crons and gates deletes behind prune ([4858c1f](https://github.com/sislelabs/kuso/commit/4858c1f439bc177d1fd5d973bb0c934545276705))
- Feat(spec): full-parity kuso.yaml schema (apiVersion, prune, crons) ([1d5a2bf](https://github.com/sislelabs/kuso/commit/1d5a2bf45ffa03983f1da2729e96650b2ce8d6a0))

### 🐛 Bug Fixes
- Fix(builds): worker-runtime services no longer 500 on build trigger ([5c062dc](https://github.com/sislelabs/kuso/commit/5c062dc117b34adebc2aa68275340604fdd1f77f))
- Fix(web): env-var diff no longer shows phantom changes for refs ([b603769](https://github.com/sislelabs/kuso/commit/b60376947e1e8043e65a416f885dd57d26fc3435))
- Fix(config-as-code): address code-review findings ([76e0421](https://github.com/sislelabs/kuso/commit/76e0421ef7d96212f5dd2019ddb41ede0e5527e9))
- Fix(cli): import help text references export-archive after rename ([ae71045](https://github.com/sislelabs/kuso/commit/ae71045ae85ea2fc63159b3e2552476def936e9c))

### 📝 Docs
- Docs: specs for TCP proxy, Postgres PITR, build vuln scanning ([af70854](https://github.com/sislelabs/kuso/commit/af70854d1951af113bfb92fb6139bb40ab92b58e))
- Docs: implementation plan for config-as-code (kuso.yaml) ([893e6aa](https://github.com/sislelabs/kuso/commit/893e6aaacc2538dc2053d1c6d45eea0a3fd155df))
- Docs: config-as-code spec — fetch kuso.yaml via GitHub Contents API ([1a2dadc](https://github.com/sislelabs/kuso/commit/1a2dadce674637e5d9ab799db8fceba526126a4e))
- Docs: spec for config-as-code (kuso.yaml) ([0e7b9a6](https://github.com/sislelabs/kuso/commit/0e7b9a66798f10f5ade4bb7401c0632ae68e37f5))

### 🔨 Refactors
- Refactor(github): parse kuso.yaml once in applyConfigFromRepo ([c00088d](https://github.com/sislelabs/kuso/commit/c00088ded25dab7bd0f4d7da7a50457370669c83))

## [0.13.12] — 2026-05-20

### ✨ Features
- Feat(kube): add ServiceSecretName and EnvSecretName helpers ([d278bfc](https://github.com/sislelabs/kuso/commit/d278bfc8d16a7977a17b52245a39fc442867016a))

### 🐛 Bug Fixes
- Fix(addons): keep per-service and per-env secrets in envFromSecrets fan-out ([40ff006](https://github.com/sislelabs/kuso/commit/40ff0066c6bc4968f372293b02220ea60874409d))

### 📝 Docs
- Docs: envFromSecrets per-service/per-env drop fix implementation plan ([649dcd1](https://github.com/sislelabs/kuso/commit/649dcd13662ae943c8f2da1fa30268a72dc0e900))
- Docs: envFromSecrets per-service/per-env drop fix design spec ([745eab3](https://github.com/sislelabs/kuso/commit/745eab333ab862d1da23066e6b281244deba87be))

### 🔨 Refactors
- Refactor(secrets): delegate Name to kube secret-name helpers ([e75b138](https://github.com/sislelabs/kuso/commit/e75b1382d8bfbf0f5b58a0ab59fd4a42cbcd6cd5))

### 🧪 Tests
- Test(addons): assert fan-out keeps per-service and per-env secrets ([d05a059](https://github.com/sislelabs/kuso/commit/d05a059bbf5c941f8f12becb9bcbbcf2ff6bb903))
- Test(kube): cover ServiceSecretName and EnvSecretName ([d1b99a5](https://github.com/sislelabs/kuso/commit/d1b99a5d3dcb98b13ceca932c99b63422f138dd8))

### 🧹 Chores
- Chore: archive older changelog entries ([fd726e2](https://github.com/sislelabs/kuso/commit/fd726e2261609f5a1d3399de055f208dd2059901))

## [0.13.11] — 2026-05-20

### ✨ Features
- Feat(kube): add SharedSecretNames helper for envFromSecrets ([9db9435](https://github.com/sislelabs/kuso/commit/9db9435dc2c0e026018bf3b6fc659dee1fdacfe3))

### 🐛 Bug Fixes
- Fix(addons): keep shared secrets in envFromSecrets fan-out ([f3adede](https://github.com/sislelabs/kuso/commit/f3adede997506cd3ac55e06e926e9a0f5e2d57e1))

### 📝 Docs
- Docs: envFromSecrets shared-secret drop fix implementation plan ([0131ed0](https://github.com/sislelabs/kuso/commit/0131ed0138a5538d475ac093c1b54bd9e8694b75))
- Docs: envFromSecrets shared-secret drop fix design spec ([bb5c281](https://github.com/sislelabs/kuso/commit/bb5c2810f8e148eaf25a27ed21db8b2dd90db8a8))

### 🔨 Refactors
- Refactor(github): build preview envFromSecrets via kube.SharedSecretNames ([a965724](https://github.com/sislelabs/kuso/commit/a965724bd956be827705956ed6139afe4ce802af))
- Refactor(projects): build envFromSecrets via kube.SharedSecretNames ([8998e4b](https://github.com/sislelabs/kuso/commit/8998e4bea6a8fa13e8d1873672aae565f3a42674))

### 🧪 Tests
- Test(addons): assert fan-out keeps shared secrets in envFromSecrets ([e9a7f71](https://github.com/sislelabs/kuso/commit/e9a7f7143de7fda1efd29379995107fe150ed00c))
- Test(kube): cover SharedSecretNames ([ce372c2](https://github.com/sislelabs/kuso/commit/ce372c2c9b5f3524332eb1a6241adc6e86406519))

### 🧹 Chores
- Chore: archive older changelog entries ([3bb4bc4](https://github.com/sislelabs/kuso/commit/3bb4bc494eeb1dfd4fbca19054d98f6fe6fe7a43))

## [0.13.10] — 2026-05-20

### ✨ Features
- Feat(cli): add --private-egress flag to service set ([3f06e03](https://github.com/sislelabs/kuso/commit/3f06e032c7dfa6733d40acba1cf3ad8cb24e1edb))
- Feat(crd): add privateEgress to KusoService and KusoEnvironment schemas ([9cf6728](https://github.com/sislelabs/kuso/commit/9cf6728995c5bb85e38d6a2f8f7e3c227ea6bb46))
- Feat(chart): stamp public-egress pod label unless privateEgress set ([7810988](https://github.com/sislelabs/kuso/commit/78109885d166540247ae39cac93f698b86ad9eb6))
- Feat(services): wire PrivateEgress through PatchService and env creation ([d8bb14f](https://github.com/sislelabs/kuso/commit/d8bb14ff1a3bc6614fbc905b5fb258230936ac98))
- Feat(propagate): mirror PrivateEgress onto env CRs ([550fe67](https://github.com/sislelabs/kuso/commit/550fe676964a4e0b662dc6fd7da8f45df54c44f3))
- Feat(types): add PrivateEgress to KusoService and KusoEnvironment specs ([56ccb55](https://github.com/sislelabs/kuso/commit/56ccb55e802360c88ac85b653f39bc299932866c))

### 📝 Docs
- Docs: public-egress fix implementation plan ([f3080bc](https://github.com/sislelabs/kuso/commit/f3080bc29b0a76d49448259991b1208fb93849a6))
- Docs: public-egress fix design spec ([3ca1b02](https://github.com/sislelabs/kuso/commit/3ca1b02c881a17cb9e708ffe45741e2d3bf46211))

### 🧪 Tests
- Test(chart): cover public-egress label in kusoenvironment render ([c973ee9](https://github.com/sislelabs/kuso/commit/c973ee9b5e29f25f3f00d0ca0d26f27c7d69b905))
- Test(propagate): cover PrivateEgress service→env propagation ([11bd6f5](https://github.com/sislelabs/kuso/commit/11bd6f5a04461039e96a48d349358e2d875bd1f7))

### 🧹 Chores
- Chore: archive older changelog entries ([207f7ca](https://github.com/sislelabs/kuso/commit/207f7cafb400139be5090c61caa267a9cbb78c41))
- Chore: gitignore .worktrees/ ([a28d4ed](https://github.com/sislelabs/kuso/commit/a28d4edaf413ea400544e999ece446d3591aa269))

## [0.13.9] — 2026-05-20

### 🐛 Bug Fixes
- Fix(chart): always emit POOLER_* conn keys, empty when disabled ([659e4d1](https://github.com/sislelabs/kuso/commit/659e4d1e1c179ab18cc87c4674ebaaa4c52a3c13))

## [0.13.8] — 2026-05-20

### 🐛 Bug Fixes
- Fix(chart): pooler must use scram-sha-256, not md5 ([d31f1c6](https://github.com/sislelabs/kuso/commit/d31f1c69bd56779264472e9fa604b8c7c32ce434))

## [0.13.7] — 2026-05-20

### ✨ Features
- Feat(web): pooler toggle in addon configuration settings ([83dda5a](https://github.com/sislelabs/kuso/commit/83dda5a8fbce714914827b53b4301bcfe9d9d8ee))
- Feat(web): add pooler to KusoAddonSpec and UpdateAddonBody types ([4e6ed4a](https://github.com/sislelabs/kuso/commit/4e6ed4aff78456f57476aafca8ff4ce8556dc32f))
- Feat(http): wire pooler field through addon create/update handlers ([4572f52](https://github.com/sislelabs/kuso/commit/4572f52e434110a608aeee7391c744a1b3c777df))
- Feat(addons): apply spec.pooler in Add and Update ([cef0493](https://github.com/sislelabs/kuso/commit/cef04936ff2eb25561eb8825ae240e0cb1384ef7))
- Feat(apiv1): add pooler field to addon create/update requests ([6d6d5cc](https://github.com/sislelabs/kuso/commit/6d6d5ccf9bd4b8b0ab1eb65aa4a64d03a5ac85c3))
- Feat: add KusoAddonPooler to KusoAddonSpec ([7c51be7](https://github.com/sislelabs/kuso/commit/7c51be7a14790861cfd55accb20415d20f9706b9))
- Feat(chart): CNPG Pooler + POOLER_* conn keys for HA postgres addon ([3db1db2](https://github.com/sislelabs/kuso/commit/3db1db230d15616a6b972636b65e38c490ebc662))
- Feat(chart): single-node PgBouncer template for postgres addon ([4f109c7](https://github.com/sislelabs/kuso/commit/4f109c7b1d6a015c68885e6a6ea23c96a4fd3e85))
- Feat(chart): add pooler.enabled default to kusoaddon values ([c8cf76d](https://github.com/sislelabs/kuso/commit/c8cf76d1c6da53a6a743eab153960a38872f42f2))
- Feat(crd): add spec.pooler.enabled to KusoAddon ([d7c585f](https://github.com/sislelabs/kuso/commit/d7c585fe1050ada4817d72fa710a21b681c9f4ec))
- Feat(postgres): default kuso-postgres to 1 instance, HA opt-in ([32131d6](https://github.com/sislelabs/kuso/commit/32131d655f100c925925a9dd083b890a5f065ebc))

### 🐛 Bug Fixes
- Fix(chart): address pooler code-review findings ([91af22b](https://github.com/sislelabs/kuso/commit/91af22be28b75695c27e5184000679f3fb5c2182))

### 📝 Docs
- Docs: implementation plan for opt-in addon PgBouncer ([f5f0725](https://github.com/sislelabs/kuso/commit/f5f0725ff90cd35190ad7e99776cd74d025c95eb))
- Docs: spec for opt-in PgBouncer on addon Postgres ([3713bdb](https://github.com/sislelabs/kuso/commit/3713bdb378a9cc67d8b6fbfd32451f53ba3d0370))

### 🧪 Tests
- Test: refresh kusoaddons CRD schema golden for spec.pooler ([36a8004](https://github.com/sislelabs/kuso/commit/36a8004e4fd9b1b348078aa65c88ea9c9f2a9f7a))

## [0.13.6] — 2026-05-18

### ✨ Features
- Feat: run log viewer, outbox health badge, per-project usage rollup ([f368fcc](https://github.com/sislelabs/kuso/commit/f368fccf83a9c80bc01eacac5dc0e8b0d337759c))

### 📝 Docs
- Docs: AGENT_SMOKE_TEST.md — standardised end-to-end protocol ([8ff43ce](https://github.com/sislelabs/kuso/commit/8ff43ce865acffabf141efffb43e7556fd7720ec))

## [0.13.5] — 2026-05-18

### 🐛 Bug Fixes
- Fix(networkpolicy): allow traefik + cert-manager from their own namespaces ([55d91f8](https://github.com/sislelabs/kuso/commit/55d91f87bf992dcc471f651f42e03339d8c281b3))

## [0.13.4] — 2026-05-18

### 🐛 Bug Fixes
- Fix(networkpolicy): build pods can reach buildkit + github ([b0d858c](https://github.com/sislelabs/kuso/commit/b0d858c6bb606010f278a94870b844643b8dfc1a))

## [0.13.3] — 2026-05-18

### 🐛 Bug Fixes
- Fix(notify): outbox SQL UPDATEs go through *Tx, not *sql.Tx ([d442800](https://github.com/sislelabs/kuso/commit/d442800bf82db397e5686213319360b07c63d3ec))

## [0.13.2] — 2026-05-18

### 🐛 Bug Fixes
- Fix(runs): live-smoke regressions before v0.13.1 went out ([5ac0a5f](https://github.com/sislelabs/kuso/commit/5ac0a5f32b95d705991d870bb6e80f38594d3919))
- Fix(pgbouncer): make userlist world-readable so the pooler can auth ([1dc1855](https://github.com/sislelabs/kuso/commit/1dc18557f1afd1123df5a3e0f9f15ca66efb244d))

## [0.13.1] — 2026-05-18

### 🐛 Bug Fixes
- Fix: post-review P0/P1 cleanup before live ship ([a11f455](https://github.com/sislelabs/kuso/commit/a11f4552792e55fe832f10121debc4c2b45104d7))

## [0.13.0] — 2026-05-18

### Other
- Ux: settings grouping, rollback chip, deployments split, changelog cap ([81ae3ae](https://github.com/sislelabs/kuso/commit/81ae3ae322e9bff3bafbe6225ae82a3336574555))

### ⚡ Performance
- Perf(kube): node informer for nodewatch + nodemetrics ([2a6eac8](https://github.com/sislelabs/kuso/commit/2a6eac891f9b3f7347bf005ae267b5a82199fe0b))

### ✨ Features
- Feat(runs): UI Runs tab in ServiceOverlay ([b2ae0bd](https://github.com/sislelabs/kuso/commit/b2ae0bd607e983d95ea48fc3ca5007cc3ef4c78f))
- Feat(runs): phase-write poller + MCP run tool ([0d9c865](https://github.com/sislelabs/kuso/commit/0d9c8651e1c4759bbe9554b15e9ecc1f3284dc65))
- Feat(runs): KusoRun CR for one-shot task pods ([d4393ee](https://github.com/sislelabs/kuso/commit/d4393eef2baf0f7ed35e456340e4ad4033d68fec))
- Feat(security): default-on NetworkPolicy for project namespaces ([7d40ccb](https://github.com/sislelabs/kuso/commit/7d40ccb6ae14b137fb4d4985c18561ae8288537a))
- Feat(builds): dry-run mode — compile without push or promote ([528efbe](https://github.com/sislelabs/kuso/commit/528efbec84737c1d9dd1d630081b6bd0dd979ca0))
- Feat(usage): cluster cost rollup page + /api/usage ([494496d](https://github.com/sislelabs/kuso/commit/494496d7f0e57b557aec86b617f31c8122236004))
- Feat(mcp): plan verb — dry-run apply for agents ([d6e1920](https://github.com/sislelabs/kuso/commit/d6e192048d642f4ad8a03a41051937b7d7893c07))
- Feat(db): opt-in daily partitioning for LogLine ([7d0ec16](https://github.com/sislelabs/kuso/commit/7d0ec16be695301855fee1574934ad51f87e38b7))
- Feat(notify): durable webhook delivery via NotificationOutbox ([d8be13d](https://github.com/sislelabs/kuso/commit/d8be13deb414461736ac2524f9321d46a70bf65e))
- Feat(deploy): PgBouncer transaction pooler in front of CNPG ([a96fb25](https://github.com/sislelabs/kuso/commit/a96fb25690d28834227f5366ba888ea26196e36d))
- Feat(instancepg): periodic health probe surfaces unhealthy phase ([4641a0e](https://github.com/sislelabs/kuso/commit/4641a0e159cea1265f1f5d308a3d5959620b81aa))

### 🐛 Bug Fixes
- Fix(ui): visibility toggle knob no longer overruns the label ([16944ba](https://github.com/sislelabs/kuso/commit/16944babd7640d0fe54d78e499268108560bc04b))
- Fix(sec): scope kuso-server secrets per-ns + instancepg ssl + leader gate ([a70c226](https://github.com/sislelabs/kuso/commit/a70c22619f119691c8d590bbe41a09e74f4f16e3))

### 📝 Docs
- Docs(work-plan): mark Phase 6 status + per-feature deferrals ([d2aebb2](https://github.com/sislelabs/kuso/commit/d2aebb2755113c5013c0c044429125ccc9cfcad0))

### 🔨 Refactors
- Refactor: split builds.go + projects.Service interface seam ([7843de5](https://github.com/sislelabs/kuso/commit/7843de5818d0b7ebf61600c9c7f1d0f7fa700173))

### 🧹 Chores
- Chore(previewdb): document instance-pg clone gap ([b37c785](https://github.com/sislelabs/kuso/commit/b37c78519a17b8fb7b89c552ca6246cedded123b))
- Chore(docs): drop internal review + planning docs ([2fae280](https://github.com/sislelabs/kuso/commit/2fae280d2384eca31e78a4ca61b6dfc988829bbf))

## [0.12.1] — 2026-05-18

### ✨ Features
- Feat(cli): kuso instance-pg subcommands; fix: NotFound is not an error ([a0d3de7](https://github.com/sislelabs/kuso/commit/a0d3de761ed23250b49e01b1444d7408378d5046))

## [0.12.0] — 2026-05-18

### ✨ Features
- Feat(ui): /settings/database — first-class cluster Postgres page ([8c792ac](https://github.com/sislelabs/kuso/commit/8c792ac6e948b5f179314aa21830d24e795f29ce))
- Feat(instance-pg): cluster-shared Postgres as a first-class service ([e288dee](https://github.com/sislelabs/kuso/commit/e288dee94e5ff6b2e4714bac51ff08bb6423b776))

## [0.11.6] — 2026-05-16

### 🐛 Bug Fixes
- Fix(notify, ui): synthetic-ref + Site link + bell-feed clear ([85101d7](https://github.com/sislelabs/kuso/commit/85101d72b76a4a27d04ae2990190fe4aeb22f9be))
- Fix(secrets): detect shadow conflicts at write time ([d35981b](https://github.com/sislelabs/kuso/commit/d35981b6805b8d9a333c7450878d3a0d798db5d9))
- Fix(release.sh): auto-recover from tag wedge + publish GH release ([26b454f](https://github.com/sislelabs/kuso/commit/26b454f4e8cff61623b8c887333f196c0c1dda5e))

## [0.11.5] — 2026-05-16

### 🐛 Bug Fixes
- Fix(projects): card click reliably nav into kuso ([bc18c68](https://github.com/sislelabs/kuso/commit/bc18c68ccd0d796bd1a459534b80f56fac116957))

## [0.11.4] — 2026-05-15

### ✨ Features
- Feat(notify): rich Discord cards across build/pod/node events ([f5200aa](https://github.com/sislelabs/kuso/commit/f5200aa0ca5ed7488deb8753ba6a693b328d0378))

### 🐛 Bug Fixes
- Fix(logs): coalesce continuation lines so multi-line tracebacks stay readable ([c1db65e](https://github.com/sislelabs/kuso/commit/c1db65ed033791efce9f9723e089b971cb5b3518))
- Fix(shared-secret): roll dependent envs on set/unset, surface count in CLI ([6e2dee6](https://github.com/sislelabs/kuso/commit/6e2dee6c425221688a3f98b5bab92c414d3ad43c))
- Fix(varrefs): ${{ svc.URL }} resolves to the actual kube Service name ([bc2273f](https://github.com/sislelabs/kuso/commit/bc2273fc67b7f222d083f42a51d4640a17fd4a08))

## [0.11.3] — 2026-05-15

### ✨ Features
- Feat(projects): per-card CPU/RAM rollup + rewired card links ([66509be](https://github.com/sislelabs/kuso/commit/66509be319eeb950193f4d7d353e5e4220483eea))

### 🐛 Bug Fixes
- Fix(notify): absolutify embed URLs so Discord stops 400ing ([9ecbe9b](https://github.com/sislelabs/kuso/commit/9ecbe9b77fe158fdfa1d83505fb6dab7ae5fa639))
- Fix(release.sh): use ghcr token-dance in visibility precheck ([0f343e5](https://github.com/sislelabs/kuso/commit/0f343e5d6144100f5e93c21e6bb699fb90f2103a))

## [0.11.2] — 2026-05-15

### 🐛 Bug Fixes
- Fix(crd): reconcile spec.* drift between Go structs and CRD yamls ([0ff34f4](https://github.com/sislelabs/kuso/commit/0ff34f4c59f8de0c9c2c3cf69d4ac69ddce81f7d))
- Fix(rbac): grant kuso-server access to kusoes CRD ([a9ec2a8](https://github.com/sislelabs/kuso/commit/a9ec2a84e7ad97d4e6668f2b54f0f50875c2a20a))
- Fix(rbac): grant kuso-server configmaps:patch/delete + crd:get/create/update/patch ([238239f](https://github.com/sislelabs/kuso/commit/238239fdbc77d142998a95613d75bdc31c86909d))
- Fix(release.sh): fail loudly when a release's images aren't public on ghcr ([b7418b7](https://github.com/sislelabs/kuso/commit/b7418b78a7927afb08a996ac378de82ef2d785d0))

## [0.11.1] — 2026-05-15

### 🐛 Bug Fixes
- Fix: propagate Runtime service→env; draw auto-injected addon edges on canvas ([d5a9d09](https://github.com/sislelabs/kuso/commit/d5a9d09b496637f2449840a5489f1c891620aaaa))
- Fix(release.sh): real breaking-change detection + hard-fail on tag-push collision ([84b21de](https://github.com/sislelabs/kuso/commit/84b21de934cdbe2acfb95a902f6d24e7053c6f06))

## [0.11.0] — 2026-05-15

### Other
- Fix+test(crons): reject Quartz ? + @-macros that kube CronJob can't parse (P1-6) ([964e52a](https://github.com/sislelabs/kuso/commit/964e52abaae28e35d80f8f9b793698a122dc6047))
- Ux(welcome): two real paths forward on non-admin GitHub-blocked state (UX P0-A) ([9a516d8](https://github.com/sislelabs/kuso/commit/9a516d8b5f2ecbe1be4408c0789973680cecf91a))
- Ux(services/new): deploy-from-image source mode (UX P0-B) ([a3dc105](https://github.com/sislelabs/kuso/commit/a3dc1051e3c433c372b6487b71d4ae957096cd36))
- Ux(deployments): surface build failure cause inline + banner (UX P0-C) ([dcf8c44](https://github.com/sislelabs/kuso/commit/dcf8c444f8dcfd92dbab3ed32a63c31b74283f99))

### ✨ Features
- Feat(cli): surface build error message in `kuso build list` ([c8ae68d](https://github.com/sislelabs/kuso/commit/c8ae68db87cf18e8eccd51d62b12967479ba00b8))

### 🐛 Bug Fixes
- Fix(crd): add spec.internal to kusoservices schema ([38f5dd1](https://github.com/sislelabs/kuso/commit/38f5dd10a7c187fcbab7a4ee278dee8eeffb5c04))
- Fix(sec): drop pods/portforward from kuso-server RBAC + document the split (Sec F-07 partial) ([89ff1ff](https://github.com/sislelabs/kuso/commit/89ff1ff825d3819151dafcf06b817d1819adb3fd))
- Fix(sec): validate static/buildpacks/image refs against shell injection (Sec F-03) ([01340ba](https://github.com/sislelabs/kuso/commit/01340bac56231230dc49ebafa1d09b7b36ec2e82))
- Fix(buildcontroller): stamp app.kubernetes.io/instance on build pods ([30e7294](https://github.com/sislelabs/kuso/commit/30e7294e2e5b751e01249c17f39e0029b99f6e34))
- Fix(sec): buildcontroller refuses unmanaged-namespace KusoBuild CRs (Sec F-02) ([67a67bb](https://github.com/sislelabs/kuso/commit/67a67bb572c9f4853332dfad45878ebd09bae180))
- Fix(buildcontroller,buildreaper): single-shot handler registration (Correct P0-3) ([fe4ea2e](https://github.com/sislelabs/kuso/commit/fe4ea2e0b04959dda47204275485bfc874a0c76f))
- Fix(kube): RMW retry on env/addon/cron updates closes lost-write on 409 (Correct P0-2) ([fe990ff](https://github.com/sislelabs/kuso/commit/fe990ff937dcb836ab9c7f076d74ba7cb6eb6ed4))
- Fix(install): restore managed-by label on the home namespace ([4cbe00d](https://github.com/sislelabs/kuso/commit/4cbe00d26b95d42a7c350478a48b6e6608451a46))
- Fix(rbac): grant kuso-server serviceaccounts:create for in-process buildcontroller ([20b5f58](https://github.com/sislelabs/kuso/commit/20b5f58d69e14929d650fc79fda46ca97346a128))
- Fix(install): chmod 0444 on admin password file so non-root kuso user can read ([74a8b9b](https://github.com/sislelabs/kuso/commit/74a8b9b577c0a5289651931356f83798b8d25789))

### 📝 Docs
- Docs: pass-4 review reports (post-v0.10.0) ([678452b](https://github.com/sislelabs/kuso/commit/678452b342849a3a6741a69da0537d44968543f4))

### 🔨 Refactors
- Refactor: drop more back-compat paths (HMAC auth, prisma-int64, env aliases) ([f74b59a](https://github.com/sislelabs/kuso/commit/f74b59aa3a4fc8f7fbfadc8916e5aaeacc6fef5f))
- Refactor: drop back-compat paths obsoleted by v0.10 + single-tenant policy ([5075b03](https://github.com/sislelabs/kuso/commit/5075b03c38eaeee64c797ccfb0a611969818f1ed))
- Refactor(db): introduce per-domain views over *DB (Arch P0-2 wedge) ([e8d4c7e](https://github.com/sislelabs/kuso/commit/e8d4c7e9f391f6ea25ed04a4d9fa081baa6c1777))
- Refactor(projects): extract service→env propagation to propagate.go (P0-1 partial) ([1b65756](https://github.com/sislelabs/kuso/commit/1b657567656733110a3b3a06cf0c90c19aa5fe30))
- Refactor(kube): owner-ref env + cron CRs to parent service (Arch P1-2) ([2974a35](https://github.com/sislelabs/kuso/commit/2974a35aff53d9f5f59e94dcc0eb57dc4fbfdb68))
- Refactor(kube): route list-by-labels through informer cache (Arch P1-1) ([73ae542](https://github.com/sislelabs/kuso/commit/73ae54217bf286515a7ca12a5fd8c8269ea5f4fe))
- Refactor(builds): extract pure helpers to refs.go (Arch P0-3 partial) ([ab7a53c](https://github.com/sislelabs/kuso/commit/ab7a53cd883db3cd5f555e3c01908cea3b7055e2))

### 🧪 Tests
- Test(buildcontroller): leader gate + running-map dedup + instance-label pin (P1) ([80c4e22](https://github.com/sislelabs/kuso/commit/80c4e222f178a201829c30b6b62a8644a6d1c2d9))
- Test(migration): cover import paths + extract interfaces (P1-5/P1-12) ([a909a26](https://github.com/sislelabs/kuso/commit/a909a26ef5b8d8534a7f238fe1757be972922d07))

## [0.10.0] — 2026-05-13

### Other
- Build(server): lay out workspace modules in Docker build context ([80567cc](https://github.com/sislelabs/kuso/commit/80567cc5d7d9a01383724445f86f4486449d7101))
- Build(server): bump go builder image to 1.26 (matches go.mod requirement) ([8ee5a98](https://github.com/sislelabs/kuso/commit/8ee5a983c711cd33c252c60b4e9036bb8b64c387))
- Ux(addon): dirty tracking + unified SaveBar in AddonOverlay (UX P0) ([b01f92b](https://github.com/sislelabs/kuso/commit/b01f92bbcfc14b5b0de74b872a7410a03fee93c8))
- Ux(project): honour ?tab= query param when opening overlay (UX P0) ([3bbd28f](https://github.com/sislelabs/kuso/commit/3bbd28f0d1e3c888e417b43788de568bee5b5492))
- Ux(P2 polish): SaveBar swallows promise rejections, /welcome handles non-admin GH (P2 batch) ([0d807ee](https://github.com/sislelabs/kuso/commit/0d807eed62cfa048ec00ee2a5ab99d10902cbdfe))
- Ux(overlay): scroll active tab into view on change, not every render (U-P1-I) ([2107563](https://github.com/sislelabs/kuso/commit/2107563e17d4f2995b3578a04e69c2118a75d599))
- Ux(overlay): surface saveError inline on the unified SaveBar (U-P1-H) ([ae6ac66](https://github.com/sislelabs/kuso/commit/ae6ac66aa4f0bb5f095bcb3e408dccea08658deb))
- Ux(service-logs): replace 'live tail' with honest 10s-poll copy (U-P1-G) ([ce5d3c4](https://github.com/sislelabs/kuso/commit/ce5d3c4921aa25c9908a89859c176c4a474d3d21))
- Ux(projects/new): wire restoreFormDraft for post-login restore (U-P1-F) ([fdc3f0d](https://github.com/sislelabs/kuso/commit/fdc3f0de6cceed4f716bf1787baaaa359b045c1d))
- Ux(logs): add scroll-pause indicator + jump-to-live affordance (U-P1-E) ([93e8266](https://github.com/sislelabs/kuso/commit/93e8266a32b9bdb67d1bde0a13d71554c6d215a2))
- Ux(welcome): rescue Step 2 dead end with explicit CTAs (U-P1-D) ([cb4201f](https://github.com/sislelabs/kuso/commit/cb4201f7c2292231487660e48bc4cdfd78c11f4f))
- A11y(projects): add health aria-label to project-card live counter (U-P1-C) ([a517a25](https://github.com/sislelabs/kuso/commit/a517a252a7e946e2a6376b5f99becac7a6e8089f))
- Ux(settings): show Activity tile for audit:read, not settings:admin (U-P1-B) ([bc605fa](https://github.com/sislelabs/kuso/commit/bc605fa0b91e4af678640e2e1a0781c900ea8b7c))
- Ux(variables): wire EnvVarsEditor save/discard into overlay SaveBar (U-P0-D) ([8325b52](https://github.com/sislelabs/kuso/commit/8325b52ef5a17cd26a8988cb99986fa97f869d29))
- Ux(projects): break /welcome redirect loop via sessionStorage memo (U-P0-C) ([318afc9](https://github.com/sislelabs/kuso/commit/318afc9651c88add1ad920419810a7b3ae3dd87b))
- Ux(welcome): add Step 3 deploy CTA, stop dumping users on empty canvas (U-P0-A) ([e3a8c20](https://github.com/sislelabs/kuso/commit/e3a8c20bcbe8729ca9b0358640e1cd43dee26f67))

### ✨ Features
- Feat(buildcontroller): render KusoBuild → Job in-process, retire helm path (D-01) ([be66c12](https://github.com/sislelabs/kuso/commit/be66c12fd45266479992edb007833b300cde0687))
- Feat(buildreaper): watch KusoBuild done-transitions, reap helm secrets (D-01 partial) ([0d102c1](https://github.com/sislelabs/kuso/commit/0d102c1ea3232e67a0afdcf1b4e1c7fa2e9cbbde))
- Feat(coolify-import): build commit endpoint + wizard commit step (U-P0-B) ([2f55270](https://github.com/sislelabs/kuso/commit/2f5527045f7392b197ddc641484bfbc44f3de17d))
- Feat(web): guided onboarding wizard at /welcome (U-P0-2) ([f8c71f1](https://github.com/sislelabs/kuso/commit/f8c71f19791c81af9972700b1c557337124d4132))
- Feat(import): Coolify import preview endpoint + wizard skeleton (U-P0-1) ([2bb386f](https://github.com/sislelabs/kuso/commit/2bb386feffdf23864887d191e2cd79e134540f1d))
- Feat(web): audit log UI under /settings/activity (U-P0-3) ([2b67f2f](https://github.com/sislelabs/kuso/commit/2b67f2fb6ed09539c2242f56c19c0e2e36b97469))

### 🐛 Bug Fixes
- Fix(coolify-import): conditional https:// prefix on GitRepository (Correct F-17) ([78b67fe](https://github.com/sislelabs/kuso/commit/78b67fefdfdf381a1a530c89f1b75afcec356eb2))
- Fix(projects): delta-ops retry on helm-operator conflict (Correct F-03) ([7b6cb4e](https://github.com/sislelabs/kuso/commit/7b6cb4ec697646fb22a863d133ae11c80771ac32))
- Fix(notify): wrap INSERT+cap-prune in one transaction (Correct F-01) ([8179a6d](https://github.com/sislelabs/kuso/commit/8179a6d6779b926e088b2f1bc8112053f6a1ef88))
- Fix(sec): NetworkPolicy gates BuildKit on kuso-managed namespace label (Sec P0-3) ([3cc6c57](https://github.com/sislelabs/kuso/commit/3cc6c57053ad5c3db2c24edd9d6964ea3a90af84))
- Fix(sec): honour X-Forwarded-* in invite URL only from trusted proxies (Sec P1-2) ([06d4909](https://github.com/sislelabs/kuso/commit/06d49098a7c4f2371894b2684329bb2df1e4e2d0))
- Fix(sec): validate repo.path against shell-injection (Sec P1-1) ([d7918f6](https://github.com/sislelabs/kuso/commit/d7918f65c63a42035fe6c2d17dd1338ad87d9862))
- Fix(sec): default KUSO_REQUIRE_SIGNATURES=true in install.sh (Sec P0-2) ([fab7cc8](https://github.com/sislelabs/kuso/commit/fab7cc8afa0d4b06529da5b4a89fa66f6320ad85))
- Fix(sec): gate POST /api/system/update on system:update perm (Sec P0-1) ([8491247](https://github.com/sislelabs/kuso/commit/849124701da5ba84f307f29d031b82a3268bc0e9))
- Fix(server): schema-drift surfaces via readyz, refuses writes (A-P0-3) ([3782c75](https://github.com/sislelabs/kuso/commit/3782c7585c47309ab9a1df647d65e243f12de0a9))
- Fix(security): generic Coolify error response (S-P2-1) ([e92493f](https://github.com/sislelabs/kuso/commit/e92493fff0ec6c4af557e0c1049da2090b8abb2c))
- Fix(security): label-selector concat sites → kube.LabelSelector (S-P1-3) ([445a6ec](https://github.com/sislelabs/kuso/commit/445a6ecfa94891edd55bc798fe25baaef42c7d8d))
- Fix(updater): honour requireSignatures on ErrUnsignedNoKey (S-P1-1) ([3d0cfb6](https://github.com/sislelabs/kuso/commit/3d0cfb66967cff2b365838023482112cacc2c4cd))
- Fix(security): SSRF guard on Coolify importer + shared httpx (S-P1-2) ([25dc58d](https://github.com/sislelabs/kuso/commit/25dc58da274413a2806cfdd7429e064a37965a8e))
- Fix(db): cap project IN-clause length in ListNotificationEventsForProjects (B8) ([7f14d2a](https://github.com/sislelabs/kuso/commit/7f14d2abe62503fff1dbc378008c28fccd064c19))
- Fix(handlers): pass ResponseWriter to MaxBytesReader (B7 from followup) ([8bb54ae](https://github.com/sislelabs/kuso/commit/8bb54ae9479613a0c085a79002848697136715b7))
- Fix(coolify): plumb ctx through client + cap response body (B4, B5) ([d7f3617](https://github.com/sislelabs/kuso/commit/d7f3617c2f580fffac87d7e55c67aad38786d754))
- Fix(projects): hoist KusoProject fetch out of per-env loop (B3 from followup) ([ce43778](https://github.com/sislelabs/kuso/commit/ce437786983eff348336d53de9b5d4014e2d6932))
- Fix(projects): hold per-service mutex in SetEnvWithOpts (B2 from followup) ([da0796a](https://github.com/sislelabs/kuso/commit/da0796a9976f2852147d432c06c9327a7c74c14e))
- Fix(backup): defer cancel() in cleanup path (B1 from followup review) ([f4dcf89](https://github.com/sislelabs/kuso/commit/f4dcf89cd4716fe1e635921dd6147e0c3c2c1280))
- Fix(build): bake env-detect image with rg+jq preinstalled (S-P2-3) ([763e4d9](https://github.com/sislelabs/kuso/commit/763e4d9a3f3bccffc17ec9f79a0e4d81fbfb3979))
- Fix(projects): single chokepoint for service→env propagation (A-P0-3) ([19d744b](https://github.com/sislelabs/kuso/commit/19d744b97a1634452ccbaead3192944fd30e9b6d))
- Fix(web): unified overlay SaveBar via useOverlayDirty (U-P1-1) ([fc8c3c2](https://github.com/sislelabs/kuso/commit/fc8c3c24a86649be8826501b1a71872032ad7fcd))
- Fix(web): CommandPalette indexes env vars + service actions (U-P2-8) ([3fc0310](https://github.com/sislelabs/kuso/commit/3fc0310e63fbe07e2e1cc0aa0181e976624f1517))
- Fix(web): EnvironmentSwitcher to Popover+Command primitive (U-P2-1) ([3142746](https://github.com/sislelabs/kuso/commit/3142746069badf0b9f75167935a2ab79983a79d7))
- Fix(web): kebab affordance on canvas nodes (U-P2-7) ([d086507](https://github.com/sislelabs/kuso/commit/d086507acd76620d19bdf04f58e0748b3eb2b03f))
- Fix(web): scope mobile interstitial to settings pages (U-P2-3) ([156002c](https://github.com/sislelabs/kuso/commit/156002c801a7ef137524a563080eebbdc2210c0a))
- Fix(web): split overlay drift chip into state pill + diagnostic (U-P2-9) ([fa8e559](https://github.com/sislelabs/kuso/commit/fa8e559213adc639580224b37530f1ac631012a0))
- Fix(web): explain "fresh addons" inline on non-prod banner (U-P2-2) ([ea734c3](https://github.com/sislelabs/kuso/commit/ea734c34c0a4d1205a3c7c94c845e9dcc4808fd9))
- Fix(web): pin Settings tab on service overlay (U-P1-2) ([f6909b0](https://github.com/sislelabs/kuso/commit/f6909b0ff5e65b7c71a35389405898b8ba29492b))
- Fix(notify): non-admin notifications bell with project-scoped feed (U-P1-4) ([36e754c](https://github.com/sislelabs/kuso/commit/36e754cf228191071c11b1eb874b4ef32f06cf1b))
- Fix(web): locked settings cards explain how to unlock (U-P1-7) ([e2f6012](https://github.com/sislelabs/kuso/commit/e2f60128e4e303a8e2356c2defafd7d9596b53d9))
- Fix(web): per-project Settings cog in TopNav (U-P1-8) ([64fbaa5](https://github.com/sislelabs/kuso/commit/64fbaa553c322757e0e2887a4c36b707ffe9bb10))
- Fix(web): hover-revealed service URL on project cards (U-P1-10) ([3f4bf80](https://github.com/sislelabs/kuso/commit/3f4bf801b3edf8a727114765c465606022d98209))
- Fix(web): partial-down + zero-live colors on project cards (U-P1-5) ([f9d8599](https://github.com/sislelabs/kuso/commit/f9d859972d16a12fba2df95fbb507a7fab0fb23e))
- Fix(web): Add addon CTA on empty project (U-P1-3) ([e10b490](https://github.com/sislelabs/kuso/commit/e10b490e736affdf3c0e1ef7108a7c85e2a60368))
- Fix(web): surface previews toggle on /projects/new (U-P1-6) ([7fd1de0](https://github.com/sislelabs/kuso/commit/7fd1de0dc2ea632a7d2e152f1f1868b763240380))
- Fix(web): description field on /projects/new (U-P2-4) ([5496f9d](https://github.com/sislelabs/kuso/commit/5496f9dd0cd59bd4a5b3b2305341b191d247b112))
- Fix(web): replace <service> placeholder in /projects/new URL preview (U-P2-5) ([b7de4a9](https://github.com/sislelabs/kuso/commit/b7de4a9684de41e515c2895e9daa5c1799bcf832))
- Fix(web): gate Updates settings card on SettingsAdmin (U-P2-6) ([d819163](https://github.com/sislelabs/kuso/commit/d8191630b581ac23c90ab508af0cefedae3f62bb))
- Fix(review): release signing, persistent rate limit, schema check + 5 more ([d15f720](https://github.com/sislelabs/kuso/commit/d15f720bdf4d45bef877b46460738ecb293ef794))
- Fix(review): architecture extracts + 8 more P1/P2 findings ([f3e852b](https://github.com/sislelabs/kuso/commit/f3e852b5bc6aa64f13b377ba9c96ee6ee7487c0b))
- Fix(review): land P0 review findings + 6 P1/P2 fixes ([6620e66](https://github.com/sislelabs/kuso/commit/6620e66451d85786f83ebceea69ff84ccb5be5ec))
- Fix(addon-sql): stop wide tables from blowing out the overlay ([9dacc6f](https://github.com/sislelabs/kuso/commit/9dacc6f5c3c016e6d7fd5a7ba3de8f683ab4066c))

### 📝 Docs
- Docs(auth): correct misleading fail-open comment on RevocationChecker (Sec P1-4) ([9f4fc8b](https://github.com/sislelabs/kuso/commit/9f4fc8b9994c87a9076ba3f064d8ab0f87be624d))
- Docs: pass-3 review reports ([780d9e4](https://github.com/sislelabs/kuso/commit/780d9e45546a487196b5c2514acb7536323160f7))
- Docs: schema-migration recipe + release.sh nudge (A-P1-5) ([d30554c](https://github.com/sislelabs/kuso/commit/d30554ca59141d2451072c8416c95254a3f6b0b6))
- Docs: P2 security polish — env-detect tag note + ListInstallations scope (P2 batch) ([8862b09](https://github.com/sislelabs/kuso/commit/8862b09b52f5e8500c9cca7f4ff8cef5e05b40d9))

### 🔨 Refactors
- Refactor(api): fill out apiv1 with service + addon + env DTOs (A-01) ([b09ff98](https://github.com/sislelabs/kuso/commit/b09ff987b3fdcffd5eec6d2756874abd4e825b33))
- Refactor(import): extract internal/migration service from handler (B-01) ([11bbdde](https://github.com/sislelabs/kuso/commit/11bbddeca744319896512d816d83f51299f4cc99))
- Refactor(coolify): promote mapping helpers to shared module (A-04) ([e097cd6](https://github.com/sislelabs/kuso/commit/e097cd6223ab9cbf2a414e8ddb5a32cdcdb5cce6))
- Refactor(architecture P2): rename nodes→nodeshape, sweep stale propagator refs (P2 batch) ([ad49e33](https://github.com/sislelabs/kuso/commit/ad49e330486a25c52dca163f3fb4601182679d31))
- Refactor(server): handlers decode POST/PATCH projects via apiv1 (A-P1-4) ([1c3c526](https://github.com/sislelabs/kuso/commit/1c3c526614eed587d94387839dad5a06c44ff8f0))
- Refactor(coolify): extract to shared workspace module (A-P0-1 from followup) ([4e710e5](https://github.com/sislelabs/kuso/commit/4e710e5204d3d96d08e1454d24d82722eb8b0c96))
- Refactor(projects): delete unused facades.go (A-P1-3 from followup) ([9423b54](https://github.com/sislelabs/kuso/commit/9423b5419f8c72c14f646ab3e3881bf22f023775))
- Refactor(projects): delete invalidateDescribe shim (A-P1-2 from followup) ([a00e3c2](https://github.com/sislelabs/kuso/commit/a00e3c28daf0ac2730e85a58a7becd2b5eda3793))
- Refactor(projects): delete dead per-field propagators (B6 from followup) ([a3aab80](https://github.com/sislelabs/kuso/commit/a3aab80b4c898af8db49ab06d0ab2a3f518d0690))
- Refactor(projects): introduce ProjectAPI / ServiceAPI / EnvironmentAPI facades (A-P1-1) ([eb50117](https://github.com/sislelabs/kuso/commit/eb50117e46a86070d0a59fe788be52d2c9e4efd5))
- Refactor(api): extract shared apiv1 DTO module (A-P1-3) ([472c2cd](https://github.com/sislelabs/kuso/commit/472c2cd2f09ca88cf98802e70cb9729bbcc4ac44))
- Refactor(http): extract mountAuthenticatedRoutes out of NewRouter (A-P1-2) ([d6b25cc](https://github.com/sislelabs/kuso/commit/d6b25cc79420a81ab53dbc09503d43b6f2212dba))
- Refactor(web): move shared-secrets API calls into features/ (A-P1-8) ([05737ce](https://github.com/sislelabs/kuso/commit/05737ce7cb9cfbef74c32bea1ea579a2220c9e7a))

## [0.9.79] — 2026-05-11

### Other
- Ux(service-settings): drop "test access" button + style Internal-only as toggle ([20679e3](https://github.com/sislelabs/kuso/commit/20679e3ef2ab0a374bca3443e76d44cd108f1681))

## [0.9.78] — 2026-05-11

### ✨ Features
- Feat: security hardening + project export/import + CLI parity ([dd2bb08](https://github.com/sislelabs/kuso/commit/dd2bb0860a04774e009b92f323d4a1fc82896d1f))

### 🐛 Bug Fixes
- Fix(updater): graceful default for unsigned-no-key state ([b5d54cc](https://github.com/sislelabs/kuso/commit/b5d54cc38bc58d5350e7bc1e2ae774eb615591b0))

## [0.9.77] — 2026-05-11

### Other
- Ux: trim user dropdown + alpha-sort env vars ([872c825](https://github.com/sislelabs/kuso/commit/872c8254f390c9c9fb0e253c7d7f58e5553acd3e))

## [0.9.76] — 2026-05-11

### Other
- Ux(settings): tighter Source + Build rows ([3301a20](https://github.com/sislelabs/kuso/commit/3301a2076b348f8244aedaf4d3a5c2e1446ae3cf))

## [0.9.75] — 2026-05-11

### ✨ Features
- Feat(github): one-click "Sign in with GitHub" provisioning for services ([d13cb5b](https://github.com/sislelabs/kuso/commit/d13cb5b8b7a75da3bf68991c1e81b2600c5f6812))

### 🐛 Bug Fixes
- Fix(builds): poll every 5s, not 30s — BuildKit warm-cache builds completed in <30s so the poller's first tick saw 'succeeded' without ever observing 'running'. UI showed PENDING all the way through despite logs streaming. ([de32a74](https://github.com/sislelabs/kuso/commit/de32a74418ff29d5b91436f55ede8ff1b569dd83))

## [0.9.74] — 2026-05-11

### Other
- Ux(canvas): pulse on pending/queued builds too, not just running ([db5dc7a](https://github.com/sislelabs/kuso/commit/db5dc7a85b94fb0e64bbc076f121d8f9b458d03a))

## [0.9.73] — 2026-05-11

### Other
- Build: long-lived shared BuildKit daemon — Coolify-style architecture ([a060ef7](https://github.com/sislelabs/kuso/commit/a060ef7f005a6dd002d1748d728f330dd5076e30))
- Build: swap kaniko for moby/buildkit:rootless ([fdc8f70](https://github.com/sislelabs/kuso/commit/fdc8f70a13e09a21b257c9fd5a8e9ba76c6c0878))

### 🐛 Bug Fixes
- Fix(kusobuild): buildkit cache ref must be tag-based (repo:buildcache) — kaniko used a /<repo>/.cache path suffix which buildkit's registry exporter rejects as 'invalid reference format' ([0726a4c](https://github.com/sislelabs/kuso/commit/0726a4caf424ce41a1c94eda0f2e761d336a5884))
- Fix(kusobuild): hardcode buildkitd host — values.yaml didn't define .buildkitd subtree and helm fails on nil-pointer access in templates. The service name is invariant across kuso installs anyway. ([505f24c](https://github.com/sislelabs/kuso/commit/505f24c62e1179900967fed6e0cbf6553f4f6ce4))
- Fix(buildkitd): readiness probe must use --addr tcp:// — buildctl defaults to unix socket which doesn't exist when daemon is TCP-only ([3e6668a](https://github.com/sislelabs/kuso/commit/3e6668ad11a9cdd8917942294478b27975f62c6c))
- Fix(kusobuild/buildkit): allowPrivilegeEscalation=true + SETFCAP/SETPCAP — rootlesskit's newuidmap is setuid, needs both to install the inner-userns UID map. Without these buildkitd never starts ('newuidmap: Could not set caps'). ([a3ca62a](https://github.com/sislelabs/kuso/commit/a3ca62a282eb38e9835cbb761b446727ad51ca07))
- Fix(kusoenvironment): drop pod-level runAsNonRoot — rejects any image with a named USER (nextjs, node, nginx, etc.). Container-level cap drops kept; that's the real blast-radius reduction. ([318fd76](https://github.com/sislelabs/kuso/commit/318fd76b18f4e87e10e59f7f9678e7a7cc61111f))
- Fix(kaniko): grant CHOWN/DAC_OVERRIDE/FOWNER/SETUID/SETGID — drop ALL killed every build at base-image rootfs unpack ('chown /etc/gshadow: operation not permitted'). allowPrivilegeEscalation:false kept so no setuid escape; kaniko's own user-ns sandbox contains the rest. ([7dc8b50](https://github.com/sislelabs/kuso/commit/7dc8b50b69188c7041e341a8392f238d73d2e9ed))
- Fix(kusobuild): pod-level fsGroup only, no runAsUser cascade — env-detect needs root to apk add ripgrep/jq, cache-init needs UID 1000, kaniko needs root. Previous pod-level runAsUser:1000 broke env-detect (exit 99 from apk add EACCES on /etc/apk). ([81faffb](https://github.com/sislelabs/kuso/commit/81faffbefa1033a6a135dc32f71771897a1ff400))
- Fix(kusobuild): drop the root-only chmod in cache-init; fsGroup already grants write on the dirs we mkdir. Non-owner chmod against the PVC mount point exited non-zero and killed every build. ([98f515e](https://github.com/sislelabs/kuso/commit/98f515e9fe586cefc2ed07be1c9569ed7bc15d7b))
- Fix(kusobuild): pod-level fsGroup+UID so cache PVC mounts writable by cache-init (UID 1000). Previous runs created the PVC under root via kaniko's UID 0, and the next run as UID 1000 couldn't mkdir under /cache. ([0b7f5b8](https://github.com/sislelabs/kuso/commit/0b7f5b804582ea6f4b50af7622a4c87b0d2f8ccc))
- Fix(s3 addon): pin HOME=/tmp + emptyDir for mc client config — running as non-root UID 1001 defaulted HOME to /, mkdir /.mc EACCES. ([07496b8](https://github.com/sislelabs/kuso/commit/07496b889775c1ec1dbf910d682e1809250ba454))
- Fix(addons): pod-security UID/GID for each addon kind so runAsNonRoot ([335ac24](https://github.com/sislelabs/kuso/commit/335ac24ccdde523e55c2257c0b7767db4474f902))
- Fix(operator): drop legacy Kind=Kuso watch — entire reconcile loop ([853a43f](https://github.com/sislelabs/kuso/commit/853a43f8788f7aba6f87ba5bb13015d4bfcb7966))
- Fix(deploy): pin operator to v0.9.59 + bump memory limit to 512Mi ([01f5470](https://github.com/sislelabs/kuso/commit/01f54706b9acc29cc7cbc90f02c0c338dd291784))
- Fix(install.sh): pin operator default to v0.9.59 (last actually-built tag); v0.9.60 release.sh decided 'operator unchanged' and pinned release.json to .59. The default here was stale and caused a fresh install to ImagePullBackOff on the operator deployment. ([454a591](https://github.com/sislelabs/kuso/commit/454a591eaeb787935c74269cce9dd395e1ec8dbb))
- Fix(postgres-dsn-stamp): add RBAC for services + drop dead -app rule ([49e8544](https://github.com/sislelabs/kuso/commit/49e854479e566f0adf4fde34b09ae0713ccb1885))
- Fix(postgres-dsn-stamp): read from kuso-postgres-conn, not -app ([857c167](https://github.com/sislelabs/kuso/commit/857c167eb7ff486e7273036a7b0a109f740961a9))
- Fix(deploy): use alpine/k8s:1.30.4 not rancher/kubectl:v1.30.4 ([485c08b](https://github.com/sislelabs/kuso/commit/485c08b3508c63eb1b157623a1f44c6a35d6ff86))
- Fix(deploy): replace bitnami/kubectl:1.30 (deprecated, pulled from ([5040a67](https://github.com/sislelabs/kuso/commit/5040a6758a6ee7efb44e2f4f10bd684b92ec0d0b))
- Fix(logs): build pod logs work via REST tail + add --build CLI flag ([eaaaae8](https://github.com/sislelabs/kuso/commit/eaaaae8931395ec72885f9e138d9f26e5ea82847))

## [0.9.60] — 2026-05-10

### 🐛 Bug Fixes
- Fix: apply scoped to project + CLI image-runtime + skill accuracy ([8e7d418](https://github.com/sislelabs/kuso/commit/8e7d4185a820bca287ccf9e5d9b7cf2c720448a9))

## [0.9.59] — 2026-05-10

### Other
- Skill: drop-in kuso skill for Claude Code ([4735c52](https://github.com/sislelabs/kuso/commit/4735c52517b8137c7da4fe1332d7317183a243e1))
- Hardening pass: pod-security, RBAC, CSRF, multi-ns logs, mobile incident ([bb1386f](https://github.com/sislelabs/kuso/commit/bb1386fe1a4354faa6ab2e52043fbe1a478b602b))

### 📝 Docs
- Docs: surface "set your app's URL env var" hint on custom-domains ([90c6d5f](https://github.com/sislelabs/kuso/commit/90c6d5f1903c6ec94ecb91c70a844ad5245ce038))

## [0.9.58] — 2026-05-08

### 🐛 Bug Fixes
- Fix two regressions ([595caec](https://github.com/sislelabs/kuso/commit/595caecc71611a722c68453dcb165274fbed57fb))

## [0.9.57] — 2026-05-08

### Other
- Build: lighter kaniko + capture OOMKilled in failure reason ([fc6e587](https://github.com/sislelabs/kuso/commit/fc6e58729bb177a69607b86102d845c9dc0c42a0))

## [0.9.56] — 2026-05-08

### Other
- Ux: drop redeploy split-button + widen settings rows ([71559a8](https://github.com/sislelabs/kuso/commit/71559a881db905e1ab78430d301d21e48521df18))

## [0.9.55] — 2026-05-08

### Other
- Gh: smarter, more resilient build-trigger flow ([a366108](https://github.com/sislelabs/kuso/commit/a3661088d909cbc897035c7a4bd2add19ee4e2be))

## [0.9.54] — 2026-05-08

### Other
- Ux: deep review pass — 20 fixes across surface ([6ba8cbf](https://github.com/sislelabs/kuso/commit/6ba8cbf19094732e5bebf9de2eab2dd1d2dc0016))

## [0.9.53] — 2026-05-08

### 👷 CI
- Ci: register react-hooks plugin + fix 22 lint errors ([e403395](https://github.com/sislelabs/kuso/commit/e40339555eb80d6b8039ece743d4c44fa12f2352))

## [0.9.52] — 2026-05-08

### 🐛 Bug Fixes
- Fix(env-switcher): always set ?env=<name>, even for production ([b855bdd](https://github.com/sislelabs/kuso/commit/b855bdd34e8d19bc78ef982edf6f0574cfc33f65))

## [0.9.51] — 2026-05-08

### 🐛 Bug Fixes
- Fix(env-switcher): rows are real <a href> links ([4709b67](https://github.com/sislelabs/kuso/commit/4709b674578446891b986cf4f39705ebac673e8c))

## [0.9.50] — 2026-05-08

### 🐛 Bug Fixes
- Fix(env-switcher): roll-our-own dropdown, both directions work ([a0a8df3](https://github.com/sislelabs/kuso/commit/a0a8df35e52ac54e12a948a61c535814be5c742f))

## [0.9.49] — 2026-05-08

### 🐛 Bug Fixes
- Fix(ui): drop discard-changes prompt + plain-button env switcher ([c4246e9](https://github.com/sislelabs/kuso/commit/c4246e967ee783e3040aea2ae26be11b6eaea79f))

## [0.9.48] — 2026-05-08

### 🐛 Bug Fixes
- Fix(envs): clone service envVars too; switcher click; pulse on running build ([0701d84](https://github.com/sislelabs/kuso/commit/0701d84e45538a492d7662ee40a9c73d4b13160a))

## [0.9.47] — 2026-05-08

### 🐛 Bug Fixes
- Fix(envs): cloned env CR uses -production suffix, not bare service name ([410161e](https://github.com/sislelabs/kuso/commit/410161e32c5f4605f7fbc52363706982755e1f38))

## [0.9.46] — 2026-05-07

### 🐛 Bug Fixes
- Fix(envs): clone sibling URLs + image, dedupe env CR name + host ([7568864](https://github.com/sislelabs/kuso/commit/7568864ad6cfb976d312b95065c7d2d4d7e8363a))

## [0.9.45] — 2026-05-07

### ✨ Features
- Feat(envs): non-prod banner + per-env branch input + previews copy ([c0d9912](https://github.com/sislelabs/kuso/commit/c0d9912ab820f239322a583a9f3402e33bbce229))

## [0.9.43] — 2026-05-07

### ✨ Features
- Feat(envs): project-level environment groups with addon mirroring ([efafe68](https://github.com/sislelabs/kuso/commit/efafe6839ca8c40a21a2fe9b3ad1946dd579c4e5))
- Feat(settings/github): rich App status with installations + repo coverage ([7aaa5b3](https://github.com/sislelabs/kuso/commit/7aaa5b39c7b43822d16157600ebe3e55aa355bb9))

## [0.9.42] — 2026-05-07

### 🐛 Bug Fixes
- Fix(canvas): status border honors prod pod, not pending/queued builds ([3d4f34c](https://github.com/sislelabs/kuso/commit/3d4f34c372686bae84d582c4ed5d646a20b6be14))

## [0.9.41] — 2026-05-07

### 🐛 Bug Fixes
- Fix(deep-review-5): close 22 scalability + UX findings from second deep review ([ad7ffff](https://github.com/sislelabs/kuso/commit/ad7ffffe25c327d94f872ed0f7a01b7cd15b3d33))

## [0.9.40] — 2026-05-07

### ✨ Features
- Feat(notifications): clear-all button in the bell dropdown ([8208913](https://github.com/sislelabs/kuso/commit/820891342fdc92b595f015f6c31b34cbc83a7532))

### 🐛 Bug Fixes
- Fix(metrics-scrape): rotate kuso-server too; clamp Prometheus secret mount ([762184d](https://github.com/sislelabs/kuso/commit/762184d9cfbc4d5595e2243f70122d706e1f6cd9))
- Fix(deep-review-4): close trailing items from the deep-review batch ([28c1ea5](https://github.com/sislelabs/kuso/commit/28c1ea5d5d344a65fc92d02e7632c58172941ada))
- Fix(deep-review-3): close audit findings from post-batch review ([ebb4b0d](https://github.com/sislelabs/kuso/commit/ebb4b0d920b79d21375e935357c84ed8878600fd))
- Fix(deep-review-2): CNPG default, real backup/restore, token invalidation, settings UX ([4a6a5ef](https://github.com/sislelabs/kuso/commit/4a6a5efc2c97f785f0d0988a10c194bfeea6cf82))
- Fix(deep-review): close ~30 audit findings across data, kube, security, UX ([b92cd20](https://github.com/sislelabs/kuso/commit/b92cd207fa81cd9402be554e50c3a16403b1df7f))
- Fix(addons): unbreak helm parse — \${{ }} in comments tripped Go templates ([e45eb61](https://github.com/sislelabs/kuso/commit/e45eb61a48bf176d826a47f67595f5991c9e904f))

### 🧪 Tests
- Test(kube): refresh CRD golden snapshots for v0.9.39 ([7098b46](https://github.com/sislelabs/kuso/commit/7098b467639a2356091f2e92dcc2289817fdb244))

## [0.9.39] — 2026-05-07

### ✨ Features
- Feat(addons): ship Mailpit + NATS + MeiliSearch + ClickHouse ([a040e58](https://github.com/sislelabs/kuso/commit/a040e584fefd452208e8a3018472200384d0542b))

## [0.9.37] — 2026-05-07

### ✨ Features
- Feat(ha): kuso-server runs on worker nodes via kuso-k3s-token Secret ([2f793a6](https://github.com/sislelabs/kuso/commit/2f793a68e7262b98fa777002da59a04bc37fbec4))

### 📝 Docs
- Docs: reposition kuso as multi-node, Postgres-backed, HA-capable ([6a8a44b](https://github.com/sislelabs/kuso/commit/6a8a44b4ad2a2e0205a34fb13e2470fa39f61de1))

## [0.9.36] — 2026-05-07

### 🐛 Bug Fixes
- Fix(builds): chart-level no-op gate stops resurrection of done builds ([255961a](https://github.com/sislelabs/kuso/commit/255961a5be6636058d3da56fdb0e9335386b5de2))

## [0.9.35] — 2026-05-07

### 🐛 Bug Fixes
- Fix(logs): copy-friendly build logs ([94534ed](https://github.com/sislelabs/kuso/commit/94534ed3372d0d62d6ac6d6f56981c33b5242caf))

## [0.9.34] — 2026-05-07

### 🐛 Bug Fixes
- Fix(builds): tighten pending→running chip latency ([6f6c4f8](https://github.com/sislelabs/kuso/commit/6f6c4f80f4d20969510d2af7af32e5e9c77e50f3))

## [0.9.33] — 2026-05-07

### 🐛 Bug Fixes
- Fix(builds): GitHub installation falls back to service-level ([29f841b](https://github.com/sislelabs/kuso/commit/29f841bdf508ce5bfd8041c336c3ab713f4350ec))

## [0.9.32] — 2026-05-07

### 🐛 Bug Fixes
- Fix(env-dialog): branch field references the picked service's repo ([a3e8653](https://github.com/sislelabs/kuso/commit/a3e865335c9c8fec76671f4f993b18a0cf874e57))

## [0.9.31] — 2026-05-07

### 🐛 Bug Fixes
- Fix(settings/builds): preset cards show real headroom math ([70581ac](https://github.com/sislelabs/kuso/commit/70581ac275066fb452c64b51724977deef496823))

## [0.9.30] — 2026-05-07

### 🐛 Bug Fixes
- Fix(builds): queue at cap-hit instead of returning 409 ([148c9f8](https://github.com/sislelabs/kuso/commit/148c9f8dc7cd36f4062b493de87edc978c39a3f3))

## [0.9.29] — 2026-05-07

### 🐛 Bug Fixes
- Fix(builds): orphaned cancelled-build pods don't block the cap ([c491067](https://github.com/sislelabs/kuso/commit/c49106721b9ee2a3f5dd2192ea26a7e82efb3e8e))

## [0.9.28] — 2026-05-07

### 🐛 Bug Fixes
- Fix(logs): swallow transient "container is waiting" lines while build pod boots ([399cbab](https://github.com/sislelabs/kuso/commit/399cbab48ca089303ac8d508c98f3f06078df944))

## [0.9.27] — 2026-05-07

### 🐛 Bug Fixes
- Fix(drift): flag rolloutPending during helm-operator reconcile lag ([7502832](https://github.com/sislelabs/kuso/commit/750283243eed46414c6c971e10ff015af12ffdc9))

## [0.9.26] — 2026-05-07

### ✨ Features
- Feat(chart): zero-downtime env rollouts with readinessProbe + maxSurge=1 ([81f9b4b](https://github.com/sislelabs/kuso/commit/81f9b4b4b3c007a87c7eb533ae52231b8ed27cdc))

### 🐛 Bug Fixes
- Fix(drift-banner): rolling-out beats out-of-date; honest copy ([b5a5f3d](https://github.com/sislelabs/kuso/commit/b5a5f3de5d164889f01012f1ccb49008b2d3e505))

## [0.9.25] — 2026-05-07

### 🐛 Bug Fixes
- Fix(env-editor): banner only fires from local save, never from server-side lastRolloutAt ([0133194](https://github.com/sislelabs/kuso/commit/01331940150b25bb054d8ff1edc1dc052427bae4))

## [0.9.24] — 2026-05-07

### 🐛 Bug Fixes
- Fix(drift): only emit lastRolloutAt when there's a recent actual edit ([550716e](https://github.com/sislelabs/kuso/commit/550716ee94579ad5ac8728ee0ee510883fbfda60))

## [0.9.23] — 2026-05-07

### 🐛 Bug Fixes
- Fix(drift): banner survives refresh + ACTIVE chip flips on build success ([2085aaf](https://github.com/sislelabs/kuso/commit/2085aaf0872d0ca0c49c4654f9826d3d5e3ae3aa))

## [0.9.22] — 2026-05-07

### 🐛 Bug Fixes
- Fix(drift): pod-creation-time vs last-spec-edit makes badge survive refresh ([c396670](https://github.com/sislelabs/kuso/commit/c396670fc28b70de77cf3926e69c62058e5ab8f5))

## [0.9.21] — 2026-05-07

### 🐛 Bug Fixes
- Fix(env-editor): sticky 60s banner after save so users see rollout signal ([fd4cee8](https://github.com/sislelabs/kuso/commit/fd4cee8ad2a3101482ca6952531f149e73e3729f))

## [0.9.20] — 2026-05-07

### 🐛 Bug Fixes
- Fix(drift): compare env CR spec against running pod, not deployment template ([6382433](https://github.com/sislelabs/kuso/commit/63824330a237909e93a715606a98cfae2f267c33))

## [0.9.19] — 2026-05-07

### 🐛 Bug Fixes
- Fix(drift): derive RolloutPending from Deployment, not env-CR observedGeneration ([185f1d1](https://github.com/sislelabs/kuso/commit/185f1d1f60c3bf724bc2b70ade1baaa86bdfcba4))

## [0.9.18] — 2026-05-07

### 🐛 Bug Fixes
- Fix(logs/builds): cancel survives operator restart, friendlier pod-not-found ([d5c741c](https://github.com/sislelabs/kuso/commit/d5c741c76fa412e221ba73f4c0d83e8dcf152896))

## [0.9.17] — 2026-05-07

### 🐛 Bug Fixes
- Fix(http): make statusRecorder a Hijacker so WS upgrades work ([d0cd35f](https://github.com/sislelabs/kuso/commit/d0cd35f2c039cd01409595651ed095bf5181ed47))

## [0.9.16] — 2026-05-07

### 🐛 Bug Fixes
- Fix(logs): clean WS close + suppress reconnect after end-of-stream ([398b44b](https://github.com/sislelabs/kuso/commit/398b44b87dd2090acfa8cddebd4859da6ededf51))

## [0.9.14] — 2026-05-07

### 🐛 Bug Fixes
- Fix(drift): correct env label key (env, not env-kind) ([acf0552](https://github.com/sislelabs/kuso/commit/acf0552a54ff9b11122c8f8c399c6ebb5fdb125f))

## [0.9.13] — 2026-05-07

### 🐛 Bug Fixes
- Fix: tab padding, settings-row hint overlap, drift label-selector miss ([43c5846](https://github.com/sislelabs/kuso/commit/43c5846b860783eb028da3ebba7096254d7d8163))

## [0.9.12] — 2026-05-07

### 🐛 Bug Fixes
- Fix: env editor focus loss, build-log "connection lost", drift invalidate-on-save ([99ec110](https://github.com/sislelabs/kuso/commit/99ec11004616e9fe1b9e8066a54567f566f0e834))

## [0.9.11] — 2026-05-07

### ✨ Features
- Feat(drift): compare env CR ↔ live deployment, surface "out of date" ([563800a](https://github.com/sislelabs/kuso/commit/563800a81adef89eae73f2c7748ebe479ba10dff))

### 🐛 Bug Fixes
- Fix(kusoproject): quota.enabled default → false ([e8cf2e2](https://github.com/sislelabs/kuso/commit/e8cf2e2e23fe1fd07dbec469818665319c9a25bf))

## [0.9.10] — 2026-05-07

### 🐛 Bug Fixes
- Fix: NetworkPolicy podSelector cluster wipe + audit follow-ups ([108a5a5](https://github.com/sislelabs/kuso/commit/108a5a50e4f4b5b374bd351544ea8aa9a44210b6))

## [0.9.9] — 2026-05-06

### ✨ Features
- Feat: parallel-run sweep — leader election, ha addon, node bootstrap, metrics, network policies ([c6921b9](https://github.com/sislelabs/kuso/commit/c6921b97c37d6ac6d34045a845e0b8aa1508ab06))

### 🐛 Bug Fixes
- Fix: T1-E + T2-A + T2-C + T2-D + T2-F + T2-H + T2-K + T2-J + S-8 + log search bug + perf hardening ([f323140](https://github.com/sislelabs/kuso/commit/f323140b970b116b7fac5dcd753bae95e3cef08f))
- Fix: triage T1 (concurrent builds, RBAC gap, project cascade, addon retain) + perf hardening ([de74a24](https://github.com/sislelabs/kuso/commit/de74a24281d89e67d43e9513528f20f6471a34cd))
- Fix: NotificationsButton conditional-hook bug + stale audit comment ([4dc1de2](https://github.com/sislelabs/kuso/commit/4dc1de2337435fd714195a51fb74a6e1d52db38e))
- Fix(security): rework after audit re-review ([4b997a2](https://github.com/sislelabs/kuso/commit/4b997a233aef90c90648efe2e02c42873888c8d2))
- Fix(security): triage audit's P0 + P1 findings ([e41723b](https://github.com/sislelabs/kuso/commit/e41723bf2312c6ed6520d6b727d698b5778afb69))
- Fix(env): per-var edits now propagate to env CR + drift indicator ([dbe23ff](https://github.com/sislelabs/kuso/commit/dbe23ffc902df023c3a46c5a9c439e10666a4610))
- Fix(security): P0-1 BackupsHandler — gate all six methods ([bf0d72a](https://github.com/sislelabs/kuso/commit/bf0d72a5bf28b893d1e6d725d3a91b8c2bc20027))
- Fix(security): plug audit P0s — auth gates, redirect, signing, broken delete ([bc35380](https://github.com/sislelabs/kuso/commit/bc353808ff2f5f8a9247e40acc8bb860d2224af0))

## [0.9.8] — 2026-05-06

### ✨ Features
- Feat(env): auto-detect missing env vars at build + crash time ([169a845](https://github.com/sislelabs/kuso/commit/169a845b0a5b52a4e9fca27cc7645e364ef7910c))

## [0.9.7] — 2026-05-06

### 🐛 Bug Fixes
- Fix(domains): use Mozilla public-suffix list for FQDN check + skip stable nixpacks ship ([3655e78](https://github.com/sislelabs/kuso/commit/3655e780ffbaeddfee47286f67ffd8b5288f208c))

## [0.9.6] — 2026-05-06

### 🐛 Bug Fixes
- Fix(domains): reject non-public FQDNs at edit + filter from ingress TLS ([f6fbe68](https://github.com/sislelabs/kuso/commit/f6fbe6854d15d47f5af2714b1a33af5f8b92049b))

## [0.9.5] — 2026-05-06

### 🐛 Bug Fixes
- Fix(networking): auto-inject PORT, propagate baseDomain, audit cleanup ([552076b](https://github.com/sislelabs/kuso/commit/552076bb35e19089fb0d234b5f3617189619d33f))

## [0.9.4] — 2026-05-06

### 🐛 Bug Fixes
- Fix(projects): live-services count was inflated by desired>0 ([a901500](https://github.com/sislelabs/kuso/commit/a9015008be1c591d09ae94de1d5f4e241d2db682))

## [0.9.2] — 2026-05-06

### ✨ Features
- Feat(settings): admin-tunable build resources + concurrency cap ([39d1456](https://github.com/sislelabs/kuso/commit/39d14566387df43336d7b002fb9c99c45d1d9dbf))

### 🐛 Bug Fixes
- Fix(builds): bump kaniko memory limit 1Gi → 2Gi for nixpacks snapshots ([0257cc1](https://github.com/sislelabs/kuso/commit/0257cc1b4983eab8f9a240cd2101cc5c3ab8f6df))

## [0.9.1] — 2026-05-05

### 🐛 Bug Fixes
- Fix(install): kusocrons CRD + eager PriorityClass + pod-create race ([7f82f73](https://github.com/sislelabs/kuso/commit/7f82f7308fcaa9d226ac418563475fe21a61ab68))
- Fix(install): drop Secret block from postgres.yaml ([6abc8ea](https://github.com/sislelabs/kuso/commit/6abc8eae60c3e2d06c9d4e55a2b010bd051223fc))

## [0.9.0] — 2026-05-05

### Other
- Release sign + build node pool + CRD schema golden + parity check ([c129fbe](https://github.com/sislelabs/kuso/commit/c129fbe183de005ccaf94b17583d489f4ed0dff2))
- Hygiene: R4 R5 S6 S7 S10 from the v0.8.13 audit ([5c62204](https://github.com/sislelabs/kuso/commit/5c62204f72ffe9766e008607b47cb0cb19a30b6f))
- Deploy: postgres StatefulSet + RollingUpdate for kuso-server ([06b25a4](https://github.com/sislelabs/kuso/commit/06b25a411c443d1c85bbda77b547a81dfc480f31))
- Db: switch from SQLite to Postgres ([b391e0b](https://github.com/sislelabs/kuso/commit/b391e0bb74a2d52ebf90e5e590864993fe6b777b))

### ✨ Features
- Feat(web): Errors tab on the service overlay ([66b7848](https://github.com/sislelabs/kuso/commit/66b78480da89800d8f22cd2d6970391fad870ccd))
- Feat: Sentry-style error feed for deployed services ([82d51bb](https://github.com/sislelabs/kuso/commit/82d51bbb268071bd6892c0a9a6f78296bb598e4b))

## [0.8.13] — 2026-05-05

### Other
- Hardening(security+resilience): authz, ssrf, cancel-respawn, sig-prep ([13703c1](https://github.com/sislelabs/kuso/commit/13703c16710517e88a1dfa02b7d1a0bb77ffccf4))

## [0.8.12] — 2026-05-05

### 🐛 Bug Fixes
- Fix(builds): admission counts legacy-labelled pods + cap kaniko at 1Gi ([a0de3d7](https://github.com/sislelabs/kuso/commit/a0de3d7cf1f3cde04b684da1840aa72ac3631ea2))

## [0.8.11] — 2026-05-05

### 🐛 Bug Fixes
- Fix(canvas): age badge shows latest-build age, not env-CR age ([ff5e07f](https://github.com/sislelabs/kuso/commit/ff5e07fa18c71cc15821f1ab00dbf104543d8cd2))

## [0.8.10] — 2026-05-05

### Other
- Resilience(builds+platform): cluster-truth admission + auto-harden ([e349d73](https://github.com/sislelabs/kuso/commit/e349d73d6b0a2241184914c36eab3231afccb6bd))

## [0.8.9] — 2026-05-05

### Other
- Hardening(builds): close TOCTOU, deflake retention, drop spoof surface ([b9df890](https://github.com/sislelabs/kuso/commit/b9df890f9e5982297cd5874bd0b2288d3cb732fb))

### ⚡ Performance
- Perf(builds): persistent /nix store + lang dep cache + baked nixpacks image ([1197d84](https://github.com/sislelabs/kuso/commit/1197d8453d4e2f3e49a8e1ab468440839e64fb71))

## [0.8.8] — 2026-05-05

### 🐛 Bug Fixes
- Fix(builds): gate queued-build kaniko Job on spec.image, not operator ([ca898b7](https://github.com/sislelabs/kuso/commit/ca898b70c30c9c72b24e264bc8db3bdc27bfff93))

## [0.8.7] — 2026-05-05

### ✨ Features
- Feat(builds): real queue + trigger context + pod-phase log frames ([b045a73](https://github.com/sislelabs/kuso/commit/b045a737e0fbc1000da1d214eb06c8a65b3905ed))

## [0.8.6] — 2026-05-05

### ✨ Features
- Feat(builds): coolify-style deployment lifecycle (cancel, dur, log archive) ([7f7a07c](https://github.com/sislelabs/kuso/commit/7f7a07c7073875b1e604ea843b37a04dcde35cd2))

## [0.8.5] — 2026-05-05

### 🐛 Bug Fixes
- Fix(cleanup): skip pods+jobs owned by KusoBuild CRDs ([e05c772](https://github.com/sislelabs/kuso/commit/e05c772da114835242c237494cb3624cc7b6d664))

## [0.8.4] — 2026-05-05

### ✨ Features
- Feat: kuso-server resilience pack + cluster cleanup endpoint ([1a3333f](https://github.com/sislelabs/kuso/commit/1a3333f16b2a72724a8bdfdbfe6bfab638a0b18a))

## [0.8.3] — 2026-05-05

### 🐛 Bug Fixes
- Fix(canvas+ui): cron side panel, neutral border, uptime fallback, ([6a9e912](https://github.com/sislelabs/kuso/commit/6a9e91226e8b78bfec5cf9871dd806f2f625b35a))

### 🧹 Chores
- Chore: post-review batch 2 — backup default, golden tests, delta ops, shared-addons doc ([cc7111f](https://github.com/sislelabs/kuso/commit/cc7111f3ed180b0d94e9a99c94bf3919ed059393))

## [0.8.1] — 2026-05-05

### ✨ Features
- Feat: mobile UX + cron edit overlay + CLI parity for v0.8 ([d264704](https://github.com/sislelabs/kuso/commit/d264704baec7aceb8ffede29744fe4c3ae00e958))
- Feat: cron canvas node + friendly schedule picker + MinIO addon ([b80665c](https://github.com/sislelabs/kuso/commit/b80665cd387f417846520f29db7ae6c5561e2df4))

### 🧹 Chores
- Chore: import other-agent changes — informer cache + docs + LE-prod default ([738bd74](https://github.com/sislelabs/kuso/commit/738bd74781140c26e59bf7e598b782ea6351bf8d))

## [0.8.0] — 2026-05-05

### ✨ Features
- Feat(services): internal-only toggle skips Ingress ([c2ec390](https://github.com/sislelabs/kuso/commit/c2ec3907816393031f238c78bd8b166675b6dc2a))

## [0.7.56] — 2026-05-05

### ✨ Features
- Feat: canvas custom-domain URL + project-level always-on toggle ([f226f6c](https://github.com/sislelabs/kuso/commit/f226f6c6e60daba9752366a93b813a108dc253c8))

## [0.7.55] — 2026-05-05

### ✨ Features
- Feat(addons): instance dropdown lists registered shared servers ([5274e10](https://github.com/sislelabs/kuso/commit/5274e10739beab5702faa7e838f1da888167aef6))

## [0.7.54] — 2026-05-05

### ✨ Features
- Feat(addons): backup schedule + retention via API + UI ([f054ee7](https://github.com/sislelabs/kuso/commit/f054ee7988e773fef4b7569afddcc4e291079be5))

## [0.7.53] — 2026-05-05

### 🐛 Bug Fixes
- Fix(s3-backup): restore reads conn from <release>-conn Secret ([d857ab3](https://github.com/sislelabs/kuso/commit/d857ab3b8f757f1fd17324e06770de488087752f))
- Fix(s3-backup): three bugs found while testing against a self-hosted S3 ([15582b2](https://github.com/sislelabs/kuso/commit/15582b2384283fa8ec56813408fa6b29f78d2868))

## [0.7.52] — 2026-05-05

### 🐛 Bug Fixes
- Fix(envvars): reserve PORT / HOSTNAME / KUBERNETES_* ([61aed3e](https://github.com/sislelabs/kuso/commit/61aed3ede3fa13a48e3c1646f8d39296ecfd47e2))

## [0.7.50] — 2026-05-05

### ✨ Features
- Feat(crd): allow KusoEnvironment.spec.additionalHosts ([49fa230](https://github.com/sislelabs/kuso/commit/49fa230bae94e13e77c4b845b258ad699b7eebe2))
- Feat(domains): end-to-end custom domain support ([570037c](https://github.com/sislelabs/kuso/commit/570037ce3985f2c49dc9211984efe72204f3c06f))

## [0.7.49] — 2026-05-05

### 🐛 Bug Fixes
- Fix(settings): persistent save errors + add/remove domains list ([a720c35](https://github.com/sislelabs/kuso/commit/a720c350355db6f553b4a7e8131b8025cf7fd83b))

## [0.7.48] — 2026-05-05

### Other
- Ui(buttons): lighten primary CTA in light mode ([71efc44](https://github.com/sislelabs/kuso/commit/71efc44002ec50a27308b204a9304e8b50a11db2))

## [0.7.47] — 2026-05-05

### 🐛 Bug Fixes
- Fix(invite): generalize parsePathname so /invite/<token> resolves ([d646187](https://github.com/sislelabs/kuso/commit/d646187dec1c1849db265848f05179f94c044816))

## [0.7.46] — 2026-05-05

### 🐛 Bug Fixes
- Fix(invite): read token from URL pathname, not Next's build placeholder ([dbf716b](https://github.com/sislelabs/kuso/commit/dbf716b3ae284ea75c1ca8798269f6935b97bcb7))

## [0.7.45] — 2026-05-04

### 🐛 Bug Fixes
- Fix(envvars): keep PORT in the ref picker ([6234990](https://github.com/sislelabs/kuso/commit/6234990699c532b87780bd37cf970a79a4150181))

## [0.7.44] — 2026-05-04

### ✨ Features
- Feat: service display name + slim env-ref picker ([2cf631b](https://github.com/sislelabs/kuso/commit/2cf631b4fcce999309d2c5d93c758daab84f2bea))

## [0.7.43] — 2026-05-04

### ✨ Features
- Feat(canvas): drag-to-connect + PUBLIC_URL/PUBLIC_HOST refs ([25332ed](https://github.com/sislelabs/kuso/commit/25332ed1828735bdd9f4808e5ece9358353a5d8e))

## [0.7.42] — 2026-05-04

### ✨ Features
- Feat(updates): add manual "Check for updates" button ([4ee5653](https://github.com/sislelabs/kuso/commit/4ee56539098f5c34234d73f2c1cc7b2c5fec1107))

## [0.7.41] — 2026-05-04

### 🐛 Bug Fixes
- Fix(projects): propagate service-level port + envVars to environments ([cd594bb](https://github.com/sislelabs/kuso/commit/cd594bb1f48ffe7ef294f6fc9a897ea729796bd5))

## [0.7.40] — 2026-05-04

### ✨ Features
- Feat(builds): supersede in-flight builds when a new one starts ([7e03acf](https://github.com/sislelabs/kuso/commit/7e03acf442713328ddd49ae5d07354e5822c9339))

## [0.7.39] — 2026-05-04

### 🔨 Refactors
- Refactor: split monoliths + extract db helpers ([eed3d98](https://github.com/sislelabs/kuso/commit/eed3d981ffbe385637a050bcfa769f873a6195d2))

## [0.7.37] — 2026-05-04

### Other
- Ui(canvas): uptime in header (bigger), build line in footer next to replicas ([3e5c702](https://github.com/sislelabs/kuso/commit/3e5c702eea7aa7808e81ff44d9b41828be148197))

## [0.7.36] — 2026-05-04

### Other
- Ui: notification click-through, service node build line, square inputs ([ff253e7](https://github.com/sislelabs/kuso/commit/ff253e75236a5dd92f5fd9a2c9ed49b7b05191ca))

### ✨ Features
- Feat(builds): multi-language toolchain detection + nixpacks default ([9c7d5ea](https://github.com/sislelabs/kuso/commit/9c7d5eae4cb0706aff1c9e231240b21196c53806))

### 🐛 Bug Fixes
- Fix(builds): bake GOTOOLCHAIN into Dockerfile via sed (kaniko ignores --env at runtime) ([e473b28](https://github.com/sislelabs/kuso/commit/e473b281e76d7f5f46357d0ffae5c3ed7d12efe9))

## [0.7.34] — 2026-05-04

### 🐛 Bug Fixes
- Fix(builds): pin GOTOOLCHAIN to detected go.mod version ([187cb54](https://github.com/sislelabs/kuso/commit/187cb548504178069da323cf4e3aaf56b1decc3c))

## [0.7.33] — 2026-05-04

### Other
- Canvas: fold latest-build state into service-node color ([581ddd5](https://github.com/sislelabs/kuso/commit/581ddd5e4061bbfec4464c290525df906c0613b7))

## [0.7.32] — 2026-05-04

### 🐛 Bug Fixes
- Fix(canvas): zero-replicas wins over stale phase=building (showed yellow on failed services) ([d1449f2](https://github.com/sislelabs/kuso/commit/d1449f2c0556ee206e21868ed8636a2196968c9d))

## [0.7.31] — 2026-05-04

### Other
- Ui+build: failed canvas state, building yellow, input bg, themed toaster, nixpacks Go ([37fb249](https://github.com/sislelabs/kuso/commit/37fb249f24a822e41b897c9de72192ad12dad571))

## [0.7.30] — 2026-05-04

### Other
- Ui: default Button variant is now sage (positive action) ([f0a523e](https://github.com/sislelabs/kuso/commit/f0a523e132f31b0483d0712c2421456681f4538b))

## [0.7.29] — 2026-05-04

### Other
- Ui: drop ACTIVE pill, replicas color by load, add uptime + addon info ([bc9477a](https://github.com/sislelabs/kuso/commit/bc9477a52ff37c8f279682d5e467cbe947ee560f))

## [0.7.28] — 2026-05-04

### Other
- Ui: orange (#EB6534) is now the universal accent ([f6c65c0](https://github.com/sislelabs/kuso/commit/f6c65c0c4723c5b9ebe5f2ba9fb7272892014226))

## [0.7.27] — 2026-05-04

### Other
- Ui: warm near-black ground (#131200) + slate/sage accents + orange action ([af43acd](https://github.com/sislelabs/kuso/commit/af43acd372c57cc4d422e743a9bb28144326376e))

## [0.7.26] — 2026-05-04

### Other
- Ui: midnight-navy (#011627) surfaces + half-height addon nodes ([cd14e5e](https://github.com/sislelabs/kuso/commit/cd14e5e978a361d02f7a4c93966a5e01b3a24c8b))

## [0.7.25] — 2026-05-04

### Other
- Ui: two-tier CTA system — orange primary, slate-blue secondary, thicker canvas borders ([17e59dc](https://github.com/sislelabs/kuso/commit/17e59dc7d2b416a4b010900306946810797eaec9))

## [0.7.24] — 2026-05-04

### Other
- Ui: solid CTAs use orange (palette complement), not slate-blue ([3b5b285](https://github.com/sislelabs/kuso/commit/3b5b2854ebb66b41d70233475e472031e7d650f6))

## [0.7.23] — 2026-05-04

### Other
- Ui: solid CTAs use slate-blue + cream, not pale periwinkle ([cdd0b08](https://github.com/sislelabs/kuso/commit/cdd0b08dbdf9fbee164463d6fcd6c05962ee59ea))

## [0.7.22] — 2026-05-04

### Other
- Ui: define --border-strong + brighten dark-mode border tokens ([61d72e2](https://github.com/sislelabs/kuso/commit/61d72e2dfa14a8756d991ae6601b240e124c9297))

### 🐛 Bug Fixes
- Fix(install): apply deploy/prometheus.yaml so the Metrics tab populates ([4846110](https://github.com/sislelabs/kuso/commit/4846110ca9a741eda23c609028167843af1b5c77))

## [0.7.21] — 2026-05-04

### Other
- Ui: full-palette dark theme + fixed-height canvas nodes ([f21f2f9](https://github.com/sislelabs/kuso/commit/f21f2f93cec55c4a9e866e57c73c76648fbec9aa))

## [0.7.20] — 2026-05-04

### 🐛 Bug Fixes
- Fix(auth): block app-shell render synchronously for pending users (was flashing settings/popovers) ([a82eb50](https://github.com/sislelabs/kuso/commit/a82eb50e552607c11f426e7f00f78bf5b3544928))

## [0.7.19] — 2026-05-04

### Other
- Release speed + first-OAuth-admin + auto-LE-prod + aubergine palette ([78c8182](https://github.com/sislelabs/kuso/commit/78c8182914596bc63b1ef155fa6f45bfd9a1aae1))

### 🧹 Chores
- Chore: track build/{updater,backup}/ Dockerfile sources (was being swept by /build/ ignore) ([ea5e20f](https://github.com/sislelabs/kuso/commit/ea5e20f79e92c9f9199863e477eba9f9e44fc47e))

## [0.7.18] — 2026-05-04

### 🐛 Bug Fixes
- Fix(config): /api/auth/methods detects GitHub via App fallback too ([fb6e60d](https://github.com/sislelabs/kuso/commit/fb6e60def648e39e32013e2b092e8c83a44c3605))

## [0.7.17] — 2026-05-04

### Other
- Security: close cross-tenant + admin authz gaps from full-project review ([d152f47](https://github.com/sislelabs/kuso/commit/d152f47068bf6b75f15d1ed5219c480ac2eea565))

### ⚡ Performance
- Perf: tier-2 scalability fixes (logdb split, build cap, operator concurrency, registry gc) ([87c24a9](https://github.com/sislelabs/kuso/commit/87c24a9cd8ee15c74ce81bb4b7e5157818659072))
- Perf: tier-1 scalability fixes (Describe cache, build dedup, async webhook) ([4623290](https://github.com/sislelabs/kuso/commit/46232905f65ace78563c438ed1ac1182399f347e))

### ✨ Features
- Feat(web): brand-aligned palette derived from kuso logo ([34ff1e4](https://github.com/sislelabs/kuso/commit/34ff1e494a5a26b15f68fdd889638fea7cb24439))

### 🐛 Bug Fixes
- Fix(release): retry gh release create on transient 502s ([77db565](https://github.com/sislelabs/kuso/commit/77db565262e5aa5731407d39b1c489fe83fae9cc))
- Fix(release): two-phase GH release upload with retries (resists transient 404/422) ([1d5f3e5](https://github.com/sislelabs/kuso/commit/1d5f3e533324d7a7ddc47c0c619873a4df54eefa))
- Fix(auth,install): GitHub sign-in works without re-pasting creds, HTTP→HTTPS redirect ([aef449f](https://github.com/sislelabs/kuso/commit/aef449fcb35a2d31a7deaf3dd0cf46c878b7a436))
- Fix(release): resolve OPERATOR_VERSION before writing release.json ([2eabe1e](https://github.com/sislelabs/kuso/commit/2eabe1ecc13c1296f753155bdb8c5fc82f86630d))

## [0.7.16] — 2026-05-04

### 🐛 Bug Fixes
- Fix(release): query ghcr for latest existing operator tag (was guessing wrong) ([aff7f44](https://github.com/sislelabs/kuso/commit/aff7f44f98cb06c9a638e76686c415b9229c2fc3))
- Fix(release): pin operator image to last actually-built tag ([569a11b](https://github.com/sislelabs/kuso/commit/569a11b2754d829f99ab9f8fb54c93acdfe5d0fc))

## [0.7.15] — 2026-05-04

### ✨ Features
- Feat(updater): bake updater image into release.json + handle nil RawPost body ([80d7838](https://github.com/sislelabs/kuso/commit/80d783824f762f6e6adf24309e39a2d4c92fec04))

## [0.7.14] — 2026-05-04

### ✨ Features
- Feat(updater): support pinned --version on kuso upgrade + add kuso github configure ([8c1fe3e](https://github.com/sislelabs/kuso/commit/8c1fe3ed6e4766d1959c3f5bd0c5372bd7e5e328))

## [0.7.13] — 2026-05-04

### Other
- Release tooling — CI workflow, dry-run, CHANGELOG, cleaner Makefile ([a59bf1f](https://github.com/sislelabs/kuso/commit/a59bf1f12a25aa0fedb3070f732ce2b1811da247))

### 🐛 Bug Fixes
- Fix(release): npm fallback in release.sh + npm cache in CI ([982f820](https://github.com/sislelabs/kuso/commit/982f820aca83cade170557db8f6ef9518bb206e2))
- Fix(release): use --unreleased not --current for GH notes ([607bc15](https://github.com/sislelabs/kuso/commit/607bc159e7a84605b87aa379ae7a070aefe4ced1))
- Fix(release): gate /healthz check behind dry-run too ([2e25a2c](https://github.com/sislelabs/kuso/commit/2e25a2c357ca4daaa02c3a1083b7dca9dcb7a8b4))

## [0.7.12] — 2026-05-04

### Other
- Install.sh: point users to /settings/github for App setup ([a592663](https://github.com/sislelabs/kuso/commit/a5926636fc0e0ec623f1b98abac191998161e072))

## [0.7.11] — 2026-05-04

### Other
- Go.sum: add transitive deps for remotecommand exec (spdystream, flowrate) ([29a9ecb](https://github.com/sislelabs/kuso/commit/29a9ecb932d3cdd84e82de2c4eccf3109267690a))

### ✨ Features
- Feat: v0.3.4 — fix builds, persist canvas, bulk env editor, editable settings ([05956ef](https://github.com/sislelabs/kuso/commit/05956efcde861b1f4ff1a474e75199c3d8eff1bb))
- Feat(web): v0.3.3 — drop left sidebar, fold admin items into user menu ([7d6956d](https://github.com/sislelabs/kuso/commit/7d6956d5fbd79d2330c6e3f367a2abec73bd99f0))
- Feat(web): v0.3.2 — overlay sharpness, denser dropdowns, slim sidebar, profile + projects refresh ([1291906](https://github.com/sislelabs/kuso/commit/12919060c94887d8ba3234b8309ddd0661f4124e))
- Feat(web): canvas-only project view, right-click menus, labels-only nodes ([84a032a](https://github.com/sislelabs/kuso/commit/84a032ac8a3d75a2a0d18fe046f1f7a7615c38a8))
- Feat(web): header-as-nav + servers in nav + icon-rail sidebar ([8baa90a](https://github.com/sislelabs/kuso/commit/8baa90a6e79f28cf7216346a07eedb5d3896ada2))
- Feat(web): service overlay panel — clicking a service opens an in-page sheet ([6a69830](https://github.com/sislelabs/kuso/commit/6a698302db8df962a735d1769779b936819d0a12))

### 🐛 Bug Fixes
- Fix(cli,install): honor KUSO_INSECURE for staging certs + surface reseeded GitHub App ([4f505a5](https://github.com/sislelabs/kuso/commit/4f505a55c70779dfe3d4d9edf1d0314404fd38aa))
- Fix(install): default KUSO_REF=main and die on CRD failures ([581d2ca](https://github.com/sislelabs/kuso/commit/581d2ca848cf87342024be5dc609d2c86fc97d00))
- Fix(web): cookie wins over localStorage so post-OAuth handoff isn't blocked by stale token ([d797c2d](https://github.com/sislelabs/kuso/commit/d797c2d58f1d541955f5e1010785acd40ee50c30))
- Fix(web): service URL uses short name, canvas zoom + scroll + minimap, sidebar params ([ed7ef7c](https://github.com/sislelabs/kuso/commit/ed7ef7cdee8f154cc1589bfc6a0315ff93a26556))
- Fix(web): read dynamic route params from pathname, not the static-export placeholder ([5408dc8](https://github.com/sislelabs/kuso/commit/5408dc8a24ece58a4527b8a2aac2f1380948d4e7))
- Fix: repo-picker search + dynamic-segment SPA routing ([8962b75](https://github.com/sislelabs/kuso/commit/8962b75641511f2438a10a8bf4442d1ad7ffd986))
- Fix(web): match server's GithubInstallation wire shape ([f91c379](https://github.com/sislelabs/kuso/commit/f91c3795db89443ec6d758213e6fb4ee50ddfcc6))
- Fix(spa): resolve Next.js export layout for /projects/new + sibling routes ([7075041](https://github.com/sislelabs/kuso/commit/7075041640aeef8ed52d4f222bee3cef1d6e5df1))
- Fix(web): hydrate JWT from cookie on first load after OAuth callback ([ad5c078](https://github.com/sislelabs/kuso/commit/ad5c07820c850e8ddacf724dcb01d3515dcafd91))

### 📝 Docs
- Docs: extend CLAUDE.md with architecture cheatsheet + active roadmap ([e58079e](https://github.com/sislelabs/kuso/commit/e58079e9dba3e054dbdf31c3afb3d9f3de4466ad))

## [0.2.1] — 2026-05-02

### ✨ Features
- Feat(web): Phase H - cmd palette + landing page ([d678d0b](https://github.com/sislelabs/kuso/commit/d678d0b159f72fd866f794547a7d9b0dbe2aac94))
- Feat(web): Phase G - canvas (React Flow) ([bbc6e0c](https://github.com/sislelabs/kuso/commit/bbc6e0c8af4ae840c99e38909d451dd334624dcf))
- Feat: Phase F - project creation fast path + Next default cutover ([94ac383](https://github.com/sislelabs/kuso/commit/94ac3839167efc55ea3f7e7fc06f5f479799bef6))
- Feat: Phase E - backend additions + var-ref parser ([5a74def](https://github.com/sislelabs/kuso/commit/5a74def4843fcfc759a5698edc78af7498382bc3))
- Feat: Phase D - WebSocket log streaming ([4ac29e2](https://github.com/sislelabs/kuso/commit/4ac29e2a62ea699baae0336ddbfc5cec892de318))
- Feat(web): Phase C - project/service detail, env vars editor, activity, settings ([6b9610a](https://github.com/sislelabs/kuso/commit/6b9610a1d60f8243a4dbfbc617ad7fbdd982ba4d))
- Feat(web): Phase B - app shell + projects list ([6805688](https://github.com/sislelabs/kuso/commit/68056880f00054e64d37df4518c6abe84a0720b7))
- Feat(server-go): dual-embed legacy + next dists, KUSO_FRONTEND switch ([18c42eb](https://github.com/sislelabs/kuso/commit/18c42ebf141daa4446796ca12d1d284959eae024))
- Feat(web): login page with local + GitHub + OAuth2 sign-in ([a8326e5](https://github.com/sislelabs/kuso/commit/a8326e5cfe0878eb290cc3e34786a876088ce9d0))
- Feat(web): AuthGate component and (app) layout ([7f4f3ea](https://github.com/sislelabs/kuso/commit/7f4f3ea9c0e4999c3de0dea8239c596f30db162e))
- Feat(web): auth feature - api, schemas, useSession/useLogin/useSignOut ([92c14ab](https://github.com/sislelabs/kuso/commit/92c14abec586573bd1bef4419eba6761c681f685))
- Feat(web): Logo and ErrorBoundary shared components ([aa76acb](https://github.com/sislelabs/kuso/commit/aa76acb65074edc047d7c0b801e6d112c39084ae))
- Feat(web): port shadcn primitives from robiv0 ([51690d4](https://github.com/sislelabs/kuso/commit/51690d48cfdce6f45bc0d30571a86b02625ddb3c))
- Feat(web): api-client wrapper with JWT injection and ApiError ([63882c6](https://github.com/sislelabs/kuso/commit/63882c64429364fbe613061404e2b594df2af0b1))
- Feat(web): root layout with fonts, theme, query client, toaster ([06c5f2f](https://github.com/sislelabs/kuso/commit/06c5f2f967ed7cec20c804e598da886a1c8599d2))
- Feat(web): tailwind 4 + design tokens + cn helper ([d77122b](https://github.com/sislelabs/kuso/commit/d77122bcae2eac97c98ce513a0b825a17c508b91))
- Feat(web): initialize Next.js 16 project scaffold ([160c8e9](https://github.com/sislelabs/kuso/commit/160c8e904c5754d59a8274a90e9f743466ed7333))

### 👷 CI
- Ci: add web/ build job; switch web embed to all: prefix ([da69394](https://github.com/sislelabs/kuso/commit/da69394fc5bbbf34ebd25142083a98e8a98ce72c))

### 📝 Docs
- Docs: README updates for web/ frontend ([6980ac9](https://github.com/sislelabs/kuso/commit/6980ac9643a4a4aaab0463c6110d03277699b8e6))
- Docs: implementation plan for Phase A (web/ scaffold + auth) ([33e27dc](https://github.com/sislelabs/kuso/commit/33e27dc7e6bf0cac6ee3e6890d46b88725b0d9fb))
- Docs: spec for Next.js frontend rewrite with Railway-style UX ([60a9a67](https://github.com/sislelabs/kuso/commit/60a9a67b42dc99bdd0788aa020744329ca3a971e))

### 🧹 Chores
- Chore: delete Vue legacy frontend; collapse dual-embed to single dist ([695d31b](https://github.com/sislelabs/kuso/commit/695d31be419cac9344a758843d9fa443b8628d4e))

## [0.2.0] — 2026-05-02

### Other
- Hack/install.sh: point at deploy/server-go.yaml + Go image ([ebebf8f](https://github.com/sislelabs/kuso/commit/ebebf8f51d13abe5897c8efcb08bc8e06a7afc9e))
- Cli: --yes flag on destructive deletes + --expires-at alias on token create ([e020f28](https://github.com/sislelabs/kuso/commit/e020f282b906630e14a9a8a5ceaf5f71049aefb2))
- Rewrite: prismaTime adapter + admin bootstrap + Hetzner cutover ([8676123](https://github.com/sislelabs/kuso/commit/8676123a95c068a2640fd2aec5816e02413005d8))
- Rewrite: phases 18-22 admin tokens + audit + events + cleanup + SPA ([22c019a](https://github.com/sislelabs/kuso/commit/22c019aff6b9afdaf845ff9c0d90ec48273c515a))
- Rewrite: phases 16-17 github admin endpoints + OAuth sign-in ([23e5b1a](https://github.com/sislelabs/kuso/commit/23e5b1a4734d90815c5098786cc7ed787360a4b7))
- Rewrite: phases 14-15 groups CRUD + notifications ([b3d964e](https://github.com/sislelabs/kuso/commit/b3d964e20155f74198783e4ffdbf11b9e47b7b9b))
- Rewrite: phases 11-13 addons + users CRUD + roles CRUD ([9ab6355](https://github.com/sislelabs/kuso/commit/9ab63551deb213bfa66e7c297491ce4a7f72010d))
- Rewrite: phase 9 config service + phase 10 status service ([cf6f7f8](https://github.com/sislelabs/kuso/commit/cf6f7f8d068cb398dfe24dd3de495f2f88d3a479))
- Rewrite: phase 7 logs + phase 8 admin CRUD + container fixes ([66726e3](https://github.com/sislelabs/kuso/commit/66726e3e0da977b98512b1d247617400aba7abb3))
- Rewrite: phase 6 GitHub App + webhook (push/PR dispatch) ([6d0d318](https://github.com/sislelabs/kuso/commit/6d0d318e4c2c456de96226d8bf6982f6374019ef))
- Rewrite: phase 5 builds (KusoBuild lifecycle + status poller) ([fa6ba6d](https://github.com/sislelabs/kuso/commit/fa6ba6dff851e6fe45d287af474895537c016daa))
- Rewrite: phase 4 secrets + secretsRev (race-free) ([e997d7b](https://github.com/sislelabs/kuso/commit/e997d7bcdf76fcbf514cc8c8106b9d9dfb21f7af))
- Rewrite: phase 3 projects + services + env CRUD ([191cace](https://github.com/sislelabs/kuso/commit/191cace41184cbe522f9e69e9b387736a2cc9c09))
- Rewrite: phase 2 db + auth (login + JWT) ([f35b98e](https://github.com/sislelabs/kuso/commit/f35b98e907495ee17b584cf4a403b07a91e73ea2))
- Rewrite: phase 1 kube client + typed CRD wrappers (#2) ([ad803b2](https://github.com/sislelabs/kuso/commit/ad803b266f5a46284b9db740a5f3b9a8f4655a7d))
- Rewrite: phase 0 scaffold for Go server (server-go/) (#1) ([2a22a3f](https://github.com/sislelabs/kuso/commit/2a22a3ff5c5cd9009ef794998c66f9784b216215))
- Server(v0.2): preview env TTL cleanup reconciler (Phase 6) ([10b87ca](https://github.com/sislelabs/kuso/commit/10b87cab16adce95d34feb5a364da31ae479b6b4))
- Cli + mcp(v0.2): project/service/env/addon surface (Phase 5) ([4f6b259](https://github.com/sislelabs/kuso/commit/4f6b259f5283982e361d74be548ff1cb69070d8d))
- Client(v0.2): projects-first UI redesign (Phase 4) ([7f27a9b](https://github.com/sislelabs/kuso/commit/7f27a9b4dd896f51c71f0fafb0a17006ae11803c))
- Server(v0.2): GitHub App integration (Phase 3) ([62d3153](https://github.com/sislelabs/kuso/commit/62d3153f2480289a3b11e29ea2f26643c0c41a20))
- Server(v0.2): add projects API surface (Phase 2) ([70c1e4a](https://github.com/sislelabs/kuso/commit/70c1e4accc793d2ad458d389da33959c2c42f575))
- Operator(v0.2): replace pipelines/apps CRDs with projects/services/envs/addons ([f69ba4f](https://github.com/sislelabs/kuso/commit/f69ba4fdb5d6a313bd944834fa2948eb5e26f209))

### ✨ Features
- Feat: PATCH /api/projects, buildpacks+static runtimes, OAuth tests, backup, multi-tenancy ([fcd3ada](https://github.com/sislelabs/kuso/commit/fcd3ada2d58019501acdf9050cdc1d45abdb7b83))
- Feat: nixpacks build strategy + correct kaniko paths + helm finalizer name ([338db2c](https://github.com/sislelabs/kuso/commit/338db2cd81c45867a57809320d7fe2475759998d))
- Feat(secrets): per-environment scoping with auto pod-roll on value changes ([f17285b](https://github.com/sislelabs/kuso/commit/f17285ba9f0a5b9e892b3e9563059ffd9ca5714f))
- Feat: install.sh polish + kuso token CRUD + kuso logs ([0335bc2](https://github.com/sislelabs/kuso/commit/0335bc2e5693ad72d65ec2e2aa96e4b82609ec30))
- Feat(cli,server): full e2e via CLI — project to running URL with secrets ([3204342](https://github.com/sislelabs/kuso/commit/3204342f9534411c42d986796d19b79c2c606e4f))
- Feat(builds,ui): kuso build pipeline end-to-end (Phase A) ([b2761c1](https://github.com/sislelabs/kuso/commit/b2761c19db3fed89057c069dd90a143a0c985428))
- Feat: end-to-end install — Hetzner, ghcr images, hello-world deploy ([c9c3fa6](https://github.com/sislelabs/kuso/commit/c9c3fa6703bc73f5e748e8a4db1c7be0db99f2ca))
- Feat(cli): add agent-friendly 'kuso get' command tree ([58ab004](https://github.com/sislelabs/kuso/commit/58ab00485c060f31b68c826839037e7c8e5653d9))
- Feat(mcp): add restart_app and tail_logs tools ([4112060](https://github.com/sislelabs/kuso/commit/41120606ed926be5255c12401e5f80da1185dcbe))
- Feat(mcp): add list_apps, describe_app, troubleshoot_app ([1ce2a5d](https://github.com/sislelabs/kuso/commit/1ce2a5d435f8af00e9ad7fde5bf63f696d4bbf71))
- Feat(mcp): scaffold kuso-mcp Go module ([06826b9](https://github.com/sislelabs/kuso/commit/06826b9f5d7dbb1f2431b214a3fc344b602153c9))

### 🐛 Bug Fixes
- Fix(oauth): drop HttpOnly on JWT cookie; seed kuso-github-app from install.sh ([2709c75](https://github.com/sislelabs/kuso/commit/2709c755aa6ceaf7c59294b9ced5729926af5932))
- Fix: preview env survives PR sync + image gets promoted onto it ([cdb6c09](https://github.com/sislelabs/kuso/commit/cdb6c098d43bd4444510c5667fec14ae5ee6214d))
- Fix: build poller status patch + CLI env list/set/unset response shape ([3559c2f](https://github.com/sislelabs/kuso/commit/3559c2f826f720c9775823bd469c20f37fcb963e))
- Fix(db): cascade Audit + Token + group + GithubUserLink rows on user delete ([8088e6f](https://github.com/sislelabs/kuso/commit/8088e6f166a72a88864fa8fa39a230a3cfebf712))
- Fix(secrets): race-free single-key upsert/remove ([dfa37c9](https://github.com/sislelabs/kuso/commit/dfa37c963938a45a820945be071bdf160a5f6551))
- Fix(install.sh): create kuso namespace before applying registry ([954c0fe](https://github.com/sislelabs/kuso/commit/954c0fe8fc966ab53c1c18e04cd581c459356587))
- Fix(server,deploy): GitHub App install callback + secret wiring ([5e4bfb3](https://github.com/sislelabs/kuso/commit/5e4bfb3a7d86f2d666a93067ba5ac41fe605f494))
- Fix(addons,server): connection-secret naming + env merge-patch (Phase 7 smoke) ([375e06f](https://github.com/sislelabs/kuso/commit/375e06f717e5f3c72cd295236d0b1f84901432da))
- Fix(server): three bugs in the first-boot config path ([8080e38](https://github.com/sislelabs/kuso/commit/8080e3833876338ddb7cbe078cb0c0b446e2bc98))
- Fix(client): blank UI on first load — four bugs in one chain ([148293b](https://github.com/sislelabs/kuso/commit/148293b89709825b3e46e58d4c5d66a678ec81c4))
- Fix: complete rebrand cleanup — kuso-dev -> sislelabs, add VERSION stubs ([e310103](https://github.com/sislelabs/kuso/commit/e31010385e05856fc53bcdbe8c1796da3d454c70))

### 👷 CI
- Ci: add per-subproject GitHub Actions workflow ([76cdff3](https://github.com/sislelabs/kuso/commit/76cdff3d9bae4cc1b449b177b6190cd8eaff63ff))

### 📝 Docs
- Docs: workflow reference + live test plan for Go server cutover ([41c5eb2](https://github.com/sislelabs/kuso/commit/41c5eb2140d6a160dbcc144cf4c50d8cc9ef5fbb))
- Docs: rewrite spec for NestJS → Go server port ([d574f4f](https://github.com/sislelabs/kuso/commit/d574f4f7aedd7eb6dd29322c4ebff54ed4c18742))
- Docs: v0.2 design — projects, not pipelines (Phase 0) ([2dd5860](https://github.com/sislelabs/kuso/commit/2dd5860df48cb009901912368f6caf1f8023b7ed))
- Docs: seed PRD, REBRAND notes, and .claude/skills ([4937a24](https://github.com/sislelabs/kuso/commit/4937a249b808717f1af7000acd4b476b30066b18))

### 🔨 Refactors
- Refactor: purge v0.1 modules — apps/pipelines/deployments/repo/addons (Phase C) ([b939f06](https://github.com/sislelabs/kuso/commit/b939f06b131a935822241efe4de0b273da0ff5b9))
- Refactor: rewrite all upstream asset refs to sislelabs/kuso-* paths ([89bf4c8](https://github.com/sislelabs/kuso/commit/89bf4c8843881958108d5ae821aeb33c9f9ffab1))
- Refactor: rebrand kubero -> kuso (full pass) ([b96cc57](https://github.com/sislelabs/kuso/commit/b96cc57c82fe4a821dd569c1dce282a23cff8376))

### 🧪 Tests
- Test(operator): add CRD dry-run smoke test on kind (3b) ([d55522a](https://github.com/sislelabs/kuso/commit/d55522ad1f3ef34e55ef106e833e045ca681a163))
- Test(mcp): add stdio integration test suite (3a) ([6671f36](https://github.com/sislelabs/kuso/commit/6671f363ef39212b253c2ab069320b796f32d96f))

### 🧹 Chores
- Chore: extend finalizer sweep to KusoBuild + bump server to v0.2.0-rc10 ([196ba3a](https://github.com/sislelabs/kuso/commit/196ba3aedd45060c610f5dff657203a12be6f81e))
- Chore: close remaining gaps — finalizer sweep, runtime gate, dead CLI ([46e0906](https://github.com/sislelabs/kuso/commit/46e09064ee2388273665226094f0fa7786313591))
- Chore(deploy): drop ghcr-pull secret — kuso-server-go is public on GHCR ([9a5bfd5](https://github.com/sislelabs/kuso/commit/9a5bfd5aecb62fe5f3e375b7bd7eb32b6007da3c))
- Chore(deploy): repoint manifests at GHCR Go image + add ghcr-pull doc ([648bf33](https://github.com/sislelabs/kuso/commit/648bf338a1170182296ccc98a8c2d21000614332))
- Chore: delete TS server, repoint helm chart + CI at server-go ([98db71e](https://github.com/sislelabs/kuso/commit/98db71ea3ce7eba016ca5f1754fb1c1cbb552bf6))
- Chore: import cli from kubero-dev/kubero-cli ([3a30d23](https://github.com/sislelabs/kuso/commit/3a30d232c3273abc8c01c9ab0c0b5b23cbc432d0))
- Chore: import operator from kubero-dev/kubero-operator ([f8c47a2](https://github.com/sislelabs/kuso/commit/f8c47a202819aec20543902005818b0c6d3ecd1e))
- Chore: import server and client from kubero-dev/kubero ([8d9de15](https://github.com/sislelabs/kuso/commit/8d9de15d7e84a8f3e61691a1f97405b7e300659b))
- Chore: bootstrap kuso monorepo ([78dce62](https://github.com/sislelabs/kuso/commit/78dce62181a735bd7a2554b24c5faabaa19b2aa4))

<!-- generated by git-cliff -->
