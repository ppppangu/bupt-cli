#!/usr/bin/env python3
"""Build an offline release containing the exact programs and notices shipped."""

import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo

ROOT = Path(__file__).resolve().parent.parent
DIST = ROOT / "dist"
TARGETS = ("darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64")
NOTICE_FILES = {"LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT", "PATENTS"}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def go_packages(go, target):
    system, arch = target.split("-")
    env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0")
    output = subprocess.check_output(
        [go, "list", "-deps", "-json=Dir,Standard,Module", "./cmd/bupt"],
        cwd=ROOT, env=env, text=True,
    )
    decoder = json.JSONDecoder()
    pos = 0
    while pos < len(output):
        if output[pos].isspace():
            pos += 1
            continue
        package, pos = decoder.raw_decode(output, pos)
        yield package


def third_party_notices(go):
    goroot = Path(subprocess.check_output([go, "env", "GOROOT"], cwd=ROOT, text=True).strip())
    toolchain = subprocess.check_output([go, "env", "GOVERSION"], cwd=ROOT, text=True).strip()
    std = "Go " + toolchain
    dirs = set()
    for target in TARGETS:
        for pkg in go_packages(go, target):
            mod = pkg.get("Module") or {}
            if pkg.get("Standard"):
                label, root = std, goroot
            elif mod and not mod.get("Main"):
                label, root = mod["Path"] + "@" + mod["Version"], Path(mod["Dir"])
            else:
                continue
            current = Path(pkg["Dir"])
            current.relative_to(root)
            while True:
                dirs.add((label, root, current))
                if current == root:
                    break
                current = current.parent

    notices = {}
    for label, root, directory in dirs:
        for file in directory.iterdir():
            name = file.name.upper().split(".", 1)[0]
            if name in NOTICE_FILES and file.is_file():
                notices[(label, file.relative_to(root).as_posix())] = file.read_bytes()
    # Homebrew keeps the toolchain license next to libexec.
    if (std, "LICENSE") not in notices:
        notices[(std, "LICENSE")] = (goroot.parent / "LICENSE").read_bytes()
    for label in {entry[0] for entry in dirs}:
        if not any(
            item == label and "/" not in name and name.upper().split(".", 1)[0]
            in {"LICENSE", "LICENCE", "COPYING"} for item, name in notices
        ):
            raise ValueError(f"Missing license for {label}")
    parts = [b"Third-party licenses and notices for the bundled executables\n"]
    for (label, name), content in sorted(notices.items()):
        parts.append(f"\n===== {label} / {name} =====\n\n".encode())
        parts.append(content.rstrip(b"\n") + b"\n")
    return b"".join(parts)


def write_zip(destination, folder, files):
    # Fixed metadata and ordering also work on machines with different umasks.
    with ZipFile(destination, "w", compression=ZIP_DEFLATED) as archive:
        for name, data in sorted(files.items()):
            info = ZipInfo(f"{folder}/{name}", date_time=(1980, 1, 1, 0, 0, 0))
            info.create_system = 3
            mode = 0o755 if name == "install.sh" or name.startswith("bin/") else 0o644
            info.external_attr = (stat.S_IFREG | mode) << 16
            archive.writestr(info, data, compress_type=ZIP_DEFLATED, compresslevel=9)


def main():
    version = os.environ.get("VERSION") or (ROOT / "VERSION").read_text().strip()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*", version):
        raise SystemExit("Invalid release version")
    go = os.environ.get("GO", "go")
    folder = f"bupt-{version}"
    zip_name = folder + ".zip"
    DIST.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".package-", dir=DIST) as temp:
        stage = Path(temp)
        subprocess.run(
            ["sh", str(ROOT / "scripts/build.sh"), str(stage)], cwd=ROOT,
            env=dict(os.environ, GO=go, VERSION=version), stdout=subprocess.DEVNULL, check=True,
        )
        files = {
            "VERSION": (version + "\n").encode(),
            "install.sh": (ROOT / "packaging/install.sh").read_bytes(),
            "使用说明.txt": (ROOT / "packaging/使用说明.txt").read_bytes(),
            "bupt.1": (ROOT / "docs/bupt.1").read_bytes(),
            "THIRD-PARTY-NOTICES.txt": third_party_notices(go),
        }
        binaries = [f"bupt-{target}" for target in TARGETS]
        for name in binaries:
            files["bin/" + name] = (stage / name).read_bytes()
        files["SHA256SUMS"] = "".join(
            f"{digest(data)}  {name}\n" for name, data in sorted(files.items())
        ).encode()
        write_zip(stage / zip_name, folder, files)
        sidecar = zip_name + ".sha256"
        (stage / sidecar).write_text(f"{digest((stage / zip_name).read_bytes())}  {zip_name}\n")
        # The online installer uses the same verification and atomic install as
        # the offline bundle, with downloads pinned to this release.
        release_url = f"https://github.com/ppppangu/bupt-cli/releases/download/v{version}"
        sums = "\n".join(f"{digest(files['bin/' + name])}  bin/{name}" for name in binaries)
        installer = files["install.sh"].decode()
        marker = "# @online-release@"
        if installer.count(marker) != 1:
            raise ValueError("Missing online installer template marker")
        (stage / "install.sh").write_text(installer.replace(marker, f"release_url='{release_url}'\nrelease_sums='{sums}'"))
        # Keep a concurrent build from interleaving the two sets of release files.
        lock = os.open(DIST, os.O_RDONLY)
        try:
            fcntl.flock(lock, fcntl.LOCK_EX)
            for name in [*binaries, "SHA256SUMS", "install.sh", zip_name, sidecar]:
                (stage / name).replace(DIST / name)
        finally:
            os.close(lock)
    print(DIST / zip_name)
    print(DIST / sidecar)


if __name__ == "__main__":
    main()
