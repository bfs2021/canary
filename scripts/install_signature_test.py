#!/usr/bin/env python3
"""Exercise the installer's actual verifier with ephemeral, synthetic GPG keys."""
import os
import pathlib
import shutil
import stat
import subprocess
import tempfile


def main():
    if not shutil.which("gpg") or not shutil.which("gpgconf"):
        print("installer signature tests: SKIP (GPG unavailable)")
        return
    repo = pathlib.Path(__file__).resolve().parent.parent
    installer = (repo / "install.sh").read_text()
    block = installer.split("# --- verify PGP signature", 1)[1]
    block = block[block.index("\n") + 1:].split("# --- verify checksum", 1)[0]
    # Key generation needs a short socket directory; verification deliberately
    # uses a long directory and must never start an agent there.
    with tempfile.TemporaryDirectory(prefix="canary-key-test-", dir="/tmp") as root_name:
        root = pathlib.Path(root_name)
        keyhome = root / "keys"
        keyhome.mkdir(mode=0o700)

        def gpg(*args):
            return subprocess.run(
                ["gpg", "--homedir", str(keyhome), "--batch", "--no-tty", *args],
                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            ).stdout

        try:
            keys = []
            for name in ("Pinned", "Unpinned"):
                uid = f"Canary Synthetic {name} <{name.lower()}@example.invalid>"
                gpg("--pinentry-mode", "loopback", "--passphrase", "",
                    "--quick-generate-key", uid, "ed25519", "cert", "0")
                listing = gpg("--with-colons", "--fingerprint", uid).decode()
                fp = next(line.split(":")[9] for line in listing.splitlines() if line.startswith("fpr:"))
                gpg("--pinentry-mode", "loopback", "--passphrase", "",
                    "--quick-add-key", fp, "ed25519", "sign", "0")
                keys.append(fp)
            pinned = gpg("--armor", "--export", keys[0])
            extra = gpg("--armor", "--export", keys[1])
            manifest = root / "manifest"
            manifest.write_text("a" * 64 + "  synthetic-release.tar.gz\n")
            signatures = []
            for i, fp in enumerate(keys):
                sig = root / f"signature-{i}.asc"
                gpg("--pinentry-mode", "loopback", "--passphrase", "", "--local-user", fp,
                    "--armor", "--detach-sign", "--output", str(sig), str(manifest))
                signatures.append(sig.read_bytes())
            shim = root / "bin"
            shim.mkdir()
            (shim / "curl").write_text('#!/bin/sh\nset -eu\n[ "$2" = -o ]\ncp "$TEST_KEY" "$3"\n')
            (shim / "curl").chmod(0o755)
            verifier = root / "verify.sh"
            verifier.write_text('set -eu\nstep() { :; }\ninfo() { :; }\nwarn() { echo "$*" >&2; }\nfail() { echo "$*" >&2; exit 1; }\n' + block)
            for name, key, signature, tamper, expected in (
                ("pinned-subkey-long-path", pinned, signatures[0], False, 0),
                ("extra-unpinned-primary", pinned + extra, signatures[1], False, 1),
                ("unpinned-signature", pinned, signatures[1], False, 1),
                ("tampered-manifest", pinned, signatures[0], True, 1),
            ):
                case = root / ("long-verification-directory-" + "x" * 110) / name
                case.mkdir(parents=True, mode=0o700)
                keyfile = case / "downloaded-key.asc"
                keyfile.write_bytes(key)
                (case / "SHA256SUMS").write_bytes(manifest.read_bytes() + (b"tampered\n" if tamper else b""))
                (case / "SHA256SUMS.asc").write_bytes(signature)
                env = {**os.environ, "PATH": str(shim) + os.pathsep + os.environ["PATH"],
                       "tmp": str(case), "TEST_KEY": str(keyfile), "KEY_URL": "synthetic-key",
                       "VERSION": "v0.0.0", "require_sig": "1", "EXPECTED_FP": keys[0]}
                result = subprocess.run(["sh", str(verifier)], env=env, capture_output=True, text=True, timeout=20)
                if (result.returncode == 0) != (expected == 0):
                    raise AssertionError(f"{name}: exit {result.returncode}: {result.stderr}")
                if any(stat.S_ISSOCK(p.stat().st_mode) for p in case.rglob("*") if not p.is_symlink()):
                    raise AssertionError(f"{name}: verification started an agent")
                print(f"installer signature test: {name} OK", flush=True)
        finally:
            subprocess.run(["gpgconf", "--homedir", str(keyhome), "--kill", "all"],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    main()
