# Uptime checks implementation plan

**Goal:** kuso pings every production web service each minute and sends one Discord message
when it goes down and one when it recovers.

**Spec:** `docs/superpowers/specs/2026-10-02-uptime-checks-design.md`

**Architecture:** a new `server-go/internal/uptime` package holds a pure state machine, an
HTTP prober, a grouping step and a leader-gated loop. State lives in a new `UptimeState`
Postgres table and is re-read every tick. Opt-out and path live on the project and service
CRs.

## Global constraints

- Constants: interval 60s, timeout 10s, downAfter 3, upAfter 3, cooldown 30m, 16 workers,
  stormProjects 3.
- Parameterised SQL only. No `interface{}` where a concrete type fits.
- CRD changes are additive.
- Send events only after the state commit succeeds.

## Review focus

1. A `uptime.path` like `//evil.com/x` must not change the probed host.
2. A failed kube list or DB read must skip the tick, never prune rows or alert.
3. A restart mid-outage must not send a second "down".
4. A muted project must not be counted towards the cluster-wide message.
5. `KUSO_UPTIME_DISABLED=true` must not register a heartbeat.

## Tasks

- [ ] **1. State machine** — `internal/uptime/decide.go`, `decide_test.go`.
  `Decide(State, Outcome, now) (State, Action)`; table tests for every spec case.
- [ ] **2. Prober** — `internal/uptime/probe.go`, `probe_test.go`.
  `TargetURL(ns, env, path)`, `Prober.Probe(ctx, url) Result`; httptest cases 200, 302, 404,
  500, hang, closed port, hostile paths.
- [ ] **3. DB** — `internal/db/migrations/0014_uptime_state.sql`, `internal/db/uptime_state.go`,
  test. `ListUptimeStates`, `ListUptimeStatesByProject`, `SaveUptimeStates(rows, deleteKeys)`
  in one transaction.
- [ ] **4. Events** — `internal/notify/notify.go` (consts, catalogue, tone),
  `internal/notify/events.go` (`UptimeDown`, `UptimeRecovered`, cluster variants), tests.
- [ ] **5. CRD fields** — `internal/kube/types.go`, both CRD YAMLs, `crd_schema` testdata.
- [ ] **6. Grouping** — `internal/uptime/group.go`, `group_test.go`.
- [ ] **7. Loop** — `internal/uptime/uptime.go` (targets, tick, self-check), tests with fakes;
  `serverstate.LoopUptime`; wiring in `cmd/kuso-server/main.go`.
- [ ] **8. API** — project and service PATCH accept `uptime`; `GET /api/projects/{p}/uptime`.
- [ ] **9. kuso.yml** — `internal/spec/{spec,export,apply,diff}.go`.
- [ ] **10. CLI** — `cli/pkg/kusoApi/uptime.go`, `cli/cmd/kusoCli/uptime.go`.
- [ ] **11. Web** — toggles in project settings and the service settings panel.
- [ ] **12. Verify** — `make test`, `make test-db`, web typecheck and build.
- [ ] **13. Ship** — commit, push, `make ship`, roll the live instance, live check.
