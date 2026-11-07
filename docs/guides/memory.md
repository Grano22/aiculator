# Shared memory

Every agent that works on a codebase starts by finding its way around it: searching, opening files, reading code it will not change. In a swarm, each agent pays for that again on every task. Shared memory lets them pay once.

The server keeps two things for all its agents:

- **A code graph:** the files, the symbols they define, and how they connect. It is built from the integration branch and rebuilt after every merge. An agent asks it a question and reads a slice of a few hundred tokens instead of searching and opening files.
- **Notes:** short facts agents leave for each other, such as where something really lives, why it is done that way, or a trap to avoid. Notes are found again by the words they share with a question, or by the files they are about.

## Asking the code graph

```sh
aiculator code query "where are session tokens verified"
aiculator code symbol Authority.Verify
```

```text
Code graph: 12 matches for "where are session tokens verified", nearest first.
* Authority.Verify (method)  internal/auth/token.go:113
    Verify checks the signature and expiry and returns the claims.
    -> method_of Authority
    <- defines internal/auth/token.go
* maxVerified (value)  internal/auth/token.go:53
...
```

Each match comes with where it is and, from its doc comment, what it does. Then come the connections that matter: what defines it, what it is a method of, what imports or uses it. A connection marked `?` was inferred from names rather than parsed, so it is likely but not certain. Answers stop at a token budget, 1,500 by default.

Agents ask with the `code_query` and `code_symbol` MCP tools, or `aiculator work code` on the shell route. The skills tell them to ask before exploring.

### How much it saves

The repository's own tests measure it, with ten real questions about this codebase. Each question's answer is compared with what an agent would read without the graph: the one file that holds the answer, which is the best case, since finding that file costs more again.

| | |
|---|---|
| Answers that held the symbol asked about | 10 of 10 |
| Tokens per answer, on average | 424 |
| Tokens to read the file that holds the answer | 3,535 on average |
| Saving | 8.3× fewer tokens |

Run it yourself with `go test -v -run Quality ./internal/codegraph`. Notes add to this: a note recalled is a finding nobody pays to make again.

## Graphify or the built-in indexer

[Graphify](https://github.com/Graphify-Labs/Graphify) builds the graph with tree-sitter parsers for many languages, and it can add what a model finds in docs and papers. If it is installed, the server uses it:

```sh
uv tool install graphifyy   # or: pipx install graphifyy
```

The server runs `graphify update` on its own checkout of the integration branch after each merge, which re-extracts only the files that changed and needs no model. If you have built a graph yourself with `/graphify .`, the `graphify-out/graph.json` at the project root is used as the starting point, so what its model pass found is kept.

Without Graphify, a built-in indexer stands in. It needs nothing installed and indexes a repository of a few hundred files in about 100 ms. It finds definitions in these languages, with a pattern per language rather than a parser:

- Go, Python, JavaScript and TypeScript, Rust
- Java, Kotlin, C# and Scala, Ruby, PHP, C and C++
- Markdown headings

It reads each definition's doc comment. It links imports, links Go methods to their types across a package, and infers which files use a symbol defined elsewhere. It writes the graph in Graphify's format, so Graphify's own tools can read it.

| `aiculator serve --code-graph` | Meaning |
|---|---|
| `auto` | Graphify if installed, the built-in indexer otherwise. The default. |
| `graphify` | Graphify only. A failure is an error rather than a fallback. |
| `builtin` | The built-in indexer only. |
| `off` | No code graph. Notes still work. |

If Graphify fails in `auto` mode, the built-in graph stands in and `aiculator memory status` shows why.

## Notes

```sh
aiculator memory recall "token expiry" --file internal/auth/token.go
aiculator memory ls
aiculator memory forget n-1a2b3c4d5e6f
```

Agents write notes with `memory_note`, or `aiculator work note`. A note is 12 to 1,500 characters, with the files, symbols and tags it is about.

- **Repeats:** a note that says much the same as one already kept updates that one, so nobody reads the same thing twice.
- **Ranking:** recall uses BM25 over the note's words, files and symbols, plus weight for notes about the files the asker named.
- **Staleness:** when a merge changes a file a note is about, the note is marked stale. It may still be right, but it's worth checking. Writing it again makes it current.
- **Delivery with tasks:** a task comes with the notes that match it well, under `memory` in the grant, so agents get them without asking.
- **Storage:** notes are kept in the server's store and survive restarts. Past 5,000 notes, the least used of the oldest go first.

Notes are written by agents, so they are information, never instructions. The skills tell agents to treat them that way and not to note anything secret.

## Status

```sh
aiculator memory status
```

```text
code graph: 3097 nodes, 9043 edges, 227 files, built by builtin from 7309a98c 2m ago in 96ms
notes: 41, 3 stale
served: 318 code questions, 122 recalls, 207 notes, about 140211 tokens read
```

The web dashboard shows the same in its Shared memory card. `aiculator memory refresh` rebuilds the graph now, if the branch moved.
