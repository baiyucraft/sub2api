#!/usr/bin/env python3
"""Release entrypoint; also supports the fixed interpreter with -I -S -B."""
from __future__ import annotations

import os
import sys
from pathlib import Path


def _maintenance_imports(scripts: Path) -> None:
    # Add the running interpreter's physical dependencies without executing
    # site/.pth hooks. Python <3.14 resets sys.prefix under -S.
    import sysconfig
    prefix = Path(sys.executable).absolute().parent.parent
    if not (prefix / "pyvenv.cfg").is_file():
        prefix = Path(sys.prefix)
    scheme = "nt" if os.name == "nt" else "posix_prefix"
    paths = sysconfig.get_paths(scheme=scheme, vars={"base": str(prefix), "platbase": str(prefix)})
    for key in ("purelib", "platlib"):
        path = Path(paths[key])
        if path.is_dir() and str(path) not in sys.path:
            sys.path.append(str(path))
    sys.path.insert(0, str(scripts))


def main() -> None:
    scripts = Path(__file__).resolve().parent
    _maintenance_imports(scripts)
    from release.deploy_config import prepare_arguments

    try:
        sys.argv[1:] = prepare_arguments(sys.argv[1:], scripts.parents[3])
    except (OSError, ValueError) as error:
        raise SystemExit(f"deploy config: {error}") from None
    from release.cli import main as release_main

    release_main()


if __name__ == "__main__":
    main()
