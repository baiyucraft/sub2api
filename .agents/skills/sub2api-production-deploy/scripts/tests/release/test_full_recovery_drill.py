"""Fail-closed boundaries and actual checkpoint receipt measurement tests."""
from __future__ import annotations
from copy import deepcopy
import configparser
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

SCRIPTS=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(SCRIPTS))
from release import full_recovery_drill as drill
from release import recovery_evidence as evidence


class FullRecoveryDrillTests(unittest.TestCase):
    def setUp(self):
        self.config={"schema":1,"drill_id":"full-dr-12345678-1234-4234-8234-123456789abc","backend":drill.BACKEND,"fixture_kind":"encrypted-production-baseline","release_id":"270-aaaaaaaaaaaa-1-deadbeef","artifact_sha256":"a"*64,"pre_image_id":"sha256:"+"b"*64,"recovery_helper_bundle_sha256":"c"*64,"approved_identity_sha256":"d"*64,"approved_recipient_sha256":"e"*64,"artifact_path":"/opt/sub2api-full-recovery-input/artifact.tar.age","identity_path":"/opt/sub2api-full-recovery-input/identity.age","authorization_path":"/opt/sub2api-full-recovery-input/authorization.json","redis_health_budget_seconds":180}
        self.config.update(guest_manifest_path='/opt/sub2api-full-recovery-input/guest-manifest.json',guest_manifest_sha256='f'*64)

    def test_real_config_is_not_synthetic_entry(self):
        self.assertEqual(drill.cfg(self.config),self.config)
        for changes in ({"fixture_kind":"synthetic-offline-complete-recovery"},{"schema":True},{"identity_path":"/root/key"},{"artifact_path":"/opt/../production/artifact"},{"authorization_path":"/opt/private\nkey"},{"redis_health_budget_seconds":181},{"pre_image_id":"latest"},{"artifact_sha256":"A"*64}):
            with self.subTest(changes=changes),self.assertRaises((RuntimeError,TypeError)):
                drill.cfg({**self.config,**changes})

    def candidate_config(self):
        return {**self.config,"postrelease_candidate":{"archive":"/opt/sub2api-full-recovery-input/baseline/candidate.tar.gz","gate":"/opt/sub2api-full-recovery-input/baseline/gate.json","signature":"/opt/sub2api-full-recovery-input/baseline/gate.sig"}}

    def test_optional_candidate_is_postrelease_only_and_path_bounded(self):
        config=self.candidate_config()
        self.assertEqual(drill.cfg(config),config)
        for item in ({},None,{**config['postrelease_candidate'],'archive':'/host/candidate.tar.gz'}):
            with self.subTest(item=item),self.assertRaises(RuntimeError): drill.cfg({**config,'postrelease_candidate':item})
        with mock.patch.object(drill,'mod') as module:
            self.assertEqual(drill.collect_candidate(self.config,{}, {},Path('/out')),'not_required')
        module.assert_not_called()

    def test_guest_candidate_collector_is_called_with_verified_trust_and_same_proof(self):
        config=self.candidate_config(); proof={'rootfs_identity_sha256':'1'*64}; approval={'trusted_public_key_sha256':'2'*64}
        collector=mock.Mock()
        with mock.patch.object(drill,'sf'),mock.patch.object(drill,'sha',return_value='2'*64),mock.patch.object(drill,'private_write') as write,mock.patch.object(Path,'read_bytes',return_value=b'public key'),mock.patch.object(Path,'unlink'),mock.patch.object(drill,'mod',return_value=collector):
            self.assertEqual(drill.collect_candidate(config,proof,approval,Path('/out')),'measured')
        write.assert_called_once_with(Path('/out/candidate-trust.pub'),b'public key')
        kwargs=collector.collect.call_args.kwargs
        self.assertIs(kwargs['proof'],proof)
        self.assertEqual(kwargs['release_id'],config['release_id'])
        self.assertEqual(kwargs['expected_trust_sha256'],approval['trusted_public_key_sha256'])
        self.assertEqual(kwargs['output'],Path('/out/candidate-load.json'))

    def test_candidate_sidecar_is_preserved_only_for_same_measured_guest(self):
        config=self.candidate_config(); image='sha256:'+'a'*64
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,'sf'):
            root=Path(temp).resolve()/'rootfs'; out=root/'opt/sub2api-full-recovery'; out.mkdir(parents=True)
            baseline=root/'opt/sub2api-full-recovery-input/baseline'; baseline.mkdir(parents=True)
            (baseline/'candidate.tar.gz').write_bytes(b'actual candidate archive')
            archive=drill.sha(baseline/'candidate.tar.gz')
            (baseline/'gate.json').write_text(json.dumps({'evidence':{'candidate_image_id':image,'candidate_archive_sha256':archive}}))
            receipt={'created_at':100,'completed_at':200,'measurements':{'isolation':{'guest_docker_engine_id_sha256':'1'*64,'rootfs_identity_sha256':'2'*64}}}
            load={'schema':1,'type':'sub2api-baseline-candidate-image-load','drill_id':config['drill_id'],'release_id':config['release_id'],'candidate_archive_sha256':archive,'candidate_image_id':image,'observed_image_id':image,'guest_docker_engine_id_sha256':'1'*64,'rootfs_identity_sha256':'2'*64,'load_exit_code':0,'created_at':150}
            (out/'candidate-load.json').write_text(json.dumps(load))
            with mock.patch.object(drill,'private_write') as write:
                self.assertEqual(drill.preserve_candidate(root,out,receipt,config),'measured')
                write.assert_called_once_with(root.parent/'candidate-load.json',drill.canon(load))
            for field,value in (('guest_docker_engine_id_sha256','3'*64),('load_exit_code',1),('created_at',99),('observed_image_id','sha256:'+'4'*64)):
                (out/'candidate-load.json').write_text(json.dumps({**load,field:value}))
                with self.subTest(field=field),mock.patch.object(drill,'private_write') as write,self.assertRaises(RuntimeError):
                    drill.preserve_candidate(root,out,receipt,config)
                write.assert_not_called()

    def test_missing_resource_does_not_launch_guest(self):
        with mock.patch.object(drill,"probe",return_value={"blockers":["missing_nspawn"],"status":"blocked"}),mock.patch.object(drill.subprocess,"Popen") as launch:
            result=drill.plan(Path("/not-provisioned"),self.config)
            self.assertEqual(result["status"],"blocked")
            with self.assertRaises(RuntimeError): drill.execute(Path("/not-provisioned"),self.config)
            launch.assert_not_called()

    def test_launch_has_no_host_bind_or_network_interface(self):
        command=drill.nspawn(Path("/owned/rootfs"),self.config)
        self.assertIn("--private-network",command)
        self.assertIn("--private-users=pick",command)
        self.assertIn("--settings=no",command)
        self.assertFalse(any("--bind" in item or "--network-veth" in item or "--network-interface" in item for item in command))

    @unittest.skipUnless(os.name == "posix", "private Guest file modes require POSIX")
    def test_guest_entry_creates_private_files_before_reading_inputs(self):
        class InputBoundary(Exception):
            pass
        with tempfile.TemporaryDirectory() as temp:
            probe = Path(temp) / "first-guest-file"
            def first_input(*args, **kwargs):
                with probe.open("xb") as stream:
                    stream.write(b"synthetic Guest entry probe")
                raise InputBoundary()
            original_mask = os.umask(0o022)
            try:
                with mock.patch.object(drill, "sf", side_effect=first_input), self.assertRaises(InputBoundary):
                    drill.guest_run()
                self.assertEqual(probe.stat().st_mode & 0o777, 0o600)
                self.assertEqual(os.umask(0o077), 0o077)
            finally:
                os.umask(original_mask)

    def test_execute_stages_private_service_before_guest_launch(self):
        class ServiceStaged(Exception):
            pass
        service = configparser.RawConfigParser()
        def capture_service(path, data, mode=0o600):
            if path.name == "sub2api-full-recovery.service":
                service.read_string(data.decode())
                self.assertEqual(mode, 0o600)
                raise ServiceStaged()
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve() / "rootfs"
            (root / "etc").mkdir(parents=True)
            (root / "etc/machine-id").write_text("synthetic-guest-id")
            with mock.patch.object(drill, "plan", return_value={"status":"planned"}), mock.patch.object(drill, "foreign_resources", return_value="synthetic"), mock.patch.object(drill.os, "readlink", return_value="mnt:[123]"), mock.patch.object(drill, "observed", return_value=b"synthetic-engine"), mock.patch.object(Path, "read_bytes", return_value=b"synthetic-host-id"), mock.patch.object(drill, "private_write", side_effect=capture_service), mock.patch.object(drill.subprocess, "Popen") as launch:
                with self.assertRaises(ServiceStaged):
                    drill.execute(root, self.config)
                launch.assert_not_called()
        self.assertEqual(service["Service"]["UMask"], "0077")
        self.assertEqual(service["Service"]["Type"], "oneshot")

    def test_guest_helper_environment_excludes_parent_docker_and_secrets(self):
        with mock.patch.dict(drill.os.environ,{"DOCKER_HOST":"unix:///host.sock","RECOVERY_INTERRUPT_AFTER_PHASE":"bad","EXAMPLE_SECRET":"private"}):
            env=drill.guest_environment(self.config)
        self.assertNotIn("DOCKER_HOST",env)
        self.assertNotIn("EXAMPLE_SECRET",env)
        self.assertEqual(env["RECOVERY_FULL_DRILL_ID"],self.config["drill_id"])
        self.assertEqual(env["RECOVERY_DRILL_AUTHORIZATION"],self.config["authorization_path"])

    def checkpoint_files(self,root,source):
        payload={"source":source,"recovery_helper_bundle_sha256":self.config["recovery_helper_bundle_sha256"]}
        (root/"identity").write_bytes(evidence.checkpoint_identity_bytes(payload))
        for phase in drill.PHASES:
            for state in ("started","done"):
                (root/(phase+"."+state)).write_bytes(evidence.checkpoint_marker_bytes(payload,phase))

    def test_checkpoint_receipt_uses_real_bytes_and_observed_replays(self):
        source={"release_id":self.config["release_id"],"pre_image_id":self.config["pre_image_id"],"recovery_point_sha256":"f"*64}
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,"sf"):
            root=Path(temp); self.checkpoint_files(root,source)
            events=[(p,s) for p in drill.PHASES for s in ("completed","reverified")]
            result=drill.checkpoint(root,source,self.config["recovery_helper_bundle_sha256"],events)
            for phase,item in result.items():
                self.assertEqual(item["apply_count"],1)
                self.assertEqual(item["resume_apply_count"],0)
                self.assertEqual(item["done_sha256"],hashlib.sha256((root/(phase+".done")).read_bytes()).hexdigest())
                evidence.verify_checkpoint_bytes(item["done_base64"],evidence.checkpoint_marker_bytes({"source":source,"recovery_helper_bundle_sha256":self.config["recovery_helper_bundle_sha256"]},phase),item["done_sha256"])
            with self.assertRaises(RuntimeError): drill.checkpoint(root,source,self.config["recovery_helper_bundle_sha256"],events+[("postgres_restored","completed")])
            with self.assertRaises(RuntimeError): drill.checkpoint(root,source,self.config["recovery_helper_bundle_sha256"],[(p,"completed") for p in drill.PHASES])
            (root/"postgres_restored.done").write_text("wrong point")
            with self.assertRaises(RuntimeError): drill.checkpoint(root,source,self.config["recovery_helper_bundle_sha256"],events)

    def test_missing_checkpoint_file_is_not_manufactured(self):
        source={"release_id":self.config["release_id"],"pre_image_id":self.config["pre_image_id"],"recovery_point_sha256":"f"*64}
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,"sf"):
            root=Path(temp); self.checkpoint_files(root,source); (root/"redis_restored.done").unlink()
            with self.assertRaises(FileNotFoundError): drill.checkpoint(root,source,self.config["recovery_helper_bundle_sha256"],[(p,s) for p in drill.PHASES for s in ("completed","reverified")])
            self.assertFalse((root/"redis_restored.done").exists())

    def test_fault_identity_is_measured_and_drift_fails_resume(self):
        before=("1234","a"*40)
        self.assertTrue(drill.fault(79,before,before)["resume_verified"])
        self.assertFalse(drill.fault(0,before,before)["resume_verified"])
        self.assertFalse(drill.fault(79,before,("1235","a"*40))["resume_verified"])
        self.assertFalse(drill.fault(79,before,("1234","b"*40))["resume_verified"])

    def test_command_collects_checkpoint_events_and_keeps_secret_output_private(self):
        result=subprocess.CompletedProcess([],79,b"recovery_checkpoint=postgres_restored status=completed\nredis_backup_dbsize=3\nsecret_value=has spaces and secrets\n",b"private stderr")
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,"run",return_value=result):
            raw=Path(temp)/"raw.log"; code,values,events=drill.vals(["bash"],{},raw)
            self.assertEqual(code,79)
            self.assertEqual(events,[("postgres_restored","completed")])
            self.assertEqual(values["redis_backup_dbsize"],"3")
            self.assertNotIn("secret_value",values)
            self.assertIn(b"private stderr",raw.read_bytes())

    def test_receipt_identity_failure_is_before_any_destroy(self):
        with mock.patch.object(drill.shutil,"rmtree") as destroy,self.assertRaises(RuntimeError):
            drill.validate_guest_receipt({"schema":1,"status":"pass"},self.config)
        destroy.assert_not_called()

    def test_json_rejects_duplicate_fields_and_noninteger_numbers(self):
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,"sf"):
            path=Path(temp)/"config.json"
            for raw in (b'{"schema":1,"schema":1}',b'{"delay":1.0}',b'{"delay":NaN}',b'[]'):
                path.write_bytes(raw)
                with self.subTest(raw=raw),self.assertRaises(RuntimeError): drill.read_json(path)
            path.write_bytes(b'{"schema":1,"name":"safe"}')
            self.assertEqual(drill.read_json(path),{"schema":1,"name":"safe"})

    def test_pg_counts_are_canonical_without_losing_missing_or_duplicate_rows(self):
        raw=b'users=2\naccounts=1\nupstream_keys=5\napi_keys=3\nupstream_configs=4\n'
        self.assertEqual(drill.canonical_rowcounts(raw),b'accounts=1\nusers=2\napi_keys=3\nupstream_configs=4\nupstream_keys=5\n')
        for bad in (raw+b'users=2\n',raw.replace(b'users=2\n',b''),raw.replace(b'users=2',b'users=-2')):
            with self.assertRaises(RuntimeError): drill.canonical_rowcounts(bad)

    def test_lazy_expiry_uses_actual_atomic_counts_and_refuses_no_expiry(self):
        config=json.dumps([{'Config':{'Entrypoint':['redis-server'],'Cmd':['--requirepass','private']}}]).encode()
        for output,valid in ((b'20\n18\n5\n',True),(b'18\n18\n5\n',False),(b'20\n18\n19\n',False)):
            with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,'observed',return_value=config),mock.patch.object(drill.subprocess,'run',return_value=subprocess.CompletedProcess([],0,output,b'')) as command:
                if valid: self.assertEqual(drill.redis_lazy_observation({},Path(temp)/'raw'),(20,18,5))
                else:
                    with self.assertRaises(RuntimeError): drill.redis_lazy_observation({},Path(temp)/'raw')
                self.assertNotIn('private',command.call_args.args[0])
                self.assertEqual(command.call_args.kwargs['input'],b'private\n')
                lua=command.call_args.args[0][-1]
                self.assertLess(lua.index("redis.call('INFO','keyspace')"),lua.index("redis.call('SCAN'"))

    def test_redis_delay_pauses_only_actual_new_starting_container(self):
        old=json.dumps({"StartedAt":"old"}).encode()
        new=json.dumps({"StartedAt":"new","Running":True,"Health":{"Status":"starting"}}).encode()
        with mock.patch.object(drill,"observed",side_effect=[old,new,b"paused",b"unpaused"]) as observed,mock.patch.object(drill.time,"monotonic",side_effect=[0,0,0,65]):
            delay=drill.RedisDelay(); delay.stop=mock.Mock(); delay.stop.is_set.return_value=False; delay.stop.wait.return_value=False
            delay.watch()
        self.assertIsNone(delay.failure)
        self.assertEqual(delay.elapsed,65)
        self.assertEqual(observed.call_args_list[-2:], [mock.call(["docker","pause","sub2api-redis"]),mock.call(["docker","unpause","sub2api-redis"])])
        delay.stop.wait.assert_called_once_with(65)

    def test_redis_delay_refuses_already_healthy_or_interrupted_case(self):
        old=json.dumps({"StartedAt":"old"}).encode()
        for health,interrupted in (("healthy",False),("starting",True)):
            new=json.dumps({"StartedAt":"new","Running":True,"Health":{"Status":health}}).encode()
            with self.subTest(health=health),mock.patch.object(drill,"observed",side_effect=[old,new,b"paused",b"unpaused"]),mock.patch.object(drill.time,"monotonic",side_effect=[0,0,0,65]):
                delay=drill.RedisDelay(); delay.stop=mock.Mock(); delay.stop.is_set.return_value=False; delay.stop.wait.return_value=interrupted
                delay.watch()
                delay.thread=mock.Mock(); delay.thread.is_alive.return_value=False
                with self.assertRaises(RuntimeError): delay.finish()
                self.assertIsNotNone(delay.failure)

    def test_plain_synthetic_report_does_not_reach_schema2(self):
        with self.assertRaises(RuntimeError):
            evidence.validate_measurements({"checkpoints":{}},{})


if __name__=="__main__": unittest.main()
