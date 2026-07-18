from __future__ import annotations

import argparse
import hashlib
import os
from pathlib import Path
import struct
import subprocess


REQUIRED_GO_VERSION = "go version go1.26.3"
ROOT = Path(__file__).resolve().parents[3]
CHECKSUM_FILE = Path(__file__).resolve().with_name("linux-amd64.sha256")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_static_linux_amd64(path: Path) -> None:
    data = path.read_bytes()
    if len(data) < 64 or data[:6] != b"\x7fELF\x02\x01":
        raise RuntimeError("verifier is not a 64-bit little-endian ELF")
    if struct.unpack_from("<H", data, 18)[0] != 62:
        raise RuntimeError("verifier is not an amd64 ELF")
    program_offset = struct.unpack_from("<Q", data, 32)[0]
    entry_size = struct.unpack_from("<H", data, 54)[0]
    entry_count = struct.unpack_from("<H", data, 56)[0]
    if entry_size < 8 or program_offset + entry_size * entry_count > len(data):
        raise RuntimeError("verifier ELF program headers are invalid")
    for index in range(entry_count):
        program_type = struct.unpack_from("<I", data, program_offset + index * entry_size)[0]
        if program_type == 3:
            raise RuntimeError("verifier ELF contains a dynamic interpreter")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=ROOT / ".tmp" / "sub2api-verify-dr-evidence")
    args = parser.parse_args()
    version = subprocess.check_output(["go", "version"], text=True).strip()
    if not version.startswith(REQUIRED_GO_VERSION + " "):
        raise RuntimeError(f"Go version must start with {REQUIRED_GO_VERSION}")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    environment = os.environ.copy()
    environment.update({"GO111MODULE": "off", "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"})
    subprocess.run(
        ["go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", str(args.output), "./deploy/release/drverify"],
        cwd=ROOT,
        env=environment,
        check=True,
    )
    verify_static_linux_amd64(args.output)
    expected = CHECKSUM_FILE.read_text(encoding="ascii").split()[0]
    actual = sha256_file(args.output)
    if actual != expected:
        raise RuntimeError("verifier binary checksum differs from the repository contract")
    print(f"verifier_build=verified sha256={actual}")


if __name__ == "__main__":
    main()
