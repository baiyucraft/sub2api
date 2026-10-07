"""Exact-content compatibility review; additional runtime changes stay blocked."""
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate
from release.paths import WORKSPACE

BASE = '0898fe8c6289f87a1a4d90cf74a50e296689960e'
TARGET = '7c58225c34f876e1fe3967f1d8cf941d7d3d5f49'
PREFIX = '.agents/skills/sub2api-production-deploy/scripts/'


class Profile264RecoveryReviewTest(unittest.TestCase):
    def test_review_covers_only_the_actual_35_compatibility_paths(self):
        paths = recovery_gate._changed_paths(WORKSPACE, BASE, TARGET)
        sensitive = {p for p in paths if recovery_gate._is_recovery_sensitive_path(p)}
        self.assertEqual(len(sensitive), 35)
        self.assertEqual(sensitive, recovery_gate._PROFILE264_COMPATIBILITY_PATHS)
        report = recovery_gate.classify(WORKSPACE, BASE, TARGET)
        self.assertEqual(report['mode'], 'specialized')
        self.assertIn('migration_changed', report['reason_codes'])
        self.assertIn('reviewed_profile_compatibility_changed', report['reason_codes'])
        self.assertNotIn('recovery_change_requires_review', report['reason_codes'])
        recovery_gate.assert_release_allowed(report)

    def test_every_reviewed_path_rejects_blob_mode_deletion_move_and_base_drift(self):
        old = recovery_gate._tree_blobs(WORKSPACE, BASE)
        new = recovery_gate._tree_blobs(WORKSPACE, TARGET)
        paths = recovery_gate._changed_paths(WORKSPACE, BASE, TARGET)
        future, future_base = 'f' * 40, 'e' * 40
        for path in sorted(recovery_gate._PROFILE264_COMPATIBILITY_PATHS):
            mode, identity = new[path].split(':')
            for mutation in ('blob', 'mode', 'delete', 'move', 'base_blob'):
                with self.subTest(path=path, mutation=mutation):
                    changed, observed_old, affected = dict(new), dict(old), list(paths)
                    observed_base = BASE
                    if mutation == 'blob':
                        changed[path] = mode + ':' + '0' * 40
                    elif mutation == 'mode':
                        changed[path] = ('100644' if mode == '100755' else '100755') + ':' + identity
                    elif mutation == 'base_blob':
                        observed_base = future_base
                        observed_old[path] = mode + ':' + '0' * 40
                    else:
                        del changed[path]
                        if mutation == 'move':
                            other = PREFIX + 'maintenance/release/restore.sh'
                            changed[other] = new[path]
                            affected.append(other)
                    trees = {BASE: old, TARGET: new, future: changed, future_base: observed_old}
                    with (
                        mock.patch.object(recovery_gate, '_REVIEWED_COMPATIBILITY_TRANCHES', ()),
                        mock.patch.object(recovery_gate, '_REVIEWED_MIGRATION_GATE_TRANCHES', ()),
                        mock.patch.object(recovery_gate, '_REVIEWED_GATE_POLICY_TRANCHES', ()),
                        mock.patch.object(recovery_gate, '_REVIEWED_SCOPED_TRANCHES', ((BASE, TARGET, 'reviewed_profile_compatibility_changed', recovery_gate._PROFILE264_COMPATIBILITY_PATHS),)),
                        mock.patch.object(recovery_gate, '_tree_blobs', side_effect=lambda _root, commit: trees[commit]),
                        mock.patch.object(recovery_gate, '_changed_paths', return_value=affected),
                        mock.patch.object(recovery_gate, '_commit_exists', return_value=True),
                        mock.patch.object(recovery_gate, '_is_ancestor', return_value=True),
                    ):
                        report = recovery_gate.classify(WORKSPACE, observed_base, future)
                        self.assertEqual(report['mode'], 'full')
                        with self.assertRaisesRegex(RuntimeError, 'recovery changes require review'):
                            recovery_gate.assert_release_allowed(recovery_gate.require_full(report))

    def test_unlisted_recovery_algorithm_never_inherits_the_review(self):
        old = recovery_gate._tree_blobs(WORKSPACE, BASE)
        new = recovery_gate._tree_blobs(WORKSPACE, TARGET)
        restore = PREFIX + 'maintenance/release/restore.sh'
        trees = {BASE: old, TARGET: {**new, restore: '100755:' + '0' * 40}}
        paths = recovery_gate._changed_paths(WORKSPACE, BASE, TARGET) + [restore]
        with (
            mock.patch.object(recovery_gate, '_tree_blobs', side_effect=lambda _root, commit: trees[commit]),
            mock.patch.object(recovery_gate, '_changed_paths', return_value=paths),
            mock.patch.object(recovery_gate, '_commit_exists', return_value=True),
            mock.patch.object(recovery_gate, '_is_ancestor', return_value=True),
            mock.patch.object(recovery_gate, '_REVIEWED_COMPATIBILITY_TRANCHES', ()),
            mock.patch.object(recovery_gate, '_REVIEWED_MIGRATION_GATE_TRANCHES', ()),
            mock.patch.object(recovery_gate, '_REVIEWED_GATE_POLICY_TRANCHES', ()),
            mock.patch.object(recovery_gate, '_REVIEWED_SCOPED_TRANCHES', ((BASE, TARGET, 'reviewed_profile_compatibility_changed', recovery_gate._PROFILE264_COMPATIBILITY_PATHS),)),
        ):
            report = recovery_gate.classify(WORKSPACE, BASE, TARGET)
            self.assertEqual(report['mode'], 'full')
            with self.assertRaisesRegex(RuntimeError, 'recovery changes require review'):
                recovery_gate.assert_release_allowed(report)


if __name__ == '__main__':
    unittest.main()
