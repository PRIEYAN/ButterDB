# butterDB — Working Instructions

Instructions for anyone, human or agent, writing code in this repository.

Read this before touching a file. Read `docs/plan.md` for the build order and
`docs/butterDBCore.md` for formats, constants and algorithms.

---

## 1. The project

butterDB turns a user's own Telegram account into a personal cloud drive: a
desktop and mobile file manager with media streaming, client-side encryption,
shared drives, an OS mount and a scriptable CLI.

It is local-first. There is no server, no container, no shared credential and no
hosted API. File bytes are Telegram document messages; the namespace is an
append-only log of `TDX1|...` control messages in the same chat history. SQLite
is a projection of that log, rebuildable offline.

### Document authority

| Document | Owns |
| --- | --- |
| `docs/butterDBCore.md` | Wire formats, DDL, algorithms, constants, RPC catalogue |
| `docs/plan.md` | Layering, interfaces, milestones, acceptance criteria |
| `docs/claude.md` | How to write the code and how to commit it |

On a conflict about a byte, a size or a constant, `butterDBCore.md` wins. On a
conflict about structure, `plan.md` wins. Never change a specified constant to
make a test pass.

---

## 2. Hard rules

These are not preferences. Work that breaks one of them is rejected.

1. **Go for everything.** All application logic is Go. The Svelte view layer
   exists only because Wails renders in a webview; it holds rendering and input,
   never domain rules, formats, transport policy or security decisions.
2. **No comment blocks.** No comment may span more than one line. Two
   consecutive comment lines are a defect — extract a named function instead.
   See §5.
3. **Every document lives in `docs/`.** No `README` sections of prose elsewhere,
   no design notes beside the code, no explanatory markdown in package
   directories.
4. **Never push.** Commit locally. Pushing is the repository owner's decision.
5. **Commit author is `prieyan <mnprieyan@gmail.com>`** on every commit.
6. **Commit messages are one line of simple English** with no body and no
   description. See §7.
7. **Clean architecture, SOLID and constructor injection** are structural
   requirements, not aspirations. See §3 and §4.

---

## 3. Architecture rules

### 3.1 Dependencies point inward

```
interface  →  application  →  domain
                   ↓
            infrastructure  →  domain
```

The domain layer imports nothing from this project and no third-party package.

Enforced import rules:

| Rule |
| --- |
| `backend/...` must never import `internal/app/...` |
| `internal/app/...` and `cmd/butterdbd/...` must never import each other |
| Only `backend/tgclient` may import `github.com/gotd/td` |
| Only `backend/storage` may import `modernc.org/sqlite` |
| `projection/wire`, `crypto/format` and `domain/...` import the standard library only |

### 3.2 Interfaces belong to the consumer

Declare a port in the package that calls it. Name it for the role, not the
implementation: `MessagePublisher`, not `GotdClientInterface`. Keep it to the
methods that caller actually uses. A port with more than about five methods
means two roles were merged — split it.

Adapters satisfy ports implicitly. An adapter package never imports a consumer
package to declare conformance.

### 3.3 Constructor injection only

Every dependency arrives through the constructor and is validated there.

```go
func NewService(deps Dependencies) (*Service, error) {
    if deps.Publisher == nil {
        return nil, fmt.Errorf("upload: %w: publisher", errs.ErrMissingDependency)
    }
    return &Service{publisher: deps.Publisher}, nil
}
```

No service locator. No dependencies carried in a `context.Context`. No setter
injection. No package-level singletons. A constructed value is fully usable or
it does not exist.

### 3.4 One composition root

`backend/core` is the only package that constructs concrete adapters and wires
them together. If another package calls a constructor from an adapter package,
that is a layering defect.

### 3.5 No ambient state

No `init()` that does work. No package-level mutable variables. Time,
randomness, the filesystem and the network are injected ports.

The projector is not given a clock, because the specification requires that
domain state never comes from the local clock. Deletion and purge timestamps
travel on the wire so replay is deterministic across devices.

### 3.6 Lifecycles are explicit

```go
type Component interface {
    Start(ctx context.Context) error
    Close(ctx context.Context) error
}
```

Every goroutine has an owner that can stop it and wait for it. Components start
in dependency order and close in strict reverse with a bounded context. The
mount closes before the engine; the engine before logging; the process lock
releases last.

---

## 4. SOLID in practice

| Principle | What it means here |
| --- | --- |
| **Single responsibility** | One package, one reason to change. `projection/wire` changes when the grammar changes and never for another reason |
| **Open/closed** | New operation types register an applier; no caller's switch grows. The wire parser's switch stays closed on purpose — an unknown type must fail as `ErrWireBadOpType`, while unknown *keys* are ignored so old clients degrade gracefully |
| **Liskov substitution** | Every implementation honours the port's error contract. A fake `MessagePublisher` that cannot return `ErrSendOutcomeUnknown` hides the bug the upload design exists to survive. Read-only behaviour is a separate type, never a write method returning "unsupported" |
| **Interface segregation** | The thumbnail extractor depends on `ReadDocumentRange` and nothing else, so it cannot reach a send path |
| **Dependency inversion** | Services depend on ports they declare; `core` supplies the adapters |

---

## 5. Code style

### 5.1 Comments

No comment may span more than one line. A single-line comment is allowed only
where a non-obvious external constraint cannot be expressed in a name:

```go
// Telegram rejects offsets that are not 4 KiB aligned.
if offset%RangeReadAlignment != 0 {
    return 0, errMisaligned
}
```

Not allowed:

- Any comment block, including doc comments on exported identifiers.
- A comment restating what the next line does.
- Commented-out code.
- `TODO` and `FIXME` markers. Open the work or do it.

If an explanation needs a paragraph, it belongs in `docs/`.

### 5.2 Naming carries the meaning

Names replace the comments. `manifestAttempted`, `storedSize`,
`acquireBackgroundGetFileSlots`, `publishByRename`, `wouldCreateCycle` — each
says what it is without help.

Never name by type: not `fileStruct`, not `dataMap`, not `utils`. Never
abbreviate beyond the domain's own vocabulary — `pts`, `DC`, `AEAD` and `fMP4`
are established terms and stay.

### 5.3 Functions

Small, one level of abstraction each, early returns over nesting. A function
needing a comment block to explain its sections is several functions.

### 5.4 Errors

Sentinel errors in `domain/errs`, wrapped with `%w` at every boundary. Stable
codes are separate from display messages; callers branch on codes and types,
never on message text. Flood waits become a typed `FloodWaitError` at the
transport boundary. `ErrSendOutcomeUnknown` is never collapsed into a generic
failure.

### 5.5 Formatting

`gofmt` is final. Tabs for indentation. Imports in three groups: standard
library, third party, this module.

---

## 6. Invariants

Assert these in code. Each one has a failure mode that corrupts user data.

1. Every remote operation enters through `ProjectFromOp` or `ProjectFromOpTx`.
   There is no other legal way to mutate the log or the projection.
2. No SQLite transaction is open across a network call.
3. Split decisions use **stored** size, never plaintext size. A file just under
   1900 MiB becomes multipart once encrypted.
4. `manifestAttempted` is set **before** the manifest send, never after.
5. Download block rows are inserted only **after** the staging file is synced.
6. Every master key copy is cleared on every return path, error paths included.
7. Media session publication re-checks the encryption generation under the gate.
8. Upload retries rewind a seekable body; they never resume mid-stream.
9. Part messages never enter the `files` table.
10. The projector never reads the local clock for domain state.
11. Error branching uses stable codes, never display text.
12. Loopback servers validate tokens; the gallery server also validates `Host`.

### The three things that bite

- **"Telegram accepted it" is not "the projection applied it."** An upload can
  return a valid message ID *and* an error. The file exists. Do not resend it.
- **The 1900 MiB split boundary and the 2,000,000,000-byte resumable threshold
  are unrelated.** Files between them are multipart but not journaled. Never
  infer resumability from the multipart boundary.
- **SQLite is pinned to one connection.** A transaction held across network I/O
  stalls every query in the process.

---

## 7. Commits

### 7.1 Format

One line. Plain English. Lowercase. Imperative or simple present. No body, no
description, no trailing period, no scope prefix, no issue reference, no
generated-by footer.

Good:

```
add tdx1 wire parser and formatter
fix part plan overflow guard for large files
move argon2 parameter bounds into the vault package
write implementation plan document
```

Not acceptable:

```
feat(wire): add parser

Adds a parser for the TDX1 format with tests.
```

### 7.2 Author

Every commit:

```bash
git -c user.name=prieyan -c user.email=mnprieyan@gmail.com commit -m "<message>"
```

### 7.3 Never push

`git push` is not run by anyone working from these instructions. Commits stay
local. Branch off `main`; do not commit directly to `main` for feature work.

### 7.4 Parallel commits

Independent file groups are committed by parallel Sonnet-low agents, one agent
per group. Rules for each agent:

- Stage only the paths assigned to it. Never `git add -A`.
- Git serializes on `.git/index.lock`. Retry on a held lock; do not delete the
  lock file.
- Never amend, rebase or reset another agent's commit.
- Never push.
- Report the resulting commit hash and subject back to the monitoring task.

A monitoring task tracks every agent and reports which commits landed and which
did not.

---

## 8. Before you call something done

```bash
gofmt -l .
go vet -tags=gtk3 ./...
go test -tags=gtk3 ./...
```

Plus, from `frontend/` when the view layer changed:

```bash
npm run typecheck
npm run lint
npm run test
```

Then check by hand:

- No comment spans more than one line.
- No new package violates §3.1.
- Every new dependency arrives through a constructor.
- Every new port is declared by its consumer.
- Any new document is in `docs/`.
- The commit message is one line and the author is correct.

Report what actually ran. If tests failed, say so and include the output. If a
step was skipped, say which and why. "It compiles" is not "it works".

---

## 9. Working agreements

- Finish the whole task. If part of it is blocked, complete everything else and
  say explicitly what was left and why.
- Do not widen scope. Fix the thing asked for; note adjacent problems rather
  than fixing them uninvited.
- Do not add a dependency without a reason that survives being written down.
  The dependency list in `butterDBCore.md` is deliberate: `modernc.org/sqlite`
  is chosen over cgo SQLite specifically to keep cross-compilation viable on
  Android and iOS.
- When the specification and an implementation disagree, the specification is
  right until someone changes it deliberately and records why in `docs/`.
