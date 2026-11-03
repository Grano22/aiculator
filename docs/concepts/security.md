# Security

## The sandbox

`aiculator drive` and all acceptance checks run sandboxed. `auto` picks Seatbelt on macOS and bubblewrap on Linux, and falls back to Docker. If none is available, it fails: running without a sandbox must be asked for by name, with `--sandbox none`.

| A sandboxed command | Can it? |
|---|---|
| Write to its worktree and private temp directory | Yes |
| Write anywhere else, including the repository's `.git` | No |
| Read the swarm's admin token and signing key | No |
| Read `~/.ssh`, `~/.aws`, `~/.gnupg` and similar | No |
| Inherit your environment variables | No, only what the manifest names |
| Use the network | Workers yes, because models need it. Checks no. |

!!! note "How well each sandbox is tested"
    Seatbelt was attacked for real, including escapes through symlinks. For bubblewrap and Docker, only the arguments have been checked so far.

## Identity

Agent tokens are signed with ed25519. Tier and identity come from the token, so an agent cannot claim a higher tier or act as another agent. No token can be issued for the swarm's own `system:` identities.

## Verification rules

These are enforced by the state machine and modeled in TLA+:

- A submitter cannot review its own work.
- A task reaches `done` only after someone other than its submitter has verified it.
- A reviewer cannot approve work whose checks have not passed.
- An agent with an old lease cannot change a task.

## Untrusted text

Notes from other agents and check output are marked as untrusted when shown to a model. This reduces prompt injection, but it cannot rule it out, which is one more reason results are checked by running them.

## What is not protected

!!! danger "A session joined by hand is not sandboxed"
    It runs under your harness's own permissions, and it can read the admin token, as you can. Treat it as you would yourself. Only checks run sandboxed in that case.

- The server is for one trusted user on localhost, and is not hardened for a shared network.
- Harness packages run with npx are pinned to a version, but you are still trusting what that version does.

See `SECURITY.md` in the repository for how to report a vulnerability.
