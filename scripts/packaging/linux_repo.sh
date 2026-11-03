#!/bin/bash
# Builds .deb, .rpm and .apk packages for one release, and adds them to signed apt, dnf and
# apk repositories in SITE. A site that already has packages keeps its recent versions.
#
# usage: linux_repo.sh TAG RENDERED_DIR ASSET_DIR SITE
# env:   PACKAGE_GPG_KEY   armored private key without a passphrase, for apt and dnf
#        APK_SIGNING_KEY   RSA private key in PEM form, for apk
#        KEEP_VERSIONS     how many versions to keep (default 5)
# Needs: nfpm, gpg, apt-ftparchive, createrepo_c, openssl and docker.
set -euo pipefail
TAG=$1 RENDERED=$(cd "$2" && pwd) ASSETS=$(cd "$3" && pwd)
mkdir -p "$4"
SITE=$(cd "$4" && pwd)
KEEP=${KEEP_VERSIONS:-5}
VERSION=${TAG#v}

: "${PACKAGE_GPG_KEY:?PACKAGE_GPG_KEY is required}"
: "${APK_SIGNING_KEY:?APK_SIGNING_KEY is required}"

WORK=$(mktemp -d)
trap 'gpgconf --homedir "$WORK/gnupg" --kill all 2>/dev/null || true; rm -rf "$WORK"' EXIT
export GNUPGHOME="$WORK/gnupg"
mkdir -m 700 "$GNUPGHOME"
umask 077
printf '%s\n' "$PACKAGE_GPG_KEY" > "$WORK/gpg.key"
printf '%s\n' "$APK_SIGNING_KEY" > "$WORK/apk.key"
umask 022
gpg --batch --quiet --import "$WORK/gpg.key"
KEY_ID=$(gpg --batch --list-secret-keys --with-colons | awk -F: '$1 == "sec" { print $5; exit }')
[ -n "$KEY_ID" ] || { echo "PACKAGE_GPG_KEY holds no private key" >&2; exit 1; }
GPG_KEY_FILE="$WORK/gpg.key" APK_KEY_FILE="$WORK/apk.key"

mkdir -p "$SITE/apt/pool" "$SITE/rpm/packages" "$SITE/apk/x86_64" "$SITE/apk/aarch64"
for arch in amd64 arm64; do
  STAGE="$WORK/stage-$arch"
  mkdir -p "$STAGE"
  tar -xzf "$ASSETS/aiculator_${TAG}_linux_${arch}.tar.gz" -C "$STAGE" --strip-components=1
  # apk fetches NAME-VERSION.apk, so the apk file below must have exactly that name.
  # nfpm does not expand variables in its contents list, so fill in the paths here.
  config="$WORK/nfpm-$arch.yaml"
  sed -e "s|\${STAGE}|$STAGE|g" -e "s|\${GPG_KEY_FILE}|$GPG_KEY_FILE|g" -e "s|\${APK_KEY_FILE}|$APK_KEY_FILE|g" \
    "$RENDERED/nfpm/$arch.yaml" > "$config"
  nfpm package --config "$config" --packager deb --target "$SITE/apt/pool/"
  nfpm package --config "$config" --packager rpm --target "$SITE/rpm/packages/"
  [ "$arch" = amd64 ] && apk_arch=x86_64 || apk_arch=aarch64
  nfpm package --config "$config" --packager apk --target "$SITE/apk/$apk_arch/aiculator-$VERSION.apk"
done

python3 "$(dirname "$0")/prune.py" "$KEEP" "$SITE/apt/pool" "$SITE/rpm/packages" "$SITE/apk/x86_64" "$SITE/apk/aarch64"
for f in "$SITE/apt/pool/"* "$SITE/rpm/packages/"* "$SITE/apk/"*/*.apk; do
  [ -f "$f" ] && [ "$(stat -c %s "$f")" -lt $((100 * 1024 * 1024)) ] \
    || { echo "$f is missing or over GitHub's 100 MB file limit" >&2; exit 1; }
done

# apt: one suite, "stable", with one component, "main".
(
  cd "$SITE/apt"
  rm -rf dists
  for arch in amd64 arm64; do
    mkdir -p "dists/stable/main/binary-$arch"
    apt-ftparchive --arch "$arch" packages pool > "dists/stable/main/binary-$arch/Packages"
    gzip -9nkf "dists/stable/main/binary-$arch/Packages"
  done
  apt-ftparchive \
    -o APT::FTPArchive::Release::Origin=Aiculator \
    -o APT::FTPArchive::Release::Label=Aiculator \
    -o APT::FTPArchive::Release::Suite=stable \
    -o APT::FTPArchive::Release::Codename=stable \
    -o APT::FTPArchive::Release::Architectures="amd64 arm64" \
    -o APT::FTPArchive::Release::Components=main \
    release dists/stable > "$WORK/Release"
  mv "$WORK/Release" dists/stable/Release
  gpg --batch --yes --default-key "$KEY_ID" --clearsign -o dists/stable/InRelease dists/stable/Release
  gpg --batch --yes --default-key "$KEY_ID" --armor --detach-sign -o dists/stable/Release.gpg dists/stable/Release
)

# dnf: one repository, since dnf picks the architecture from each package.
rm -rf "$SITE/rpm/repodata"
createrepo_c --quiet "$SITE/rpm"
gpg --batch --yes --default-key "$KEY_ID" --armor --detach-sign -o "$SITE/rpm/repodata/repomd.xml.asc" "$SITE/rpm/repodata/repomd.xml"

# apk: its index format and signature come from Alpine's own tools. Pinned, because this
# writes the index format that clients read.
docker run --rm -v "$SITE/apk:/apk" -v "$WORK/apk.key:/run/apk.key:ro" alpine:3.20 sh -euc '
  apk add --quiet --no-progress abuild
  for arch in x86_64 aarch64; do
    cd "/apk/$arch"
    rm -f APKINDEX.tar.gz
    apk index --quiet --allow-untrusted -o APKINDEX.tar.gz *.apk
    abuild-sign -q -k /run/apk.key -p aiculator.rsa.pub APKINDEX.tar.gz
  done
  chown -R '"$(id -u):$(id -g)"' /apk
'

# What clients need to trust the repositories.
gpg --batch --yes --armor --export "$KEY_ID" > "$SITE/aiculator.asc"
gpg --batch --yes --export "$KEY_ID" > "$SITE/aiculator.gpg"
openssl rsa -in "$WORK/apk.key" -pubout -out "$SITE/aiculator.rsa.pub" 2>/dev/null
touch "$SITE/.nojekyll"
echo "built $VERSION into $SITE"
