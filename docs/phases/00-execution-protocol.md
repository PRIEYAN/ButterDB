# Phase 00 — Execution protocol

> Audience: the Opus orchestrator (high effort) that executes one phase document, and the owner who launches it.
> Binding inputs: [decisions.md](../decisions.md) (D19, E11, E12, E14), [claude.md](../claude.md) §12. This document wins on how a phase is executed; the phase document wins on what is built.

This is the runbook for executing any `docs/phases/NN-<slug>.md`. It turns one phase
document into commits on `main` through a prelude, parallel lanes in separate git
worktrees, an integration lane and a verification pass. Sections 3 to 13 are the steps,
in order. Do not skip a step because it looks unnecessary for a small phase. Sections 15
to 18 explain the rules the steps rely on.

Every cross-reference in this document is to a section number (§9), never to a step
number.

---

## 1. Roles, agents and fixed values

### 1.1 Roles

| Role | Who | Model and effort | Writes | Commits |
| --- | --- | --- | --- | --- |
| Owner | the human | n/a | answers gates and owner inputs | no; merges the documentation set to `main` once, before phase 01 |
| Orchestrator | the Claude Code session the owner opens | Opus, high | prelude, module files, tidy, status note; integration only when the phase document assigns it to the orchestrator | on the phase branch only |
| Lane worker | `butterdb-lane-worker` | Sonnet, high | its lane's owned paths only | on its lane branch only |
| Git monitor | `butterdb-git-monitor` | Sonnet, low | nothing | never |
| Lane reviewer | `butterdb-lane-reviewer` | Sonnet, high | nothing | never |

The agent definitions are `.claude/agents/butterdb-lane-worker.md`,
`.claude/agents/butterdb-lane-reviewer.md` and `.claude/agents/butterdb-git-monitor.md`.
Claude Code loads agent definitions when a session starts, so they must be committed on
`main` before the orchestrator session opens.

The orchestrator never edits a file a lane owns, not even to fix a typo. It re-dispatches
the lane instead (§8.3).

### 1.2 Fixed values

| Name | Value |
| --- | --- |
| Repository (main checkout) | `/home/prieyan/weeb/butterDB` |
| Worktree root | `/home/prieyan/weeb/butterdb-wt`, which is `../butterdb-wt` from the repository |
| TDrive reference, read only | `/home/prieyan/weeb/TDrive` (module `TDrive`, MIT, `LICENSE` at its root) |
| Module path | `github.com/PRIEYAN/ButterDB` (D20) |
| Commit identity | `prieyan <mnprieyan@gmail.com>` as author and committer |
| Commit command | `git -c user.name=prieyan -c user.email=mnprieyan@gmail.com commit -m "<one line>"` |
| Phase slug | the phase file name without `NN-` and `.md`; `docs/phases/03-storage-engine.md` has slug `storage-engine` |
| Phase branch | `phase/NN-<slug>`, for example `phase/03-storage-engine` |
| Lane token | the lane ID in lower case, `l1` to `l6`; `int` for the integration lane |
| Lane branch | `phase/NN-<slug>-<lane>`, for example `phase/03-storage-engine-l4` |
| Lane worktree | `/home/prieyan/weeb/butterdb-wt/NN-<lane>`, for example `/home/prieyan/weeb/butterdb-wt/03-l4` |
| Retry branch | `phase/NN-<slug>-<lane>-r<k>`, `k` is 1 or 2 |
| Retry worktree | `/home/prieyan/weeb/butterdb-wt/NN-<lane>-r<k>` |
| Status note | `docs/phases/status/NN-<slug>.md` |

Lane branches join the lane token with a hyphen, never a slash. Git cannot hold both
`phase/03-storage-engine` and `phase/03-storage-engine/l4`, because one ref name cannot
be a file and a directory at once.

When a phase document's lanes table names a different branch or worktree for a lane, the
table wins. Otherwise the values above apply.

### 1.3 Environment for every Go command

| Module | Prefix | Mode |
| --- | --- | --- |
| Root, `github.com/PRIEYAN/ButterDB` | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0` | `-mod=readonly` everywhere except the prelude `go get` and the tidy step |
| Root, race tests only | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=1` | `-mod=readonly -race` |
| `apps/admin`, `apps/transformd` | `GOTOOLCHAIN=local CGO_ENABLED=1 go -C apps/<module>` | workspace mode, `-mod=readonly` |

`GOTOOLCHAIN=local` stops the go command from downloading a different toolchain in the
middle of a phase. Default (owner may change). Shell state does not survive between Bash
calls, so every command carries `cd <absolute path> &&` or `git -C <absolute path>`.

### 1.4 Commit message rules

Every commit made under this protocol, by any role:

- one line; no body; no trailers (no `Co-Authored-By`, no `Signed-off-by`, no `(cherry picked from ...)`, no generated-by footer);
- simple English, all lower case, no trailing period;
- no scope prefix (`feat:`, `fix(wire):`), no issue reference;
- at most 72 characters. Default (owner may change);
- made with the commit command in §1.2, never with `--amend`, never through an editor, never with `--no-verify`.

Messages the orchestrator uses when the phase document gives none:

| Event | Message |
| --- | --- |
| Prelude without a message in the phase document | `add phase NN prelude contracts` |
| Module added mid-phase (§16.4) | `add missing module requirement for phase NN` |
| Tidy after integration (§10.3) | `tidy module requirements for phase NN` |
| Status note (§13.1, §15) | `record phase NN status` |

Nothing is ever pushed: not the phase branch, not a lane branch, not a tag.

---

## 2. Launching a phase (owner)

### 2.1 One-time setup before phase 01

1. Commit the complete documentation set (`docs/` and `.claude/agents/`) and merge it
   into `main`. Phase 01 pre-flight (§4) refuses to start when a phase document or an
   agent definition is not tracked on `main`.
2. Create the worktree root: `mkdir -p /home/prieyan/weeb/butterdb-wt`.
3. Confirm Go 1.26 or later (`go version`) and a C compiler (`cc --version`). The race
   detector needs cgo for test binaries even though the server builds with
   `CGO_ENABLED=0`.

### 2.2 Opening the orchestrator session

From a terminal:

```bash
cd /home/prieyan/weeb/butterDB
git switch main
mkdir -p ../butterdb-wt
claude --model opus --effort high --add-dir ../butterdb-wt --add-dir ../TDrive
```

`--add-dir` lets the session and every agent it spawns edit the worktrees and read
TDrive without a permission prompt per file. In a session that is already open, set the
model and effort with:

```
/model opus
/effort high
```

Do not start the session with `claude -w` or `--worktree`. This protocol creates its own
worktrees; a session inside a Claude-managed worktree puts the phase branch in the wrong
checkout.

Parallel workers stall if every `go` and `git` command waits for a permission prompt.
Either approve the prompts as they come, or rely on the allow rules that the phase 01
prelude writes into `.claude/settings.json` (§5.5).

### 2.3 What to type

To start a phase, type exactly, with the real file name:

```
Execute docs/phases/NN-<slug>.md per docs/phases/00-execution-protocol.md
```

For example `Execute docs/phases/01-foundation.md per docs/phases/00-execution-protocol.md`.

To resume a phase that stopped:

```
Resume docs/phases/NN-<slug>.md from docs/phases/status/NN-<slug>.md per docs/phases/00-execution-protocol.md
```

The owner stays reachable while a phase runs. The orchestrator stops and asks at every
owner input (§4), every owner gate (§12) and every failure (§15).

---

## 3. Step 1 — Read

The orchestrator reads, completely and in this order:

1. `docs/phases/00-execution-protocol.md` (this file).
2. `docs/decisions.md`.
3. `docs/claude.md`.
4. `docs/phases/NN-<slug>.md`.
5. Every file listed in section 3 of the phase document that the prelude or the
   integration lane depends on. Lane workers read their own lanes' references.

From the phase document it extracts and keeps in its working notes:

| Item | Source in the phase document |
| --- | --- |
| `NN`, slug, title | file name and `#` heading |
| Entry criteria and owner inputs | section 2 |
| Prelude `go get` commands, contract files, commit message | section 4 |
| Lanes in table order, owned paths, contracts each lane depends on | section 5 table |
| Each lane brief, verbatim | sections 5.1 to 5.6 |
| Integration brief and who runs it | section 6 |
| Verification commands, race package list, acceptance test names | section 7 |
| Owner gates and when each is asked | section 8 |

The row order of the section 5 table is the cherry-pick order. Record it. When a lane's
owned paths use brace notation (`internal/wire/{grammar.go,parse.go}`), expand it into
one path per line; every prompt in this protocol lists owned paths expanded.

If the phase document is missing a section the template requires, contradicts
`docs/decisions.md`, or gives two lanes an overlapping path, stop before pre-flight
(§15) and report the defect.

---

## 4. Step 2 — Pre-flight checks

Run every check. Any failure stops the phase before a branch exists; report the failing
check and its output to the owner.

| # | Check | Command | Pass condition |
| --- | --- | --- | --- |
| P1 | Repository root | `git -C /home/prieyan/weeb/butterDB rev-parse --show-toplevel` | prints `/home/prieyan/weeb/butterDB` |
| P2 | On main | `git -C /home/prieyan/weeb/butterDB branch --show-current` | prints `main` |
| P3 | Clean tree, untracked files included | `git -C /home/prieyan/weeb/butterDB status --porcelain --untracked-files=all` | empty |
| P4 | No operation in progress | `git -C /home/prieyan/weeb/butterDB rev-parse -q --verify CHERRY_PICK_HEAD`, the same for `MERGE_HEAD` and `REVERT_HEAD` | all three print nothing |
| P5 | Phase document on main | `git -C /home/prieyan/weeb/butterDB ls-files --error-unmatch docs/phases/NN-<slug>.md` | exit 0 |
| P6 | Agent definitions on main | `git -C /home/prieyan/weeb/butterDB ls-files --error-unmatch .claude/agents/butterdb-lane-worker.md .claude/agents/butterdb-lane-reviewer.md .claude/agents/butterdb-git-monitor.md` | exit 0 |
| P7 | No branches for this phase | `git -C /home/prieyan/weeb/butterDB branch --list 'phase/NN-*'` | empty, unless resuming (§15.2) |
| P8 | No worktrees for this phase | `git -C /home/prieyan/weeb/butterDB worktree list --porcelain` and `ls /home/prieyan/weeb/butterdb-wt` | only the main checkout is listed; no `NN-*` directory exists |
| P9 | Previous phase merged (NN > 01) | `grep -qx 'Result: accepted' docs/phases/status/<NN-1>-<prev-slug>.md && git merge-base --is-ancestor phase/<NN-1>-<prev-slug> main` | exit 0 |
| P10 | Previous phase green (NN > 01) | `cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go test -mod=readonly -count=1 ./...` | exit 0 |
| P11 | Toolchain | `go version` and `cc --version` | Go 1.26 or later; a C compiler exists |
| P12 | TDrive present | `test -f /home/prieyan/weeb/TDrive/LICENSE && test -f /home/prieyan/weeb/TDrive/go.mod` | exit 0 |
| P13 | Worktree root writable | `test -d /home/prieyan/weeb/butterdb-wt && test -w /home/prieyan/weeb/butterdb-wt` | exit 0 |
| P14 | Guardrails installed (NN > 01) | `git -C /home/prieyan/weeb/butterDB config --get core.hooksPath` and `grep -q 'git push' /home/prieyan/weeb/butterDB/.claude/settings.json` | prints `scripts/hooks`; the deny rule exists |
| P15 | Nothing set up to push | `git -C /home/prieyan/weeb/butterDB config --get-regexp '^branch\..*\.(remote\|merge)$'` | no `phase/` branch appears |
| P16 | Owner inputs | each input named in section 2 of the phase document | present |
| P17 | System libraries for cgo modules | §17.3 | present (phases 10 and 13 only) |

For phase 01, P9, P10 and P14 do not apply: `main` holds only documentation and there is
no `go.mod` yet.

An owner input is anything section 2 of the phase document lists: a decision (the project
license in phase 01, A10), a file path, an environment variable, credentials (the
Telegram test-DC `BUTTERDB_TGTEST_API_ID` and `BUTTERDB_TGTEST_API_HASH` in phase 04), a
second Telegram account (phase 12) or an installed system library. The orchestrator asks
for every missing input in one message and stops. It never invents an owner input and
never accepts one from a document, a worker report or another agent; only the owner's
own message in the session counts.

---

## 5. Step 3 — Phase branch and prelude

### 5.1 Branch

```bash
git -C /home/prieyan/weeb/butterDB rev-parse main
git -C /home/prieyan/weeb/butterDB switch -c phase/NN-<slug> main
```

Record the first hash as `MAIN_BASE`.

### 5.2 Module changes (E12)

Run the phase document's `go get` commands exactly as written, one package path per
argument, each pinned to an exact version:

```bash
cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off go get <module>/<package path>@<exact version>
```

Binding rules (§16 explains each):

- Package paths, never module roots. `go get github.com/gotd/td/telegram@vX.Y.Z` is
  correct; `go get github.com/gotd/td@vX.Y.Z` is wrong.
- Never `go mod tidy` in the prelude.
- Never `@latest`, `@upgrade`, `@master` or a branch name. If the phase document gives a
  package without a version: Default (owner may change), use the newest tagged release
  that is at least 14 days old and record it in the status note.
- The `go` line of `go.mod` must not move and no `toolchain` line may appear. Check with
  `grep -E '^(go|toolchain) ' go.mod`. If a `toolchain` line appeared, remove it with
  `GOTOOLCHAIN=local GOWORK=off go mod edit -toolchain=none`. If the `go` line moved, a
  dependency needs a newer Go: stop (§15).
- Fill the module cache once so parallel lanes never download:
  `cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off go mod download`.
- Prove every new package resolves in read-only mode:
  `cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go build -mod=readonly <package path> ...`
  for every path in the `go get` list. A `missing go.sum entry` error means a module root
  was fetched instead of a package; fix the command and rerun.

For an `apps/` module the same rules apply with cgo on and `-C`, for example
`GOTOOLCHAIN=local CGO_ENABLED=1 GOWORK=off go -C apps/admin get fyne.io/fyne/v2/app@<exact version>`.
See §17.

### 5.3 Contract files

Write each contract file from section 4 of the phase document exactly as given: package
clause, imports, types, interfaces, sentinel errors, constants and full signatures. A
prelude contract never contains a function body a lane is meant to implement. Contract
files obey the comment policy like any other Go file.

Every file the prelude creates belongs to the prelude for the rest of the phase. No lane
edits it; a lane that needs it changed reports a contract defect (§8.3).

### 5.4 Prelude checks and commit

```bash
cd /home/prieyan/weeb/butterDB && test -z "$(gofmt -l .)"
cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go build -mod=readonly ./...
cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go vet -mod=readonly ./...
cd /home/prieyan/weeb/butterDB && scripts/layering.sh && scripts/nocomments.sh
cd /home/prieyan/weeb/butterDB && git add -- go.mod go.sum <each contract file>
cd /home/prieyan/weeb/butterDB && git diff --cached --name-only
cd /home/prieyan/weeb/butterDB && git -c user.name=prieyan -c user.email=mnprieyan@gmail.com commit -m "<prelude message from the phase document>"
cd /home/prieyan/weeb/butterDB && git rev-parse HEAD
```

The two scripts exist from phase 02 onward; in phase 01 skip that line. Stage by explicit
path only, never `git add -A`, `git add .` or `git commit -a`. The staged list must equal
the prelude's file list exactly.

Record the last hash as `PRELUDE`. A phase without a prelude commits nothing and
`PRELUDE` equals `MAIN_BASE`.

### 5.5 Phase 01 prelude

Phase 01 has no `go.mod` to amend; its prelude creates the repository's guardrails. The
phase 01 document is authoritative for each file's content. The orchestrator creates:

| File | Content (defaults where phase 01 is silent) |
| --- | --- |
| `go.mod` | `module github.com/PRIEYAN/ButterDB` and `go 1.26`, created with `GOTOOLCHAIN=local GOWORK=off go mod init github.com/PRIEYAN/ButterDB` then `GOWORK=off go mod edit -go=1.26 -toolchain=none` |
| `go.work` | `go 1.26` and `use .`; phases 10 and 13 add the `apps/` modules (§17) |
| `.gitignore` | as given by phase 01 |
| `CLAUDE.md` | the single line `Read docs/claude.md before doing anything in this repository.` |
| `.claude/settings.json` | see below |
| `scripts/hooks/commit-msg` and `scripts/install-hooks.sh` | as given by phase 01 |
| `docs/legal/THIRD_PARTY_NOTICES.md` | TDrive's MIT notice copied verbatim from `/home/prieyan/weeb/TDrive/LICENSE` |

Recommended `.claude/settings.json`, Default (owner may change):

```json
{
  "includeCoAuthoredBy": false,
  "permissions": {
    "additionalDirectories": ["../butterdb-wt", "../TDrive"],
    "deny": ["Bash(git push:*)", "Bash(git -C * push:*)"],
    "allow": [
      "Bash(go build:*)", "Bash(go vet:*)", "Bash(go test:*)", "Bash(go list:*)",
      "Bash(gofmt:*)", "Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)",
      "Bash(git show:*)", "Bash(git add:*)", "Bash(git commit:*)", "Bash(git -C:*)",
      "Bash(scripts/layering.sh)", "Bash(scripts/nocomments.sh)", "Bash(scripts/verify.sh)"
    ]
  }
}
```

A permission rule matches the command's text, so a rule cannot stop every spelling of a
push. The hard stop is a hook. Default (owner may change): phase 01 also commits
`scripts/hooks/pre-push` that prints `butterdb: pushing is disabled` and exits 1, so a
push fails in every checkout and every worktree. The deny entry for `git -C ... push`
uses wildcard rule syntax; on a Claude Code release without it, the hook still holds.

After the phase 01 prelude commit, the orchestrator runs
`cd /home/prieyan/weeb/butterDB && scripts/install-hooks.sh` once. It sets
`core.hooksPath` to `scripts/hooks` in the repository config, which every worktree
shares. Because the path is relative, each worktree runs the hooks from its own checkout
of `scripts/hooks`, which the lanes inherit from `PRELUDE`. This is the only git config
change this protocol makes, and the orchestrator makes it, never a worker.

---

## 6. Step 4 — One worktree per lane

For each lane, in table order:

```bash
git -C /home/prieyan/weeb/butterDB worktree add -b phase/NN-<slug>-<lane> /home/prieyan/weeb/butterdb-wt/NN-<lane> <PRELUDE>
```

Then confirm, for each:

```bash
git -C /home/prieyan/weeb/butterdb-wt/NN-<lane> rev-parse HEAD
git -C /home/prieyan/weeb/butterdb-wt/NN-<lane> branch --show-current
git -C /home/prieyan/weeb/butterdb-wt/NN-<lane> status --porcelain --untracked-files=all
```

`HEAD` must equal `PRELUDE`, the branch must be the lane branch and the status must be
empty. The integration lane's worktree is created later, in §10.1, from the commit that
holds every picked lane.

Never pass `--force` to `git worktree add`. A refusal means a stale branch or worktree
exists, which is a pre-flight failure (§4, P7 and P8).

---

## 7. Step 5 — Spawn the lane workers in parallel

### 7.1 One message, one Agent call per lane

Spawn every lane in a single assistant message that holds one Agent tool call per lane.
Calls in one message run concurrently; calls in separate messages run one after another.
Each call sets:

| Field | Value |
| --- | --- |
| `subagent_type` | `butterdb-lane-worker` |
| `model` | `sonnet` |
| `description` | `phase NN lane <Lx> <lane name>` |
| `prompt` | the template in §7.2 |

Effort comes from the agent definition's frontmatter (`effort: high`). Never use a
built-in worktree isolation option of the Agent tool; the worktree named in the prompt is
the isolation. Never run lanes as background tasks that outlive the orchestrator's turn.

### 7.2 Worker prompt template

Copy the lane brief from the phase document verbatim, from its `### 5.x Lane Lx` heading
to the line before the next `###` or `##` heading. Do not summarise, reorder, correct or
extend it. Anything the orchestrator adds goes in the header, never inside the brief.

```
PHASE: NN <title>
PHASE DOCUMENT: docs/phases/NN-<slug>.md
LANE: <Lx> <lane name>
WORKTREE: /home/prieyan/weeb/butterdb-wt/NN-<lane>
BRANCH: phase/NN-<slug>-<lane>
BASE COMMIT: <PRELUDE>
MAIN REPOSITORY (never touch): /home/prieyan/weeb/butterDB
TDRIVE (read only): /home/prieyan/weeb/TDrive
OWNED PATHS:
- <path, one per line, braces expanded>
CONTRACT FILES (import, never edit):
- <prelude file, one per line, or "none">
ATTEMPT: 1 of 3
PREVIOUS ATTEMPT BRANCH: none
FINDINGS TO FIX: none

----- LANE BRIEF (verbatim from docs/phases/NN-<slug>.md) -----
<verbatim brief>
----- END LANE BRIEF -----
```

On a retry (§8.3) `ATTEMPT`, `PREVIOUS ATTEMPT BRANCH` and `FINDINGS TO FIX` are filled
in and the brief stays verbatim.

### 7.3 Waiting and checking the result

The Agent calls in the message return when the last worker finishes. While they run the
orchestrator does nothing in the repository: no commits, no checkouts, no `git gc`, no
`git prune`, no `git worktree prune`.

When every call has returned:

1. The main checkout is untouched:
   `git -C /home/prieyan/weeb/butterDB status --porcelain --untracked-files=all` is
   empty, `git -C /home/prieyan/weeb/butterDB branch --show-current` prints the phase
   branch and `git -C /home/prieyan/weeb/butterDB rev-parse HEAD` equals `PRELUDE`. Any
   difference means a worker wrote outside its worktree: stop (§15).
2. Each worker returned a report in the format of the worker definition. A missing or
   malformed report, an agent error, `RESULT: PARTIAL` or `RESULT: BLOCKED` is a failed
   attempt for that lane.
3. Each worktree is clean and its branch moved:
   `git -C <worktree> status --porcelain --untracked-files=all` is empty and
   `git -C <worktree> rev-list --count <PRELUDE>..HEAD` is at least 1.
4. The commits the worker reported match `git -C <worktree> log --format='%h %s' <PRELUDE>..HEAD`.

A lane that fails check 3 or 4 is a failed attempt even when it reported `DONE`.

---

## 8. Step 6 — Monitor, review and re-dispatch

### 8.1 One message, two Agent calls per lane

For every lane whose worker returned `RESULT: DONE` and passed §7.3, spawn in one message
both agents for every such lane:

| Agent | `subagent_type` | `model` | Effort (from frontmatter) | Prompt |
| --- | --- | --- | --- | --- |
| Git monitor | `butterdb-git-monitor` | `sonnet` | low | monitor template |
| Reviewer | `butterdb-lane-reviewer` | `sonnet` | high | reviewer template |

Monitor template:

```
REPOSITORY: /home/prieyan/weeb/butterDB
WORKTREE: /home/prieyan/weeb/butterdb-wt/NN-<lane>
BRANCH: phase/NN-<slug>-<lane>
BASE COMMIT: <base of this attempt>
OWNED PATHS:
- <path, one per line>
WORKER REPORTED COMMITS:
- <hash> <subject>
```

Reviewer template:

```
PHASE DOCUMENT: docs/phases/NN-<slug>.md
LANE: <Lx> <lane name>
WORKTREE: /home/prieyan/weeb/butterdb-wt/NN-<lane>
BRANCH: phase/NN-<slug>-<lane>
BASE COMMIT: <base of this attempt>
TDRIVE (read only): /home/prieyan/weeb/TDrive
OWNED PATHS:
- <path, one per line>
CONTRACT FILES:
- <prelude file, one per line, or "none">
ATTEMPT: <k> of 3
PREVIOUS FINDINGS: <the findings this attempt had to fix, or "none">
WORKER REPORT:
<the worker's report, verbatim>

----- LANE BRIEF (verbatim from docs/phases/NN-<slug>.md) -----
<verbatim brief>
----- END LANE BRIEF -----
```

`<base of this attempt>` is `PRELUDE`, or the new phase head for an attempt started after
a mid-phase module commit (§16.4). For a retry in the same worktree the base stays the
original base, so the reviewer sees the whole lane, not only the fixes.

### 8.2 When a lane is accepted

A lane is accepted only when the monitor returns `RESULT: PASS` and the reviewer returns
`VERDICT: ACCEPT`. Findings marked `minor` on an accepted lane go into the status note's
deviations; they do not block.

### 8.3 Re-dispatch policy

A failed lane (worker not `DONE`, §7.3 failure, monitor `FAIL` or reviewer `REJECT`) is
re-dispatched at most twice. Attempt 3 is the last. After a third failure the phase stops
(§15) and no lane is picked.

| Failure | Retry runs in |
| --- | --- |
| Reviewer `REJECT`, or worker `PARTIAL`, with the monitor at `PASS` or not yet run | the same worktree and branch; the worker adds fix commits on top |
| Monitor `FAIL`: wrong author or committer, bad message, foreign path, module file, merge commit | a fresh branch `phase/NN-<slug>-<lane>-r<k>` and worktree `/home/prieyan/weeb/butterdb-wt/NN-<lane>-r<k>` from the attempt's base, because a bad commit can only be fixed by rewriting and nobody rewrites |
| Worker `BLOCKED` with `MISSING MODULE` | §16.4, then a fresh `-r<k>` branch from the new phase head |
| Worker `BLOCKED` with `CONTRACT DEFECT` or `SPEC CONFLICT` | no retry; the phase stops after the other lanes return (§15) |

A fresh retry branch:

```bash
git -C /home/prieyan/weeb/butterDB worktree add -b phase/NN-<slug>-<lane>-r<k> /home/prieyan/weeb/butterdb-wt/NN-<lane>-r<k> <base>
```

The failed branch and worktree stay as evidence. The retry prompt names the failed branch
under `PREVIOUS ATTEMPT BRANCH` so the worker can reuse good files with
`git -C <new worktree> show <failed branch>:<path>` and commit them correctly itself.

`FINDINGS TO FIX` carries the reviewer's findings and the monitor's failures verbatim,
keeping the reviewer's numbering. Retries for several lanes go in one message, in
parallel, and each is followed by a fresh monitor and reviewer pass (§8.1).

---

## 9. Step 7 — Cherry-pick accepted lanes

Only after every lane is accepted. In the main checkout, on the phase branch, one lane at
a time, in the order of the section 5 table:

```bash
git -C /home/prieyan/weeb/butterDB rev-list --reverse --no-merges <base>..<accepted branch>
git -C /home/prieyan/weeb/butterDB -c user.name=prieyan -c user.email=mnprieyan@gmail.com cherry-pick <base>..<accepted branch>
```

`<base>` is the base of the accepted attempt. Cherry-pick keeps each lane commit's author
and message; the `-c` flags make `prieyan` the committer too. Never pass `-x`, `-s`, `-e`,
`--edit` or `--allow-empty`: the first three add a trailer or open an editor, the last
hides an ownership violation.

After each lane:

```bash
git -C /home/prieyan/weeb/butterDB status --porcelain --untracked-files=all
cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go build -mod=readonly ./...
cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go vet -mod=readonly ./...
```

### 9.1 Conflict policy

Lanes own disjoint paths, so a correct phase cherry-picks without a conflict. A conflict,
or a pick that stops because a commit became empty, proves that two lanes touched the same
path or a lane touched a prelude file. That is an ownership violation:

1. `git -C /home/prieyan/weeb/butterDB cherry-pick --abort`.
2. Find the overlap: compare `git diff --name-only <base>..<lane branch>` of the failing
   lane with the same list for every lane already picked and with the prelude's files.
3. Stop the phase (§15). Never resolve by hand, never take `ours` or `theirs`, never edit
   another lane's file to make it fit.

### 9.2 Build failure after a clean pick

A build or vet failure after a clean pick means two lanes collided inside one Go package
(a duplicate identifier) or a lane used a contract wrongly. If every failing line is in
one lane's owned paths and that lane has attempts left, re-dispatch that lane (§8.3) with
the compiler output as findings, review it again, then pick only its new commits with
`git cherry-pick <last picked commit of that lane's branch>..<lane branch>`. Otherwise
stop (§15).

Record `LANES_HEAD` as `git -C /home/prieyan/weeb/butterDB rev-parse HEAD` after the last
lane.

---

## 10. Step 8 — Integration lane

The integration lane owns `internal/core`, `cmd/`, `test/acceptance/phaseNN` and anything
else section 6 of the phase document assigns. Unless the phase document says otherwise it
also owns the phase's rows in `docs/legal/THIRD_PARTY_NOTICES.md`: the orchestrator passes
it the `PORTED` lists of every lane report so each ported TDrive file is attributed (D18).

### 10.1 Run by a worker (default)

```bash
git -C /home/prieyan/weeb/butterDB worktree add -b phase/NN-<slug>-int /home/prieyan/weeb/butterdb-wt/NN-int <LANES_HEAD>
```

Spawn one `butterdb-lane-worker` (`model: sonnet`) with the §7.2 template:
`LANE: INT integration`, `BASE COMMIT: <LANES_HEAD>`, owned paths from section 6, and
section 6 of the phase document verbatim as the brief. Add, in the header outside the
verbatim block:

```
PORTED BY LANES:
- <Lx>: <TDrive path> -> <butterDB path>
```

Monitor, review, re-dispatch and cherry-pick follow §8 and §9 with `LANES_HEAD` as the
base and `phase/NN-<slug>-int` as the lane branch.

### 10.2 Run by the orchestrator

When section 6 says the orchestrator runs integration, it works on the phase branch in the
main checkout under the worker's rules: owned paths only, explicit staging, the commit
command, the commit messages section 6 gives. It still runs the monitor and the reviewer,
with `LANES_HEAD` as the base and `phase/NN-<slug>` as the branch.

### 10.3 Tidy, the integration lane's last step

E12 gives tidying to the integration lane, and claude.md §3 rule 8 forbids every worker,
the integration worker included, to edit module files. The tidy step therefore belongs to
the integration lane but is executed by the orchestrator, on the phase branch, after the
integration commits are picked:

```bash
cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off go mod tidy
cd /home/prieyan/weeb/butterDB && GOTOOLCHAIN=local GOWORK=off go mod verify
cd /home/prieyan/weeb/butterDB && grep -E '^(go|toolchain) ' go.mod
cd /home/prieyan/weeb/butterDB && git diff --stat -- go.mod go.sum
```

The `go` line must be unchanged and no `toolchain` line may appear (§5.2). Every
requirement the prelude added must survive. If tidy removed one, no file of the phase
imports it: record a phase document defect in the status note. When the phase touched an
`apps/` module, repeat with `GOTOOLCHAIN=local CGO_ENABLED=1 GOWORK=off go -C apps/<module> mod tidy`.

If any module file changed:

```bash
cd /home/prieyan/weeb/butterDB && git add -- go.mod go.sum
cd /home/prieyan/weeb/butterDB && git -c user.name=prieyan -c user.email=mnprieyan@gmail.com commit -m "tidy module requirements for phase NN"
```

Add `go.work.sum`, `apps/<module>/go.mod` and `apps/<module>/go.sum` to the `git add`
when they changed. Nothing else is staged in this commit.

---

## 11. Step 9 — Verification

Run on the phase branch in the main checkout, in this order. Every command must exit 0.
Record each command, its result and, on failure, the first 50 lines of output.

| # | Check | Command (run in `/home/prieyan/weeb/butterDB`) |
| --- | --- | --- |
| V1 | Formatting | `test -z "$(gofmt -l .)"` |
| V2 | Static build | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go build -mod=readonly ./...` |
| V3 | Server binary | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go build -mod=readonly -trimpath -o /dev/null ./cmd/butterdbd` |
| V4 | Vet | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go vet -mod=readonly ./...` |
| V5 | Tests | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go test -mod=readonly -count=1 ./...` |
| V6 | Race tests | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=1 go test -mod=readonly -race -count=1 <packages from section 7 of the phase document>`; when section 7 lists none, `./internal/...` |
| V7 | Module integrity | `GOTOOLCHAIN=local GOWORK=off go mod verify` |
| V8 | Layering | `scripts/layering.sh` |
| V9 | Comment policy | `scripts/nocomments.sh` |
| V10 | Phase acceptance | `GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 go test -mod=readonly -count=1 -v ./test/acceptance/phaseNN/...` |
| V11 | `apps/` modules, when present | `GOTOOLCHAIN=local CGO_ENABLED=1 go -C apps/<module> vet -mod=readonly ./...` and `GOTOOLCHAIN=local CGO_ENABLED=1 go -C apps/<module> test -mod=readonly -count=1 ./...` |
| V12 | Phase gate script | `scripts/verify.sh` |
| V13 | Commit hygiene | the four commands below |
| V14 | Clean tree | `git status --porcelain --untracked-files=all` |

V13 commands:

```bash
git -C /home/prieyan/weeb/butterDB log --format='%H|%an <%ae>|%cn <%ce>|%s' <MAIN_BASE>..HEAD
git -C /home/prieyan/weeb/butterDB log --format='%H %b' <MAIN_BASE>..HEAD
git -C /home/prieyan/weeb/butterDB rev-list --merges <MAIN_BASE>..HEAD
git -C /home/prieyan/weeb/butterDB log --format='%s' <MAIN_BASE>..HEAD | grep -nE '[A-Z]|\.$|^[a-z]+(\([^)]*\))?!?: |^.{73,}$'
```

Pass conditions not covered by an exit code:

- V5 includes every earlier phase's acceptance package, so it is the regression run.
- V10: every test name that section 7 of the phase document maps must appear as `--- PASS`
  in the verbose output. A listed test reported as `--- SKIP` fails unless section 7
  marks it as gated (the Telegram test-DC smoke test behind build tag `tgtest` and
  `BUTTERDB_TGTEST=1`); a gated skip is recorded in the status note.
- V13: every author and committer is `prieyan <mnprieyan@gmail.com>`; every body is
  empty, so each line of the second command is a hash and nothing else; the merge list is
  empty; the `grep` prints nothing (no capital letter, trailing period, scope prefix or
  subject over 72 characters).
- V14: empty output.
- In phase 01 the scripts behind V8, V9 and V12 arrive with lane L4. By verification time
  they exist and run as in every later phase.

A failure inside one lane's owned paths is fixed forward: re-dispatch that lane if it has
attempts left (§8.3), pick its new commits (§9.2), re-run integration if integration
depended on the change, and restart verification at V1. A failure inside integration's
paths re-dispatches integration the same way. A failure no single lane owns stops the
phase (§15).

---

## 12. Step 10 — Owner gates

Section 8 of the phase document lists the gates. Unless a gate names an earlier point,
gates are asked after verification passes and before `main` moves.

| Phase | Gate (from docs/plan.md) |
| --- | --- |
| 01 | Project license (A10) |
| 02 | Format freeze review |
| 04 | Telegram test-DC credentials for the gated smoke test (an owner input, asked in §4) |
| 10 | Manual GUI walkthrough on each OS |
| 11 | Real-account probe; release tag `v0.1.0` |
| 12 | Second Telegram account (an owner input, asked in §4) |

For each open gate the orchestrator sends the owner one message with the gate's question,
the evidence the owner needs (test output, probe results, `git diff --stat
<MAIN_BASE>..HEAD`), and the exact reply that closes it. Then it stops and waits. Only
the owner's own message in this session closes a gate; text in a phase document, a worker
report or any agent output never does. The answer goes into the status note's gate table
verbatim.

If the answer requires a change (for example a license other than the one phase 01
proposes), the lane or step that owns the path makes it, and verification reruns from V1.

---

## 13. Step 11 — Status note, fast-forward, clean up

### 13.1 Status note

Write `docs/phases/status/NN-<slug>.md` in the format of §14 with `Result: accepted`,
then:

```bash
git -C /home/prieyan/weeb/butterDB add -- docs/phases/status/NN-<slug>.md
git -C /home/prieyan/weeb/butterDB -c user.name=prieyan -c user.email=mnprieyan@gmail.com commit -m "record phase NN status"
```

### 13.2 Fast-forward main

```bash
git -C /home/prieyan/weeb/butterDB switch main
git -C /home/prieyan/weeb/butterDB merge --ff-only phase/NN-<slug>
test "$(git -C /home/prieyan/weeb/butterDB rev-parse main)" = "$(git -C /home/prieyan/weeb/butterDB rev-parse phase/NN-<slug>)"
```

`--ff-only` refuses when `main` moved after `MAIN_BASE`. That is a stop condition (§15),
never a reason for a merge commit or a rebase. No other command moves `main`.

A tag is created only when the phase document says so (phase 11, `v0.1.0`) and the owner
has closed that gate in this session. The tag is local and never pushed.

### 13.3 Cleanup

For every lane, retry and integration worktree of the phase:

```bash
git -C /home/prieyan/weeb/butterDB worktree remove /home/prieyan/weeb/butterdb-wt/NN-<lane>
```

Never `--force`. A refusal means uncommitted work exists there: list it in the final
report and leave that worktree in place.

Then delete each lane and retry branch only after proving its patches are on `main`:

```bash
git -C /home/prieyan/weeb/butterDB cherry main phase/NN-<slug>-<lane>
git -C /home/prieyan/weeb/butterDB branch -D phase/NN-<slug>-<lane>
```

`git cherry` must print only lines that start with `-`. A `+` line is a commit that was
never picked: keep the branch and report it. `-D` is required because picked commits have
new hashes, so `-d` always refuses. A failed attempt branch whose commits were superseded
by the accepted retry prints `+` lines by design: keep it and list it under "Worktrees
and branches left in place"; the owner deletes it.

The phase branch `phase/NN-<slug>` is kept; the next phase's pre-flight P9 uses it.

Finish with `git -C /home/prieyan/weeb/butterDB worktree prune` and
`git -C /home/prieyan/weeb/butterDB worktree list`. The list shows only the main
checkout and any worktree kept above.

---

## 14. Status note and final report format

The status note is the durable record. The final chat report to the owner has the same
content plus the final `main` hash.

```
# Phase NN status — <title>

Result: accepted
Date: YYYY-MM-DD
Phase branch: phase/NN-<slug> @ <hash>
Main before: <MAIN_BASE>
Main after: <hash, or "unchanged">
Prelude commit: <PRELUDE, or "none">
Lanes head: <LANES_HEAD>
Stopped at: none
Resume point: none

## Lanes

| Lane | Accepted branch | Attempts | Monitor | Reviewer | Commits picked |
| --- | --- | --- | --- | --- | --- |
| L1 <name> | phase/NN-<slug>-l1 | 1 | PASS | ACCEPT | 3 |

## Commits on the phase branch

| Hash | Author | Subject |
| --- | --- | --- |

## Verification

| Check | Result | Note |
| --- | --- | --- |
| V1 gofmt | pass | |

## Evidence classes run

| Class | Run | Note |
| --- | --- | --- |
| Unit and table tests | yes | |
| faketg acceptance | yes | |
| Telegram test DC smoke | no | gated, BUTTERDB_TGTEST unset |

## Owner gates

| Gate | Question | Owner answer (verbatim) | Closed |
| --- | --- | --- | --- |

## Module changes

| Module | Package paths | Version | Added by |
| --- | --- | --- | --- |

## Ported TDrive files

| TDrive file | butterDB path | Lane |
| --- | --- | --- |

## Deviations and minor findings

## Phase document defects

## Open questions for the owner

## Worktrees and branches left in place
```

`Result` is `accepted` or `stopped`. When stopped, `Stopped at` names the section and the
reason, and `Resume point` names the exact section and lane to continue from. The
evidence classes are those of `docs/plan.md` §8.7; "compiles" is never reported as
"works".

---

## 15. Failure mid-phase

Stop conditions: a phase document defect found in §3, a pre-flight failure, a third
failed attempt for any lane, a contract defect or spec conflict reported by a worker, a
cherry-pick conflict or empty pick, a build failure after picking that no single lane
owns, a verification failure no single lane owns, a `go` line change, a worker change in
the main checkout, `main` moving during the phase, and anything this protocol does not
cover.

### 15.1 On a stop

1. Start nothing new. Spawn no agent. If agents are still running, let them return and
   record their reports.
2. If a cherry-pick is in progress, `git -C /home/prieyan/weeb/butterDB cherry-pick --abort`.
   Run nothing else that moves a ref.
3. Preserve every worktree and every branch exactly as it is: no `reset`, no
   `branch -D`, no `worktree remove`, no `stash`, no `clean`, no `checkout -- <path>`.
4. Write the status note (§14) with `Result: stopped`, `Stopped at: §<n> — <reason>`, the
   evidence, the state and attempt count of every lane, the base of every attempt and the
   resume point.
5. If the main checkout has no change other than the note, commit it with
   `record phase NN status`. Otherwise leave the note uncommitted and say so in the report.
6. Report to the owner and stop. `main` does not move.

A contract defect or a phase document defect is fixed by the owner on `main`, which
invalidates every lane's base. The phase then restarts from §3. The orchestrator removes
the old phase branches and worktrees only after the owner approves it in this session,
using `git worktree remove --force` and `git branch -D` on the exact names the status
note lists.

### 15.2 Resuming

On a resume prompt the orchestrator reads this file, the phase document and the status
note, then confirms with `git branch --list 'phase/NN-*'`, `git worktree list` and
`git log --oneline <MAIN_BASE>..phase/NN-<slug>` that the repository matches the note. If
it does, it continues from the recorded resume point with the same `PRELUDE`, bases,
attempt counts and lane order. If it does not, it reports the difference and stops.

---

## 16. Module files (E12)

### 16.1 Why exact package paths

`go get` on a module root adds the requirement but records `go.sum` hashes only for what
the root package needs. When the useful code lives in subpackages (gotd's
`github.com/gotd/td` is such a module), building a subpackage under `-mod=readonly` then
fails with `missing go.sum entry for module providing package`. `go get
<module>/<package>@<version>` records every hash needed to build that package and its
imports. The prelude's read-only build of each package path (§5.2) proves it.

### 16.2 Why never tidy in the prelude

After the prelude no source file imports the new packages, so `go mod tidy` deletes their
requirements and every lane then fails under `-mod=readonly`. Tidy runs once, after
integration, when the phase imports everything it needs (§10.3). A requirement that is
marked `// indirect` while a lane imports it directly does not break a read-only build;
tidy corrects the marker at the end.

### 16.3 Who may change module files

| File | Prelude (orchestrator) | Lane workers | Integration worker | Tidy and mid-phase add (orchestrator) |
| --- | --- | --- | --- | --- |
| `go.mod`, `go.sum` | yes | never | never | yes |
| `go.work`, `go.work.sum` | yes | never | never | yes |
| `apps/*/go.mod`, `apps/*/go.sum` | yes | never | never | yes |

Workers build and test with `-mod=readonly` and never run `go get`, `go mod tidy`,
`go mod edit`, `go work` or `go mod vendor`, never add a `replace`, and never set
`GOFLAGS=-mod=mod`.

### 16.4 A module missing mid-phase

A worker that needs a module the prelude did not add stops with `RESULT: BLOCKED` and
`MISSING MODULE: <package path>`. The orchestrator then:

1. waits for the other lanes to return;
2. on the phase branch, runs the exact-path `go get`, the cache fill and the read-only
   build check of §5.2, stages only module files and commits
   `add missing module requirement for phase NN`;
3. records the new head as the base for the blocked lane, and re-dispatches it as a
   fresh `-r<k>` attempt from that head (§8.3), which counts against its retries;
4. records the omission as a phase document defect in the status note.

The new commit touches only module files, so lanes based on `PRELUDE` still cherry-pick
cleanly on top of it.

---

## 17. cgo modules under apps/ and go.work (E14)

### 17.1 Layout

| Module | Directory | cgo | Commands |
| --- | --- | --- | --- |
| `github.com/PRIEYAN/ButterDB` | repository root | never | `GOWORK=off CGO_ENABLED=0 go ...` |
| `github.com/PRIEYAN/ButterDB/apps/admin` | `apps/admin` | yes (Fyne, OpenGL) | `CGO_ENABLED=1 go -C apps/admin ...` |
| `github.com/PRIEYAN/ButterDB/apps/transformd` | `apps/transformd` | yes (libvips through govips) | `CGO_ENABLED=1 go -C apps/transformd ...` |

`go.work` starts in phase 01 as `go 1.26` with `use .`. The phase 10 prelude runs
`GOTOOLCHAIN=local go work use ./apps/admin`; the phase 13 prelude runs
`GOTOOLCHAIN=local go work use ./apps/transformd`. Nothing else edits `go.work`.

Each `apps/<module>/go.mod` requires `github.com/PRIEYAN/ButterDB` at
`v0.0.0-00010101000000-000000000000` and carries
`replace github.com/PRIEYAN/ButterDB => ../..` (docs/plan.md §3), so `GOWORK=off`
builds of the apps (Dockerfiles, packaging) work without the workspace. The `apps/`
prelude creates both lines.

### 17.2 Rules

- Root-module commands always set `GOWORK=off`. The root module never imports `apps/`,
  so the workspace adds nothing, and `GOWORK=off` proves the root `go.sum` is complete on
  its own, exactly as a release build sees it.
- `./...` at the root matches only root-module packages in both modes; a nested module is
  never part of the root pattern. `GOWORK=off` is still mandatory for the reason above.
- `apps/` commands run in workspace mode so they compile against the working tree's
  `api/...` packages. `-mod=readonly` is accepted in workspace mode.
- `apps/` modules import only `github.com/PRIEYAN/ButterDB/api/...`; `scripts/layering.sh`
  enforces it.
- `go -C <dir>` is used instead of `cd`, so a command stays one line with an absolute
  repository path before it.
- A worker in a worktree runs the same commands inside its worktree. The worktree carries
  its own `go.work`, whose relative `use` paths resolve inside that worktree.

### 17.3 System libraries (pre-flight P17)

| Phase | Needs | Check |
| --- | --- | --- |
| 10 | C compiler; OpenGL and X11 development headers for Fyne | `cc --version` and `pkg-config --exists gl x11 xrandr xcursor xinerama xi xxf86vm` |
| 13 | C compiler; libvips with HEIF support; ffmpeg | `pkg-config --modversion vips libheif` and `ffmpeg -version` |

Package names differ by distribution; the check is what counts. A missing library is an
owner input: ask and stop (§4).

---

## 18. Why parallel commits are safe with worktrees

Each git worktree has its own working directory, its own `HEAD` and its own index
(`.git/worktrees/<name>/index`). The object database and the refs are shared. Every lane
commits to its own branch, so no two lanes write the same ref, and object writes are
content-addressed and atomic. Lanes therefore commit at the same moment without
coordination.

The shared-index collision is why this protocol never runs parallel agents in one
checkout. The earlier rule, `docs/claude.md` §7.4 as committed in `b73a0ec`, had several
agents commit from one working tree and retry on `.git/index.lock`. The lock serialises
each `git add` and each `git commit`, but not the sequence between them: agent A stages
its files, agent B stages its files into the same index, and agent A's `git commit`
records both sets under A's message. The lock prevented index corruption and nothing
else. Commits came out with foreign files under the wrong messages, and fixing them
needed history rewrites that nobody is allowed to make. With one index per worktree,
staging in one lane cannot reach another lane's commit. `docs/claude.md` §12.4 replaces
the old rule.

What is still shared, and the rule that protects it:

| Shared thing | Risk | Rule |
| --- | --- | --- |
| Object database | `git gc` or `git prune` deleting objects a running worker just wrote | nobody runs either during a phase |
| Worktree metadata | `git worktree prune` dropping an active worktree | only in §13.3 |
| Branch refs | two worktrees on one branch | git refuses it; each lane has its own branch |
| `refs/stash` | one worker's stash visible to another | nobody stashes |
| Repository config | a worker changing `user.*` or `core.hooksPath` for every lane | identity comes only from `-c` flags; only §5.5 changes config |
| Hooks through `core.hooksPath` | none; the relative path resolves to each worktree's own `scripts/hooks` | installed once in phase 01 |
| Go build and module caches | none; both are safe for concurrent use | the prelude fills the module cache (§5.2) |
| The main checkout | a worker writing to `/home/prieyan/weeb/butterDB` instead of its worktree | workers use absolute worktree paths only; §7.3 check 1 detects it |

Within one worktree a worker still stages by explicit path and checks
`git diff --cached --name-only` before each commit, because the monitor and the reviewer
judge every commit by the paths it touches.

---

## 19. Checklist

The orchestrator copies this list into its notes and ticks each item.

```
[ ] §3  read protocol, decisions, claude.md, phase doc; lane order recorded
[ ] §4  pre-flight P1-P17; owner inputs present
[ ] §5  branch phase/NN-<slug> from main; MAIN_BASE recorded
[ ] §5  go get by exact package path, no tidy, go line unchanged, cache filled, read-only build
[ ] §5  contract files written; gofmt, build, vet, scripts
[ ] §5  prelude committed; PRELUDE recorded
[ ] §6  one worktree per lane at /home/prieyan/weeb/butterdb-wt/NN-<lane>
[ ] §7  all lane workers spawned in one message
[ ] §7  main checkout untouched; reports parsed; worktrees clean
[ ] §8  monitor and reviewer for every lane in one message
[ ] §8  retries resolved, at most 2 per lane
[ ] §9  lanes cherry-picked in table order; build and vet after each; LANES_HEAD recorded
[ ] §10 integration done, monitored, reviewed, picked
[ ] §10 tidy run by the orchestrator; committed if changed
[ ] §11 verification V1-V14
[ ] §12 owner gates closed by the owner
[ ] §13 status note committed
[ ] §13 main fast-forwarded
[ ] §13 worktrees removed; branches deleted after git cherry check
[ ] §14 final report sent
[ ]     nothing pushed
```
