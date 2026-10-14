"""Fail-closed Guest preparation inputs and actual image import contracts."""
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0,str(Path(__file__).resolve().parents[2]))
from release import full_recovery_guest as guest


class FullRecoveryGuestTests(unittest.TestCase):
    def setUp(self):
        self.manifest={'schema':1,'drill_id':'full-dr-12345678-1234-4234-8234-123456789abc','release_id':'270-aaaaaaaaaaaa-1-deadbeef','pre_image_id':'sha256:'+'a'*64,'images':[{'role':role,'image_id':'sha256:'+char*64,'path':'/opt/sub2api-full-recovery-input/images/images.tar','sha256':'d'*64,'bytes':123} for role,char in [('app','a'),('postgres','b'),('redis','c')]],'backup_units':[{'name':'sub2api-backup.'+suffix,'path':'/opt/sub2api-full-recovery-input/units/sub2api-backup.'+suffix,'sha256':'e'*64,'bytes':100} for suffix in ('service','timer')],'active_app':{'container':'sub2api','port':18080,'instance_id':'old-active','release_id':'old'},'source_snapshot_path':'/opt/sub2api-full-recovery-input/source-snapshot.json','compose_plugin_sha256':'f'*64}
        self.manifest['docker_daemon_config_sha256']=hashlib.sha256(guest.DAEMON_BYTES).hexdigest()
        self.manifest['docker_version']='29.1.3'

    def test_manifest_requires_exact_images_units_and_guest_paths(self):
        self.assertEqual(guest.validate_manifest(self.manifest),self.manifest)
        for change in ('duplicate_role','missing_unit','host_path','synthetic_image','bad_port','unknown_field'):
            value=deepcopy(self.manifest)
            if change=='duplicate_role': value['images'][1]['role']='app'
            if change=='missing_unit': value['backup_units'].pop()
            if change=='host_path': value['images'][0]['path']='/var/lib/docker/images.tar'
            if change=='synthetic_image': value['images'][0]['image_id']='latest'
            if change=='bad_port': value['active_app']['port']=8080
            if change=='unknown_field': value['verified']=True
            with self.subTest(change=change),self.assertRaises(RuntimeError): guest.validate_manifest(value)

    def test_manifest_hash_is_checked_before_parsing_or_commands(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/'manifest'; path.write_text('{}')
            with mock.patch.object(guest,'regular'),mock.patch.object(guest,'strict_json') as parse,mock.patch.object(guest,'command') as command,self.assertRaises(RuntimeError):
                guest.approved_manifest(path,{'guest_manifest_sha256':'0'*64})
            parse.assert_not_called(); command.assert_not_called()

    def test_source_snapshot_actual_commit_and_image_are_bound(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/'manifest'; path.write_text('{}')
            snapshot=Path(temp)/'snapshot'; snapshot.write_text('{}')
            manifest=deepcopy(self.manifest); manifest['source_snapshot_path']=str(snapshot)
            context={'source_snapshot_sha256':guest.digest(snapshot),'commit_sha':'1'*40,'candidate_image_id':manifest['pre_image_id']}
            approval={k:manifest[k] for k in ('drill_id','release_id','pre_image_id')}; approval.update(guest_manifest_sha256=guest.digest(path),drill_context=context)
            for actual in ({'production_current_commit_sha':'2'*40,'current_image_id':manifest['pre_image_id']},{'production_current_commit_sha':'1'*40,'current_image_id':'sha256:'+'9'*64}):
                with mock.patch.object(guest,'regular'),mock.patch.object(guest,'validate_manifest',return_value=manifest),mock.patch.object(guest,'strict_json',side_effect=[manifest,actual]),self.assertRaises(RuntimeError): guest.approved_manifest(path,approval)

    def test_combined_archive_loaded_once_and_each_exact_image_observed(self):
        def fake_digest(p):
            return {'docker-compose':'f'*64,'daemon.json':self.manifest['docker_daemon_config_sha256']}.get(p.name,'d'*64)
        def output(argv,*args):
            if argv[:2]==['docker','version']: return b'29.1.3'
            if argv[:2]==['docker','info']: return b'[["driver-type","io.containerd.snapshotter.v1"]]'
            if argv[:3]==['docker','compose','version']: return b'v2'
            if argv[:2]==['docker','load']: return b'loaded'
            if argv[:3]==['docker','image','inspect']: return argv[-1].encode()
            raise AssertionError('unexpected mutation')
        with mock.patch.object(guest,'verify_inputs'),mock.patch.object(guest,'regular'),mock.patch.object(guest,'digest',side_effect=fake_digest),mock.patch.object(Path,'stat') as info,mock.patch.object(guest,'command',side_effect=output) as command:
            info.return_value.st_size=123
            # Missing unit input then stops before live systemd mutation.
            with self.assertRaises(RuntimeError): guest.prepare_system(self.manifest,{},Path('/helpers'),{},Path('/raw'))
            loads=[c for c in command.call_args_list if c.args[0][:2]==['docker','load']]
            checks=[c for c in command.call_args_list if c.args[0][:3]==['docker','image','inspect']]
            self.assertEqual(len(loads),1); self.assertEqual(len(checks),3)

    def test_private_outputs_never_overwrite_failure_evidence(self):
        with tempfile.TemporaryDirectory() as temp:
            p=Path(temp)/'receipt'; guest.write_private(p,b'first')
            with self.assertRaises(RuntimeError): guest.write_private(p,b'replaced')
            self.assertEqual(p.read_bytes(),b'first')


if __name__=='__main__': unittest.main()
