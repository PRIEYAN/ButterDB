# Object viewer implementation plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Open uploaded files in butterDB and stream video/audio with embedded MPV.

**Architecture:** Objects requests a viewer through a consumer-owned port. An
admin-only viewer package renders images and file cards and serves private
HTTP ranges to an injected MPV process adapter.

**Tech Stack:** Go, Fyne 2.6, existing adminv1 client, installed MPV executable.

**Spec:** docs/superpowers/specs/2026-11-17-object-viewer-design.md

## Global constraints

- No new Go dependencies; preserve existing uncommitted project work.
- No object bytes on disk; images bounded at 20 MiB and 40 megapixels.
- All streaming lives on loopback and ends with its viewer/project context.
- Embed MPV in the butterDB window; never silently launch an external player.

## Review focus

- Object changes during playback must fail ETag preconditions.
- Range requests include byte zero, suffixes, empty files, and invalid ranges.
- Closing a viewer must cancel upstream reads and terminate MPV.
- Malformed or oversized images must not hang or crash the UI.
- Missing MPV or unsupported native handles must produce an actionable error.

## Task 1: File activation

Files: objects/browser.go, rows.go, viewer.go, objects_test.go.
Interface: FileOpener.Open(context.Context, string, adminv1.ObjectSummary).

- [ ] Add failing UI tests for object activation, Open and Cancel.
- [ ] Implement the popup and preserve folder navigation.
- [ ] Run the Objects tests.

## Task 2: Viewer and streaming

Files: internal/viewer/viewer.go, image.go, stream.go and matching tests.
Interfaces: Source.ObjectMeta/GetObject matching adminv1.Client;
Player.Play(context.Context, uintptr, string) error; New(Dependencies).

- [ ] Add failing tests for rendering, range requests, errors, cancellation.
- [ ] Implement image/file presentation, bounded reads, private range server,
      and native media window lifecycle.
- [ ] Run viewer tests and race checks.

## Task 3: MPV and integration

Files: internal/app/mpv.go, composition/session wiring, imports_test.go,
docs/decisions.md, docs/spec/admin-gui.md, docs/guide/admin-app-build.md.

- [ ] Test MPV invocation, missing executable, cancellation and errors first.
- [ ] Implement the adapter and inject the viewer into Objects.
- [ ] Verify the full admin suite, vet, build, architecture/comment checks.
- [ ] Smoke-test real native MPV against generated local media.
- [ ] Review the final change and resolve material findings.

## Execution record

The current shared checkout contains the uncommitted embedded-project feature
that this change extends. Work stays in this checkout; moving to a clean
worktree would omit those required changes. No staging, branch switching, or
committing other work is authorized by this plan. The owner selected native
MPV after the complete requested behavior was presented; proceed within that
authorized scope without another permission question.
