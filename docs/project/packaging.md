# Package managers

Each release runs `.github/workflows/packages.yml`. Every channel installs the real package first, then publishes it if the release is not a prerelease and the channel's secrets are set. A channel with no secrets is skipped with a notice, and one channel failing does not stop the others.

!!! info "Current status"
    Homebrew, Scoop, apt, dnf and apk are live. WinGet is waiting on the review of its first pull request. Chocolatey and the Arch User Repository need the setup below, so their install commands do not work yet.

## Channels

| Channel | How it publishes | Secrets | Needs first |
|---|---|---|---|
| Homebrew | Commits to your tap | `PACKAGING_TOKEN` | Repository `homebrew-aiculator` |
| Scoop | Commits to your bucket | `PACKAGING_TOKEN` | Repository `scoop-aiculator` |
| apt, dnf, apk | Signed repositories on GitHub Pages | `PACKAGING_TOKEN`, `PACKAGE_GPG_KEY`, `APK_SIGNING_KEY` | Repository `aiculator-packages`, with Pages on |
| Chocolatey | `choco push` | `CHOCOLATEY_API_KEY` | A community.chocolatey.org account |
| WinGet | Pull request to `microsoft/winget-pkgs` | `WINGET_TOKEN` | Nothing |
| Arch User Repository | Pushes `aiculator-bin` | `AUR_SSH_KEY` | An aur.archlinux.org account with this public key |

apt, dnf and apk have no central registry that accepts a project directly, so these repositories are hosted by the project. Getting into Debian, Fedora or Alpine themselves means going through each distribution's own review.

Two channels have human review. Chocolatey moderates package versions before listing them, and each WinGet version is a pull request that is reviewed before it merges. Both can take days, while the other channels are live after the workflow finishes.

## One-time setup

### Repositories

```sh
gh repo create Grano22/homebrew-aiculator --public --add-readme
gh repo create Grano22/scoop-aiculator --public --add-readme
gh repo create Grano22/aiculator-packages --public --add-readme
gh api -X POST repos/Grano22/aiculator-packages/pages \
  -f build_type=legacy -f 'source[branch]=master' -f 'source[path]=/'
```

The Pages source for `aiculator-packages` must be **Deploy from a branch**, on `master` at `/`, which is what `build_type=legacy` sets. With the **GitHub Actions** source, a publish succeeds but the URLs return 404.

### Tokens

- **`PACKAGING_TOKEN`:** a fine-grained token, limited to the three repositories above, with **Contents: read and write**.
- **`WINGET_TOKEN`:** a classic token, because it must fork and open a pull request on a repository you do not own. See [wingetcreate's token guide](https://github.com/microsoft/winget-create/blob/main/doc/token.md) for its scope.
- **`CHOCOLATEY_API_KEY`:** from your account page on community.chocolatey.org.

### Signing keys

Generate these on a trusted machine. They have no passphrase, because CI has to use them unattended, so the repository secret is what protects them.

```sh
export GNUPGHOME=$(mktemp -d)
gpg --batch --passphrase '' --quick-generate-key "Aiculator packages" ed25519 sign 5y
gpg --batch --armor --export-secret-keys | gh secret set PACKAGE_GPG_KEY -R Grano22/aiculator
openssl genrsa 4096 | openssl pkcs8 -topk8 -nocrypt | gh secret set APK_SIGNING_KEY -R Grano22/aiculator
ssh-keygen -t ed25519 -N '' -C aiculator-aur -f aur_ed25519
gh secret set AUR_SSH_KEY -R Grano22/aiculator < aur_ed25519
```

Add `aur_ed25519.pub` to your AUR account, then delete both files and `$GNUPGHOME`.

Keep a safe copy of the GPG and apk keys if you want the option to restore them. Replacing either key later means every user has to trust the new one.

## Try it without publishing

```sh
gh workflow run packages.yml -R Grano22/aiculator -f dry_run=true            # The latest release
gh workflow run packages.yml -R Grano22/aiculator -f tag=v0.4.0 -f dry_run=false   # Retry a release for real
```

A dry run installs from every channel using throwaway signing keys. Pull requests that change the packaging run one automatically. Publishing again is safe, since each channel notices a version it already has.

## Install, once published

=== "macOS and Linux"

    ```sh
    brew install grano22/aiculator/aiculator
    ```

    Homebrew asks you to trust third-party taps. See its [tap docs](https://docs.brew.sh/How-to-Create-and-Maintain-a-Tap).

=== "Windows"

    ```powershell
    scoop bucket add aiculator https://github.com/Grano22/scoop-aiculator
    scoop install aiculator

    choco install aiculator

    winget install Grano22.Aiculator
    ```

=== "Debian and Ubuntu"

    ```sh
    sudo install -d /etc/apt/keyrings
    curl -fsSL https://grano22.github.io/aiculator-packages/aiculator.gpg | sudo tee /etc/apt/keyrings/aiculator.gpg > /dev/null
    echo "deb [signed-by=/etc/apt/keyrings/aiculator.gpg] https://grano22.github.io/aiculator-packages/apt stable main" | sudo tee /etc/apt/sources.list.d/aiculator.list
    sudo apt update && sudo apt install aiculator
    ```

=== "Fedora and RHEL"

    ```sh
    sudo tee /etc/yum.repos.d/aiculator.repo > /dev/null <<'REPO'
    [aiculator]
    name=Aiculator
    baseurl=https://grano22.github.io/aiculator-packages/rpm
    enabled=1
    gpgcheck=1
    repo_gpgcheck=1
    gpgkey=https://grano22.github.io/aiculator-packages/aiculator.asc
    REPO
    sudo dnf install aiculator
    ```

=== "Alpine"

    ```sh
    wget -q https://grano22.github.io/aiculator-packages/aiculator.rsa.pub -O /etc/apk/keys/aiculator.rsa.pub
    echo https://grano22.github.io/aiculator-packages/apk >> /etc/apk/repositories
    apk update && apk add aiculator
    ```

=== "Arch"

    ```sh
    yay -S aiculator-bin
    ```

## Limits

- **Windows on Arm:** Chocolatey installs the x64 build. Scoop and WinGet have native arm64 builds.
- **Old versions:** the apt, dnf and apk repositories keep the newest 5 versions, and keep no git history, so that they stay within GitHub Pages' size limits.
- **macOS signing:** the binaries are not notarized by Apple.
