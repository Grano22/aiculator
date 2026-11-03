#!/usr/bin/env python3
"""Tests for render.py. Run: python3 scripts/packaging/render_test.py"""
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import prune  # noqa: E402
import render  # noqa: E402

TAG = "v1.2.3"
ASSETS = [f"aiculator_{TAG}_{o}_{a}.{'zip' if o == 'windows' else 'tar.gz'}" for o in ("linux", "darwin", "windows") for a in ("amd64", "arm64")]
SUMS = "".join(f"{i:064x}  {name}\n" for i, name in enumerate(ASSETS, 1))


class RenderTest(unittest.TestCase):
    def setUp(self):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        self.out = Path(d.name)
        self.names = render.render(TAG, SUMS, self.out)

    def read(self, rel):
        return (self.out / rel).read_text(encoding="utf-8")

    def test_every_channel_is_written(self):
        for rel in ("homebrew/aiculator.rb", "scoop/aiculator.json", "chocolatey/aiculator.nuspec", "chocolatey/tools/chocolateyinstall.ps1", "aur/PKGBUILD", "aur/.SRCINFO", "nfpm/amd64.yaml", "nfpm/arm64.yaml", "winget/Grano22.Aiculator.yaml", "winget/Grano22.Aiculator.installer.yaml", "winget/Grano22.Aiculator.locale.en-US.yaml"):
            self.assertIn(rel, self.names)

    def test_each_url_sits_beside_its_own_checksum(self):
        sums = dict(reversed(line.split()) for line in SUMS.splitlines())
        rb = self.read("homebrew/aiculator.rb")
        for name in ASSETS:
            if "windows" in name:
                continue
            url = f"{render.REPO}/releases/download/{TAG}/{name}"
            self.assertIn(f'url "{url}"\n      sha256 "{sums[name]}"', rb)
        scoop = json.loads(self.read("scoop/aiculator.json"))
        arm = scoop["architecture"]["arm64"]
        name = f"aiculator_{TAG}_windows_arm64.zip"
        self.assertEqual((arm["hash"], arm["extract_dir"]), (sums[name], name[:-4]))
        self.assertIn(sums[name].upper(), self.read("winget/Grano22.Aiculator.installer.yaml"))
        self.assertIn(f"Url64bit       = '{render.REPO}/releases/download/{TAG}/aiculator_{TAG}_windows_amd64.zip'", self.read("chocolatey/tools/chocolateyinstall.ps1"))

    def test_the_license_texts_ship_with_every_linux_package(self):
        for rel in ("nfpm/amd64.yaml", "nfpm/arm64.yaml", "aur/PKGBUILD"):
            for doc in ("LICENSE", "THIRD_PARTY_NOTICES.md"):
                self.assertIn(doc, self.read(rel), rel)
        self.assertIn('"THIRD_PARTY_NOTICES.md"', self.read("homebrew/aiculator.rb"))

    def test_versions_drop_the_v(self):
        self.assertIn('version "1.2.3"', self.read("homebrew/aiculator.rb"))
        self.assertIn("<version>1.2.3</version>", self.read("chocolatey/aiculator.nuspec"))
        self.assertIn("pkgver=1.2.3", self.read("aur/PKGBUILD"))

    def test_free_text_is_quoted_in_yaml(self):
        # A ": " in an unquoted value is invalid YAML, which only the real tools would catch.
        self.assertIn('description: "', self.read("nfpm/amd64.yaml"))
        self.assertIn('ShortDescription: "', self.read("winget/Grano22.Aiculator.locale.en-US.yaml"))

    def test_bad_input_is_refused(self):
        for tag in ("1.2.3", "v1.2", "v1.2.3;rm -rf /", "$(id)"):
            with self.assertRaises(ValueError, msg=tag):
                render.render(tag, SUMS, self.out)
        short = "".join(line + "\n" for line in SUMS.splitlines() if "windows_arm64" not in line)
        with self.assertRaisesRegex(ValueError, "windows_arm64"):
            render.render(TAG, short, self.out)


class PruneTest(unittest.TestCase):
    def test_keeps_the_newest_numerically_in_every_format(self):
        with tempfile.TemporaryDirectory() as d:
            names = []
            for v in ("0.9.0", "0.10.0", "0.11.0"):
                names += [f"aiculator_{v}_amd64.deb", f"aiculator_{v}_arm64.deb", f"aiculator-{v}-1.x86_64.rpm", f"aiculator-{v}.apk"]
            for n in names + ["APKINDEX.tar.gz", "other_0.1.0_amd64.deb"]:
                (Path(d) / n).touch()
            removed = prune.prune(d, 2)
            self.assertEqual(removed, sorted(n for n in names if "0.9.0" in n))
            self.assertTrue((Path(d) / "APKINDEX.tar.gz").exists())
            self.assertTrue((Path(d) / "other_0.1.0_amd64.deb").exists())
            self.assertEqual(prune.prune(d, 2), [])


if __name__ == "__main__":
    unittest.main()
