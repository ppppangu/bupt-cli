#!/usr/bin/env python3
"""Verify the distributable archive and exercise the installer in isolation."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
from zipfile import ZipFile

ROOT = Path(__file__).resolve().parent.parent
PLATFORMS = {
    ("Darwin", "arm64"): "darwin-arm64",
    ("Darwin", "x86_64"): "darwin-amd64",
    ("Linux", "aarch64"): "linux-arm64",
    ("Linux", "x86_64"): "linux-amd64",
}


def check(ok, message):
    if not ok:
        raise SystemExit(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def verify_bundle(folder, version, work):
    name = f"bupt-{version}"
    archive = folder / (name + ".zip")
    checksum = (folder / (archive.name + ".sha256")).read_text()
    check(checksum == f"{sha(archive.read_bytes())}  {archive.name}\n", "ZIP checksum mismatch")
    files = {"VERSION", "使用说明.txt", "install.sh", "bupt.1", "THIRD-PARTY-NOTICES.txt", "SHA256SUMS"}
    files.update("bin/bupt-" + target for target in PLATFORMS.values())
    bundle = work / name
    with ZipFile(archive) as z:
        check(sorted(z.namelist()) == sorted(f"{name}/{f}" for f in files), "Unexpected ZIP contents")
        for member in z.infolist():
            mode = member.external_attr >> 16
            check(stat.S_ISREG(mode), "Unexpected non-regular member")
            path = work / member.filename
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(z.read(member))
            path.chmod(stat.S_IMODE(mode))
    manifest = "".join(f"{sha((bundle / f).read_bytes())}  {f}\n" for f in sorted(files - {"SHA256SUMS"}))
    check((bundle / "SHA256SUMS").read_text() == manifest, "Inner manifest mismatch")
    check((bundle / "VERSION").read_text().strip() == version, "Bundled version mismatch")
    return bundle


def run_installer(bundle, dest, env, *, ok=True):
    result = subprocess.run(
        ["sh", str(bundle / "install.sh"), "--bin-dir", str(dest)],
        cwd=bundle.parent, env=env, capture_output=True, text=True,
    )
    check((result.returncode == 0) == ok, result.stderr or "Unexpected installer result")


def run_online_installer(work, dest, env, *, ok=True):
    result = subprocess.run(
        ["sh", "-s", "--", "--bin-dir", str(dest)], input=(ROOT / "dist/install.sh").read_bytes(),
        cwd=work, env=env, capture_output=True,
    )
    check((result.returncode == 0) == ok, result.stderr.decode() or "Unexpected online installer result")


def check_shell_setup(bundle, work, env):
    for shell in ("bash", "zsh", "fish", "sh"):
        home = work / f"{shell} home"
        home.mkdir()
        config_dir = home / "shell configs"
        paths = {
            "bash": [home / ".bash_login", home / ".bashrc"],
            "zsh": [config_dir / ".zshenv"],
            "fish": [config_dir / "fish/conf.d/bupt-path.fish"],
            "sh": [home / ".profile"],
        }[shell]
        for path in paths:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("# existing config", encoding="utf-8")
        clean = dict(env, HOME=str(home), SHELL=f"/bin/{shell}", ZDOTDIR=str(config_dir),
                     XDG_CONFIG_HOME=str(config_dir), PATH=os.defpath)
        def install():
            subprocess.run(["sh", str(bundle / "install.sh")], cwd=work, env=clean,
                           check=True, capture_output=True)
        install()
        configs = [path.read_text() for path in paths]
        install()
        check(configs == [path.read_text() for path in paths], f"Duplicate PATH setup for {shell}")
        check(all(text.startswith("# existing config\n") and ".local/bin" in text for text in configs),
              f"Invalid PATH setup for {shell}")
        check(not (home / ".bash_profile").exists(), "Installer shadowed an existing bash login file")
        binary = str(home / ".local/bin/bupt")
        if shell in ("bash", "sh"):
            for path in paths:
                output = subprocess.check_output([shell, "-c", '. "$1"; . "$1"; command -v bupt; echo "$PATH"',
                                                  shell, str(path)], cwd=work, env=clean, text=True)
                resolved, search_path = output.splitlines()
                check(resolved == binary and search_path.split(":").count(str(Path(binary).parent)) == 1,
                      f"PATH lookup failed after sourcing {path}")
        elif executable := shutil.which(shell):
            output = subprocess.check_output([executable, "-c", "command -v bupt"], cwd=work, env=clean, text=True)
            check(output.strip() == binary, f"Fresh {shell} failed to find bupt")


def main():
    version = os.environ.get("VERSION") or (ROOT / "VERSION").read_text().strip()
    with tempfile.TemporaryDirectory(prefix="bupt release check ") as tmp:
        work = Path(tmp)
        bundle = verify_bundle(ROOT / "dist", version, work)
        dest = work / "local bin"
        binary = dest / "bupt"
        env = dict(os.environ, HOME=str(work), XDG_CONFIG_HOME=str(work / "config"),
                   XDG_STATE_HOME=str(work / "state"), XDG_DATA_HOME=str(work / "data"))
        check_shell_setup(bundle, work, env)
        run_installer(bundle, dest, env)
        installed = subprocess.check_output([str(binary), "version", "--json"], cwd=work, env=env, text=True)
        check(json.loads(installed)["version"] == version, "Installed version mismatch")

        shims = work / "shim"
        shims.mkdir()
        uname = shims / "uname"
        uname.write_text('#!/bin/sh\ncase "$1" in -s) echo "$TEST_SYSTEM";; -m) echo "$TEST_MACHINE";; *) exit 2;; esac\n')
        uname.chmod(0o755)
        curl = shims / "curl"
        curl.write_text("""#!/bin/sh
set -eu
while [ "$1" != "-o" ]; do shift; done
[ "$3" = "$TEST_RELEASE_URL/$TEST_PROGRAM" ]
cp "$TEST_BIN_DIR/$TEST_PROGRAM" "$2"
case ${TEST_DOWNLOAD_FAULT-} in
    interrupted) exit 18 ;;
    corrupt) printf 'damaged' >> "$2" ;;
esac
""")
        curl.chmod(0o755)
        env.update(TEST_RELEASE_URL=f"https://github.com/ppppangu/bupt-cli/releases/download/v{version}",
                   TEST_BIN_DIR=str(bundle / "bin"), TEST_DOWNLOAD_FAULT="")
        env["PATH"] = str(shims) + os.pathsep + env["PATH"]
        go = os.environ.get("GO", "go")
        for (system, machine), target in PLATFORMS.items():
            env.update(TEST_SYSTEM=system, TEST_MACHINE=machine)
            packaged = bundle / "bin" / f"bupt-{target}"
            build = json.loads(subprocess.check_output([go, "version", "-m", "-json", str(packaged)], cwd=work))
            settings = {x["Key"]: x["Value"] for x in build["Settings"]}
            check(target == settings["GOOS"] + "-" + settings["GOARCH"], f"Wrong target in {packaged.name}")
            check(settings["CGO_ENABLED"] == "0", "Expected a standalone build")
            run_installer(bundle, dest, env)
            check(binary.read_bytes() == packaged.read_bytes(), f"Wrong installer selection for {target}")
            env["TEST_PROGRAM"] = f"bupt-{target}"
            run_online_installer(work, dest, env)
            check(binary.read_bytes() == packaged.read_bytes(), f"Wrong downloaded binary for {target}")

        original = sha(binary.read_bytes())
        env.update(TEST_SYSTEM="Linux", TEST_MACHINE="riscv64")
        run_installer(bundle, dest, env, ok=False)
        run_online_installer(work, dest, env, ok=False)
        check(sha(binary.read_bytes()) == original, "Unsupported target replaced the previous binary")
        env.update(TEST_SYSTEM="Darwin", TEST_MACHINE="arm64")
        for fault in ("interrupted", "corrupt"):
            env.update(TEST_PROGRAM="bupt-darwin-arm64", TEST_DOWNLOAD_FAULT=fault)
            run_online_installer(work, dest, env, ok=False)
            check(sha(binary.read_bytes()) == original, f"{fault} download replaced the previous binary")
        with (bundle / "bin/bupt-darwin-arm64").open("ab") as f:
            f.write(b"corrupt fixture")
        run_installer(bundle, dest, env, ok=False)
        check(sha(binary.read_bytes()) == original, "Corrupt package replaced the previous binary")
        check(list(dest.iterdir()) == [binary], "Installer left a staging file")
    print("Verified archive, checksums, native execution, online/offline installs for four platforms and failed upgrades.")


if __name__ == "__main__":
    main()
