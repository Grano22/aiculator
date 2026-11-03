# MCP config

`aiculator join` sets up MCP for you. These commands give you direct control, for one harness or any config file.

## Add

```sh
aiculator mcp add                               # Every installed harness
aiculator mcp add --harness opencode --tier 2   # By id
aiculator mcp add --harness claude,cursor --global
aiculator mcp add --harness ~/.local/bin/opencode   # Found from its executable
aiculator mcp add --harness ./custom/mcp.json   # A config file; format inferred
aiculator mcp add --harness opencode --dry-run  # Show it; change nothing
```

`--harness` accepts `auto`, `all`, harness ids, a path to a harness's executable, or a path to a config file. For a config file, the format comes from its name: `.toml` means Codex's format, `opencode*` means OpenCode's, and anything else is the common `mcpServers` JSON. Override it with `--format`.

## Print the config piece

To add it by hand, print it. This changes nothing.

```console
$ aiculator mcp print --harness opencode
# opencode: add to opencode.json
{
  "mcp": {
    "servers": {
      "swarm": {
        "command": [
          "aiculator",
          "mcp",
          "--auto",
          "--tier",
          "1",
          "--harness",
          "opencode"
        ],
        "timeout": {
          "execution": 60000
        },
        "type": "local"
      }
    }
  }
}
```

## Any other server

These commands manage any MCP server, not only the swarm's. Give it a name and put its command after `--`:

```sh
aiculator mcp add --name docs --harness claude -- npx -y docs-server
aiculator mcp print --name docs --harness codex -- npx -y docs-server
aiculator mcp add --name api --env API_URL=https://x --harness cursor -- api-server
```

!!! warning "`--env` values are stored in plain text"
    Do not put real secrets in a file you commit.

## Remove

```sh
aiculator mcp remove --harness opencode                # The swarm's entry
aiculator mcp remove --harness opencode --name docs    # A named one
```

## Safety

- **The swarm's entry holds no token.** The bridge, `aiculator mcp --auto`, gets its own when it starts, so these files are safe to commit.
- **Your other settings stay.** Each change is merged, and the previous file is saved in `.swarm/backups/`.
- **A file it cannot understand is left alone.** That includes invalid JSON and `opencode.jsonc`, which may hold comments. You get the piece to paste instead.
- **Timeouts are set for you.** The `wait_for_task` call can hold for up to 40 seconds, so the entry allows 60.
