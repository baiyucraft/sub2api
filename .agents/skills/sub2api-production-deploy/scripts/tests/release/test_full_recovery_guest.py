"""Fail-closed Guest preparation inputs and actual image import contracts."""
from copy import deepcopy
import hashlib
import json
import stat
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
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
        with mock.patch.object(guest,'verify_inputs'),mock.patch.object(guest,'verify_nginx_runtime'),mock.patch.object(guest,'regular'),mock.patch.object(guest,'digest',side_effect=fake_digest),mock.patch.object(Path,'stat') as info,mock.patch.object(guest,'command',side_effect=output) as command:
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

    def test_prepare_source_rejects_missing_recovery_state_directory(self):
        with tempfile.TemporaryDirectory() as temp:
            missing = Path(temp) / "release-state" / self.manifest["release_id"]
            with self.assertRaises(RuntimeError):
                guest.private_directory(missing)

    def test_digest_references_use_system_containerd_and_verify_docker_identity(self):
        image = 'sha256:' + 'b'*64
        source = 'postgres@' + image
        reference = 'docker.io/library/' + source
        ctr = ['ctr','--address','/run/containerd/containerd.sock','--namespace','moby','images']
        def output(argv,*args):
            if argv == [*ctr,'list','--quiet']: return ('moby-dangling@'+image+'\n').encode()
            if argv == [*ctr,'tag','moby-dangling@'+image,reference]: return b''
            if argv == ['docker','image','inspect','--format','{{.Id}}',source]: return image.encode()
            raise AssertionError('unexpected command or containerd socket')
        with mock.patch.object(guest,'verify_oci_content') as content,mock.patch.object(guest,'command',side_effect=output) as command:
            guest.restore_image_references(['postgres '+source+' '+image],{image},Path('/raw'),{})
        content.assert_called_once_with(image,Path('/raw'),{})
        self.assertEqual(command.call_count,3)

    def test_existing_digest_reference_is_observed_without_retagging(self):
        image = 'sha256:' + 'c'*64
        source = 'docker.io/library/redis@' + image
        def output(argv,*args):
            if argv[-2:] == ['list','--quiet']: return (source+'\n').encode()
            if argv[:3] == ['docker','image','inspect']: return image.encode()
            raise AssertionError('existing reference must not be mutated')
        with mock.patch.object(guest,'verify_oci_content'),mock.patch.object(guest,'command',side_effect=output) as command:
            guest.restore_image_references(['redis '+source+' '+image],{image},Path('/raw'),{})
        self.assertEqual(command.call_count,2)

    def test_descriptor_mismatch_blocks_before_tag_or_docker_commands(self):
        image = 'sha256:' + 'b'*64
        def output(argv,*args):
            if argv[-2:] == ['list','--quiet']: return b'wrong-image\n'
            raise AssertionError('mismatched descriptor must not be used')
        with mock.patch.object(guest,'verify_oci_content'),mock.patch.object(guest,'command',side_effect=output) as command,self.assertRaisesRegex(RuntimeError,'reference missing'):
            guest.restore_image_references(['postgres postgres@'+image+' '+image],{image},Path('/raw'),{})
        self.assertEqual(command.call_count,1)

    def test_retagged_reference_still_requires_exact_docker_identity(self):
        image = 'sha256:' + 'b'*64
        def output(argv,*args):
            if argv[-2:] == ['list','--quiet']: return ('moby-dangling@'+image+'\n').encode()
            if 'tag' in argv: return b''
            return ('sha256:'+'c'*64).encode()
        with mock.patch.object(guest,'verify_oci_content'),mock.patch.object(guest,'command',side_effect=output),self.assertRaisesRegex(RuntimeError,'unavailable offline'):
            guest.restore_image_references(['postgres postgres@'+image+' '+image],{image},Path('/raw'),{})

    def test_missing_nginx_mime_data_blocks_before_system_mutation(self):
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaises(OSError): guest.verify_nginx_runtime(Path(temp))
        with mock.patch.object(guest,'verify_inputs'),mock.patch.object(guest,'verify_nginx_runtime',side_effect=RuntimeError('missing MIME data')),mock.patch.object(guest,'command') as command,self.assertRaises(RuntimeError):
            guest.prepare_system(self.manifest,{},Path('/helpers'),{},Path('/raw'))
        command.assert_not_called()

    def test_nginx_mime_data_must_be_nonempty_bounded_and_private_package_file(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); mime = root/'etc/nginx/mime.types'
            mime.parent.mkdir(parents=True); mime.write_bytes(b'x'*guest.NGINX_MIME_BYTES)
            with mock.patch.object(guest,'regular') as regular,mock.patch.object(guest,'digest',return_value=guest.NGINX_MIME_SHA256):
                guest.verify_nginx_runtime(root)
                regular.assert_called_once_with(mime,mode=0o644)
                for data in (b'',b'x'*(128*1024+1)):
                    mime.write_bytes(data)
                    with self.assertRaises(RuntimeError): guest.verify_nginx_runtime(root)

    def test_oci_content_chain_is_parsed_as_digest_verified_json(self):
        config = b'{"architecture":"amd64","os":"linux"}'
        config_id = 'sha256:'+hashlib.sha256(config).hexdigest()
        manifest = json.dumps({'schemaVersion':2,'mediaType':'application/vnd.oci.image.manifest.v1+json','config':{'digest':config_id}}).encode()
        manifest_id = 'sha256:'+hashlib.sha256(manifest).hexdigest()
        index = json.dumps({'schemaVersion':2,'mediaType':'application/vnd.oci.image.index.v1+json','manifests':[{'digest':manifest_id,'platform':{'os':'linux','architecture':'amd64'}}]}).encode()
        image = 'sha256:'+hashlib.sha256(index).hexdigest()
        content = {image:index,manifest_id:manifest,config_id:config}
        def output(argv,*args):
            self.assertEqual(argv[:7],['ctr','--address','/run/containerd/containerd.sock','--namespace','moby','content','get'])
            return content[argv[-1]]
        with mock.patch.object(guest,'command',side_effect=output) as command:
            guest.verify_oci_content(image,Path('/raw'),{})
        self.assertEqual(command.call_count,3)
        for corrupt in (image,manifest_id,config_id):
            with self.subTest(corrupt=corrupt),mock.patch.object(guest,'command',side_effect=lambda argv,*args: b'wrong content' if argv[-1]==corrupt else content[argv[-1]]),self.assertRaisesRegex(RuntimeError,'digest differs'):
                guest.verify_oci_content(image,Path('/raw'),{})

    def test_containerd_socket_and_docker_unit_are_bound(self):
        info = SimpleNamespace(st_mode=stat.S_IFSOCK|0o660,st_uid=0)
        with mock.patch.object(Path,'lstat',return_value=info),mock.patch.object(Path,'is_symlink',return_value=False),mock.patch.object(guest,'command',return_value=b'ExecStart=/usr/bin/dockerd --containerd=/run/containerd/containerd.sock'):
            guest.verify_containerd_runtime(Path('/raw'),{})
        for mode,uid,link,unit in ((stat.S_IFREG|0o660,0,False,b'--containerd=/run/containerd/containerd.sock'),(stat.S_IFSOCK|0o660,1,False,b''),(stat.S_IFSOCK|0o660,0,True,b''),(stat.S_IFSOCK|0o666,0,False,b''),(stat.S_IFSOCK|0o660,0,False,b'--containerd=/run/docker/containerd/containerd.sock')):
            with self.subTest(mode=mode,uid=uid,link=link),mock.patch.object(Path,'lstat',return_value=SimpleNamespace(st_mode=mode,st_uid=uid)),mock.patch.object(Path,'is_symlink',return_value=link),mock.patch.object(guest,'command',return_value=unit),self.assertRaises(RuntimeError):
                guest.verify_containerd_runtime(Path('/raw'),{})


    def test_guest_docker_runtime_disables_nested_runc_keyring_creation(self):
        self.assertEqual(
            guest.DAEMON_CONFIG['runtimes'][guest.OFFLINE_RUNTIME_NAME],
            {'runtimeType': 'io.containerd.runc.v2', 'options': {'NoNewKeyring': True}},
        )
        self.assertEqual(guest.DAEMON_CONFIG['default-runtime'], guest.OFFLINE_RUNTIME_NAME)
        guest.validate_daemon_runtime_config()

    def test_reserved_builtin_runc_runtime_is_rejected(self):
        original = guest.OFFLINE_RUNTIME_NAME
        try:
            guest.OFFLINE_RUNTIME_NAME = 'runc'
            with self.assertRaisesRegex(RuntimeError, 'reserved'):
                guest.validate_daemon_runtime_config()
        finally:
            guest.OFFLINE_RUNTIME_NAME = original


if __name__=='__main__': unittest.main()
