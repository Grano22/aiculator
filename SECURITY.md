# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's **Report a vulnerability** button on the Security tab. Do not open a public issue.

## What the design protects

- **Sandboxed workers** (`aiculator drive`) can write only to their worktree and a private temp directory. They cannot read the swarm's admin token or signing key, or credential stores such as `~/.ssh`.
- **Agent identity** comes from signed tokens, so an agent cannot act as another agent or approve its own work.
- **Config files** written by `aiculator join` and `mcp add` hold no swarm tokens.

## What it does not protect

- A session joined by hand runs outside the sandbox, under its harness's own permissions. It can read the admin token, as you can.
- The server listens only on localhost by default and is meant for one trusted user. It is not hardened for a shared network.
- Skills and templates give instructions to a model. They are not technical limits.
