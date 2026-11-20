"""Validate the additive 0.2.23 release and the inherited downtime policy."""
from __future__ import annotations

import hashlib
import unittest
from unittest import mock

from test_profile281_release_contract import git_blob
from release.manifest import create_manifest, validate_manifest_profile_contract
from release.migration_planner import catalog_sha256, checksum_policy_sha256, discover_migration_catalog, plan_migrations
from release.paths import WORKSPACE
from release.profiles import get_profile

UPSTREAM = "2af3f1e15c98c893a89255a037f551046883b8c0"
MIGRATION = "313_channel_pricing_ultrafast_multiplier.sql"


class Profile286ReleaseContractTest(unittest.TestCase):
    def test_only_313_is_pending_and_other_payload_checksums_cannot_be_reused(self):
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(raw, git_blob(UPSTREAM, "backend/migrations/252_channel_pricing_ultrafast_multiplier.sql"))
        self.assertEqual(hashlib.sha256(raw).hexdigest(), "01eb249a933e5c3bef3a8e2106098abf56d20a5f2890fcc6490b1f83ec05687f")
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] != MIGRATION}
        [pending] = plan_migrations(catalog, snapshot)["pending"]
        self.assertEqual(pending["filename"], MIGRATION)
        self.assertFalse(pending["non_transactional"])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})["existing_checksums_verified"])

    def test_manifest_requires_downtime_new_migration_and_ancestor_assets(self):
        profile = get_profile("286")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]),
                         ("0.2.23-baiyu", "285", 2, [MIGRATION]))
        with self.assertRaisesRegex(ValueError, "requires downtime"):
            create_manifest("a" * 40, profile, "286-aaaaaaaaaaaa-1-aaaaaaaa", "blue-green")
        catalog = discover_migration_catalog(WORKSPACE)
        assets = {f".agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-{n}-assert.sh":
                  str(n % 10) * 64 for n in (297, 298, 299, 301)}
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "downtime",
            "release_id": "286-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "286", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "parent_profile": "285", "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "release_asset_sha256": assets,
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"parent_profile": "284"}, {"new_migrations": []}, {"deployment_mode": "blue-green"}):
                with self.subTest(mutation=mutation), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
            for missing in assets:
                with self.assertRaisesRegex(RuntimeError, "assertion asset is missing"):
                    validate_manifest_profile_contract({**manifest, "release_asset_sha256": {k: v for k, v in assets.items() if k != missing}}, profile)
