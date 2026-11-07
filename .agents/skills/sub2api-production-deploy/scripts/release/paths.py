from __future__ import annotations

import os
from pathlib import Path


RELEASE_PACKAGE_ROOT = Path(__file__).resolve().parent
SCRIPTS_ROOT = RELEASE_PACKAGE_ROOT.parent
SKILL_ROOT = SCRIPTS_ROOT.parent


def _application_root() -> Path:
    configured = os.environ.get("SUB2API_APP_ROOT")
    if configured:
        root = Path(configured).expanduser().resolve()
        if not (root / ".git").exists():
            raise RuntimeError("configured application root is not a Git checkout")
        return root
    for candidate in (Path.cwd().resolve(), *Path.cwd().resolve().parents):
        if (candidate / ".git").exists():
            return candidate
    raise RuntimeError("run release commands from the application checkout or set SUB2API_APP_ROOT")


APP_ROOT = _application_root()
WORKSPACE = APP_ROOT
REPO_ROOT = APP_ROOT
OFFICIAL_DEPLOY_ROOT = APP_ROOT / "deploy"
RUN_ROOT = Path(os.environ.get("SUB2API_RUN_ROOT", APP_ROOT / ".tmp" / "releases")).expanduser().resolve()
PLUGIN_RUN_ROOT = APP_ROOT / ".tmp" / "plugin-releases"
PLUGIN_SOURCE_ROOT = APP_ROOT / "plugins"
SSH_CONFIG = Path(os.environ.get("SUB2API_SSH_CONFIG", APP_ROOT / ".ssh.local")).expanduser().absolute()
DEPLOY_CONFIG = APP_ROOT / ".release" / "deploy.local.json"
MAINTENANCE_ROOT = SCRIPTS_ROOT / "maintenance" / "release"
LEGACY_UNIT_ROOT = OFFICIAL_DEPLOY_ROOT / "maintenance" / "181"
UNIT_ROOT = SCRIPTS_ROOT / "maintenance" / "181"
LOGGING_PACKAGE_ROOT = SCRIPTS_ROOT / "logging" / "release_logging"
WINDOWS_ROOT = SCRIPTS_ROOT / "windows"
ENTRYPOINT = SCRIPTS_ROOT / "release.py"
TRUSTED_VM_PUBLIC_KEY = RELEASE_PACKAGE_ROOT / "trust" / "vm-gate-ed25519.pub"

# Release asset layouts are intentionally versioned.  A manifest without a
# layout field predates the skill migration and therefore uses deploy-v1.
LAYOUT_DEPLOY_V1 = "deploy-v1"
LAYOUT_SKILL_V1 = "skill-v1"
SKILL_ASSET_ROOT = ".agents/skills/sub2api-production-deploy/scripts"
DEPLOY_ASSET_ROOT = "deploy"


def skill_asset_path(relative: str) -> str:
    """Return a normalized repository-relative skill asset path."""
    return f"{SKILL_ASSET_ROOT}/{relative}".replace("\\", "/")


def deploy_asset_path(relative: str) -> str:
    """Return a normalized repository-relative legacy asset path."""
    return f"{DEPLOY_ASSET_ROOT}/{relative}".replace("\\", "/")
