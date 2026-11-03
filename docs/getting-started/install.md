# Install

## From a release

Download the archive for your platform from the project's GitHub releases page, then check it against `SHA256SUMS` and extract it. Each archive holds the binary, the changelog and the third-party license notices.

=== "Linux and macOS"

    ```sh
    sha256sum --check --ignore-missing SHA256SUMS
    tar -xzf aiculator_*.tar.gz
    sudo mv aiculator_*/aiculator /usr/local/bin/
    aiculator --version
    ```

=== "Windows"

    ```powershell
    Get-FileHash aiculator_*.zip -Algorithm SHA256
    Expand-Archive aiculator_*.zip
    ```

    Then add the extracted folder to your `PATH`.

!!! note "Windows is compiled, not yet run"
    Windows and FreeBSD builds compile, but no one has run them yet. There is no native sandbox on Windows, so sandboxed work needs Docker.

## From source

You need Go at the version in `go.mod`, and git.

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/aiculator ./cmd/aiculator
export PATH="$PWD/bin:$PATH"
```

## Put it on your PATH

Skills call `aiculator` by name, so agents can only use them if it is on your `PATH`.

## What else you need

| Tool | When |
|---|---|
| git | Always |
| A harness | To do any work, such as Claude Code or OpenCode |
| A sandbox | To run checks and workers. macOS has one built in. On Linux, install `bubblewrap`. Elsewhere, use Docker. |
