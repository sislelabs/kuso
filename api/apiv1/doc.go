// Package v1 holds the wire-shape DTOs the kuso HTTP API uses.
//
// Both the server (kuso/server) and the CLI (kuso/cli) import this
// module. Before it existed, every request/response struct lived
// twice: once in server-go/internal/<domain> as the real type, and
// once in cli/pkg/kusoApi as a hand-rolled copy. The CLI's copies
// drifted from the server's whenever someone forgot to mirror a
// field — the CLI silently dropped it. Sharing a no-deps module
// kills that class of bug.
//
// Design rules:
//
//   - No imports outside the Go stdlib. No k8s, no http frameworks,
//     no toolchain pins. This module compiles cleanly in any Go
//     ≥1.25 install regardless of what server-go pulls in.
//   - Wire-stable JSON tags. Renaming a field is a breaking change;
//     callers (CLI, web via tygo, future Terraform provider) all key
//     on the JSON name.
//   - Pointer fields express "leave alone" (omit) vs "set to zero"
//     for partial updates. New fields default to omitempty so older
//     clients ignore them.
//
// File layout mirrors the URL surface: projects.go for
// /api/projects/*, services.go for service nested routes, etc.
//
// This is v1 — additive changes only. v2 lives next door if/when
// we ever break wire format. We're not building a Terraform
// provider today, but we will be in v2.
package v1
