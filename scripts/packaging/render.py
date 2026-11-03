#!/usr/bin/env python3
"""Writes the package definitions for one release, from its SHA256SUMS.

Everything here comes from the tag and the checksums, so a definition can never point
at a file the release does not have. Nothing is published by this script.
"""
import argparse
import json
import re
from pathlib import Path

OWNER = "Grano22"
REPO = f"https://github.com/{OWNER}/aiculator"
SUMMARY = "AI task swarm that runs coding tasks across harnesses, with verification"
DESCRIPTION = (
    "Aiculator is a single binary that runs coding tasks across AI harnesses, with task "
    "tiers, sandboxes, verification, a merge queue, dashboards, skills and templates."
)
MAINTAINER = f"{OWNER} <{OWNER}@users.noreply.github.com>"
TAG_RE = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$")
# The dependency licenses require their texts to ship with the binary.
DOCS = ["LICENSE", "THIRD_PARTY_NOTICES.md", "README.md", "CHANGELOG.md"]


class Release:
    def __init__(self, tag, sums_text):
        if not TAG_RE.match(tag):
            raise ValueError(f"{tag} is not a semantic version")
        self.tag = tag
        self.version = tag[1:]
        self.sums = {}
        for line in sums_text.splitlines():
            if line.strip():
                digest, name = line.split()
                self.sums[name.lstrip("*")] = digest

    def stem(self, goos, arch):
        return f"aiculator_{self.tag}_{goos}_{arch}"

    def asset(self, goos, arch):
        name = self.stem(goos, arch) + (".zip" if goos == "windows" else ".tar.gz")
        if name not in self.sums:
            raise ValueError(f"{name} is not in SHA256SUMS")
        return name

    def url(self, goos, arch):
        return f"{REPO}/releases/download/{self.tag}/{self.asset(goos, arch)}"

    def sha(self, goos, arch):
        return self.sums[self.asset(goos, arch)]


def homebrew(r):
    def block(goos, arch):
        return f'      url "{r.url(goos, arch)}"\n      sha256 "{r.sha(goos, arch)}"\n'

    return f'''class Aiculator < Formula
  desc "AI task swarm that runs coding tasks across harnesses"
  homepage "{REPO}"
  version "{r.version}"
  license "MIT"

  on_macos do
    on_arm do
{block("darwin", "arm64")}    end
    on_intel do
{block("darwin", "amd64")}    end
  end

  on_linux do
    on_arm do
{block("linux", "arm64")}    end
    on_intel do
{block("linux", "amd64")}    end
  end

  uses_from_macos "git"

  def install
    bin.install "aiculator"
    doc.install {", ".join(f'"{d}"' for d in DOCS)}
  end

  test do
    assert_match version.to_s, shell_output("#{{bin}}/aiculator --version")
  end
end
'''


def scoop(r):
    def arch(a):
        return {"url": r.url("windows", a), "hash": r.sha("windows", a), "extract_dir": r.stem("windows", a)}

    return json.dumps(
        {
            "version": r.version,
            "description": SUMMARY,
            "homepage": REPO,
            "license": "MIT",
            "suggest": {"git": "git"},
            "architecture": {"64bit": arch("amd64"), "arm64": arch("arm64")},
            "bin": "aiculator.exe",
            "checkver": "github",
        },
        indent=2,
    ) + "\n"


def winget(r):
    ident = f"{OWNER}.Aiculator"
    schema = "1.12.0"
    common = f"PackageIdentifier: {ident}\nPackageVersion: {r.version}\n"
    end = lambda kind: f"ManifestType: {kind}\nManifestVersion: {schema}\n"

    def installer(goos_arch, winget_arch):
        return (
            f"  - Architecture: {winget_arch}\n"
            f"    InstallerUrl: {r.url('windows', goos_arch)}\n"
            f"    InstallerSha256: {r.sha('windows', goos_arch).upper()}\n"
            f"    NestedInstallerFiles:\n"
            f"      - RelativeFilePath: {r.stem('windows', goos_arch)}\\aiculator.exe\n"
            f"        PortableCommandAlias: aiculator\n"
        )

    return {
        f"{ident}.yaml": common + "DefaultLocale: en-US\n" + end("version"),
        f"{ident}.installer.yaml": common
        + "InstallerType: zip\nNestedInstallerType: portable\nInstallers:\n"
        + installer("amd64", "x64")
        + installer("arm64", "arm64")
        + end("installer"),
        f"{ident}.locale.en-US.yaml": common
        + f"PackageLocale: en-US\nPublisher: {OWNER}\nPublisherUrl: https://github.com/{OWNER}\n"
        + f"PackageName: Aiculator\nPackageUrl: {REPO}\nLicense: MIT\nLicenseUrl: {REPO}/blob/{r.tag}/LICENSE\n"
        + f"ShortDescription: {json.dumps(SUMMARY)}\nTags:\n  - ai\n  - agents\n  - cli\n  - swarm\n"
        + f"ReleaseNotesUrl: {REPO}/releases/tag/{r.tag}\n"
        + end("defaultLocale"),
    }


def chocolatey(r):
    nuspec = f'''<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2015/06/nuspec.xsd">
  <metadata>
    <id>aiculator</id>
    <version>{r.version}</version>
    <title>Aiculator</title>
    <authors>{OWNER}</authors>
    <owners>{OWNER}</owners>
    <projectUrl>{REPO}</projectUrl>
    <packageSourceUrl>{REPO}/tree/master/scripts/packaging</packageSourceUrl>
    <projectSourceUrl>{REPO}</projectSourceUrl>
    <docsUrl>https://grano22.github.io/aiculator/</docsUrl>
    <bugTrackerUrl>{REPO}/issues</bugTrackerUrl>
    <licenseUrl>{REPO}/blob/{r.tag}/LICENSE</licenseUrl>
    <requireLicenseAcceptance>false</requireLicenseAcceptance>
    <tags>ai agents cli swarm foss cross-platform</tags>
    <summary>{SUMMARY}</summary>
    <description>{DESCRIPTION}</description>
    <releaseNotes>{REPO}/releases/tag/{r.tag}</releaseNotes>
    <dependencies>
      <dependency id="git" />
    </dependencies>
  </metadata>
  <files>
    <file src="tools\\**" target="tools" />
  </files>
</package>
'''
    install = f'''$ErrorActionPreference = 'Stop'
$toolsDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$params = @{{
  PackageName    = $env:ChocolateyPackageName
  UnzipLocation  = $toolsDir
  Url64bit       = '{r.url("windows", "amd64")}'
  Checksum64     = '{r.sha("windows", "amd64")}'
  ChecksumType64 = 'sha256'
}}
Install-ChocolateyZipPackage @params
'''
    return {"aiculator.nuspec": nuspec, "tools/chocolateyinstall.ps1": install}


def aur(r):
    pkgbuild = f'''# Maintainer: {MAINTAINER}
pkgname=aiculator-bin
pkgver={r.version}
pkgrel=1
pkgdesc={json.dumps(SUMMARY)}
arch=('x86_64' 'aarch64')
url="{REPO}"
license=('MIT')
depends=('git')
provides=('aiculator')
conflicts=('aiculator')
options=('!strip' '!debug')
source_x86_64=("{r.url("linux", "amd64")}")
source_aarch64=("{r.url("linux", "arm64")}")
sha256sums_x86_64=('{r.sha("linux", "amd64")}')
sha256sums_aarch64=('{r.sha("linux", "arm64")}')

package() {{
  cd "aiculator_v${{pkgver}}_linux_"*
  install -Dm755 aiculator "$pkgdir/usr/bin/aiculator"
  install -Dm644 LICENSE "$pkgdir/usr/share/licenses/aiculator-bin/LICENSE"
  install -Dm644 THIRD_PARTY_NOTICES.md "$pkgdir/usr/share/licenses/aiculator-bin/THIRD_PARTY_NOTICES.md"
}}
'''
    srcinfo = f'''pkgbase = aiculator-bin
\tpkgdesc = {SUMMARY}
\tpkgver = {r.version}
\tpkgrel = 1
\turl = {REPO}
\tarch = x86_64
\tarch = aarch64
\tlicense = MIT
\tdepends = git
\tprovides = aiculator
\tconflicts = aiculator
\toptions = !strip
\toptions = !debug
\tsource_x86_64 = {r.url("linux", "amd64")}
\tsha256sums_x86_64 = {r.sha("linux", "amd64")}
\tsource_aarch64 = {r.url("linux", "arm64")}
\tsha256sums_aarch64 = {r.sha("linux", "arm64")}

pkgname = aiculator-bin
'''
    return {"PKGBUILD": pkgbuild, ".SRCINFO": srcinfo}


def nfpm(r, arch):
    """linux_repo.sh fills in the ${...} paths, once it has unpacked the release."""
    r.asset("linux", arch)  # Refuse an architecture the release does not have.
    docs = "".join(
        f"  - src: ${{STAGE}}/{d}\n    dst: /usr/share/doc/aiculator/{d}\n    file_info:\n      mode: 0644\n"
        for d in DOCS
    )
    return f'''name: aiculator
arch: {arch}
platform: linux
version: {r.version}
section: devel
priority: optional
maintainer: {MAINTAINER}
description: {json.dumps(SUMMARY)}
vendor: {OWNER}
homepage: {REPO}
license: MIT
depends:
  - git
contents:
  - src: ${{STAGE}}/aiculator
    dst: /usr/bin/aiculator
    file_info:
      mode: 0755
{docs}rpm:
  signature:
    key_file: ${{GPG_KEY_FILE}}
apk:
  signature:
    key_file: ${{APK_KEY_FILE}}
    key_name: aiculator.rsa.pub
'''


def render(tag, sums_text, out):
    r = Release(tag, sums_text)
    files = {
        "homebrew/aiculator.rb": homebrew(r),
        "scoop/aiculator.json": scoop(r),
        "nfpm/amd64.yaml": nfpm(r, "amd64"),
        "nfpm/arm64.yaml": nfpm(r, "arm64"),
    }
    files.update({f"winget/{k}": v for k, v in winget(r).items()})
    files.update({f"chocolatey/{k}": v for k, v in chocolatey(r).items()})
    files.update({f"aur/{k}": v for k, v in aur(r).items()})
    for rel, text in files.items():
        path = Path(out) / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        # Always LF, including on Windows runners, so checksums and diffs stay stable.
        with open(path, "w", encoding="utf-8", newline="\n") as f:
            f.write(text)
    return sorted(files)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--tag", required=True)
    p.add_argument("--sums", required=True, help="path to the release's SHA256SUMS")
    p.add_argument("--out", required=True)
    a = p.parse_args()
    for name in render(a.tag, Path(a.sums).read_text(encoding="utf-8"), a.out):
        print(name)


if __name__ == "__main__":
    main()
