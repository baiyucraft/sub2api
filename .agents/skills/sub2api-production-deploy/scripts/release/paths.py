from __future__ import annotations

from pathlib import Path


RELEASE_PACKAGE_ROOT = Path(__file__).resolve().parent
SCRIPTS_ROOT = RELEASE_PACKAGE_ROOT.parent
SKILL_ROOT = SCRIPTS_ROOT.parent
WORKSPACE = SKILL_ROOT.parents[2]
REPO_ROOT = WORKSPACE
OFFICIAL_DEPLOY_ROOT = WORKSPACE / "deploy"
RUN_ROOT = WORKSPACE / ".tmp" / "releases"
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
