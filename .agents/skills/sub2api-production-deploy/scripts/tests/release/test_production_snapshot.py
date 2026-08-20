from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.atomic import canonical_json
from release.production_snapshot import snapshot_sha256


class ProductionSnapshotTest(unittest.TestCase):
    def test_snapshot_digest_matches_canonical_persisted_document(self) -> None:
        snapshot = {
            "current_image_id": "sha256:" + "a" * 64,
            "schema_migrations": [
                {"filename": "241_example.sql", "checksum": "b" * 64},
                {"filename": "242_example.sql", "checksum": "c" * 64},
            ],
            "plan": {"pending": []},
        }

        persisted = json.loads(canonical_json(snapshot))

        self.assertEqual(snapshot_sha256(snapshot), snapshot_sha256(persisted))


if __name__ == "__main__":
    unittest.main()
