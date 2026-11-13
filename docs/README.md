# butterDB documentation

butterDB is self-hosted object storage that speaks the S3 protocol and offers
Cloudinary-style media transforms, with its bytes stored in the deploying
developer's own Telegram account.

## Start here

| Read | When |
| --- | --- |
| [decisions.md](decisions.md) | Always first. Every product and engineering decision, the open assumptions, and the accepted risks |
| [claude.md](claude.md) | Before writing any code: house rules, layering, comment policy, git rules |
| [plan.md](plan.md) | The architecture, package layout, request flows and phase overview |
| [phases/00-execution-protocol.md](phases/00-execution-protocol.md) | Before running a phase: how one Opus orchestrator drives parallel Sonnet lanes in git worktrees |

## Authority order

`decisions.md` → `spec/*.md` → `plan.md` → `phases/*.md` → `butterDBCore.md`.
When two documents disagree, the earlier one wins until it is changed on
purpose.

## Specifications

| Spec | Covers |
| --- | --- |
| [spec/storage-model.md](spec/storage-model.md) | Accounts, registry and bucket channels, the object commit protocol, overwrite, delete, encryption, sync, rebuild |
| [spec/wire-format.md](spec/wire-format.md) | TDX1 grammar and the op catalogue |
| [spec/schema.md](spec/schema.md) | SQLite DDL for `projection.db` and `control.db`, connections, migrations, table ownership |
| [spec/s3-api.md](spec/s3-api.md) | The S3-compatible API: SigV4, operations, multipart, errors, timeouts, compatibility tests |
| [spec/media-api.md](spec/media-api.md) | Cloudinary-style URL transforms, signing, derivative cache, the transform worker |
| [spec/admin-api.md](spec/admin-api.md) | The admin JSON API, server claim, device tokens, pairing, Telegram login relay |
| [spec/admin-gui.md](spec/admin-gui.md) | The Fyne desktop admin app |
| [spec/operations.md](spec/operations.md) | Configuration, data directory, startup, snapshots, restore, deployment, metrics, capacity |
| [spec/security.md](spec/security.md) | Threat model, keys, secrets, admin authentication, the security test checklist |

## Build phases

Run strictly in order. Each document is executed by one Opus orchestrator that
fans its lanes out to parallel Sonnet workers.

| Phase | Builds |
| --- | --- |
| [01](phases/01-foundation.md) | Platform primitives, config, logging, lifecycle, guardrail scripts |
| [02](phases/02-formats-gate.md) | TDX1 ops, TDE1 arithmetic, schema v1, domain types. Hard gate |
| [03](phases/03-storage-engine.md) | Projector, rebuild, encryption streams, keyring, object and bucket repositories |
| [04](phases/04-telegram-transport.md) | MTProto transport, flow control, fake Telegram, identity and login |
| [05](phases/05-buckets-and-sync.md) | Buckets, history scan, differences, live sync, recovery |
| [06](phases/06-write-path.md) | Spool, put, delete, admission, job journal, multipart staging |
| [07](phases/07-read-path.md) | Range reader, origin cache, object reader, HTTP range helpers |
| [08](phases/08-s3-api.md) | Access keys, SigV4, policy, the S3 API |
| [09](phases/09-admin-api.md) | Admin API, server claim, device tokens, pairing, TLS |
| [10](phases/10-admin-gui.md) | Fyne admin app |
| [11](phases/11-operations-and-release.md) | Health, metrics, snapshots, deployment, compatibility and load tests, guides; first release |
| [12](phases/12-replica-account.md) | Replica Telegram account |
| [13](phases/13-media-transforms.md) | Media transforms and the transform worker |

## Reference

| Document | Purpose |
| --- | --- |
| [reference/tdrive-port-map.md](reference/tdrive-port-map.md) | Which TDrive package becomes which butterDB package, and in which phase |
| [legal/THIRD_PARTY_NOTICES.md](legal/THIRD_PARTY_NOTICES.md) | TDrive's MIT notice and dependency licenses |
| [butterDBCore.md](butterDBCore.md) | Legacy engine specification (TDrive's explainer). Lowest authority; read sections by line range |
| [guide/](guide/) | Operator guides, written in phase 10 and phase 11 |
