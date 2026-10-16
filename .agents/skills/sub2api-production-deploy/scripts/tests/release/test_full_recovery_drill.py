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


class Clock:
 def __init__(self):self.now=0.
 def monotonic(self):return self.now

class Event:
 def __init__(self,clock,checkpoint,phase_at,interrupted=False):
  self.clock=clock;self.checkpoint=checkpoint;self.phase_at=phase_at;self.interrupted=interrupted;self.stopped=False
 def is_set(self):return self.stopped
 def set(self):self.stopped=True
 def wait(self,seconds):
  if self.stopped:return True
  self.clock.now+=seconds
  if self.phase_at is not None and self.clock.now>=self.phase_at and not self.checkpoint.exists():self.checkpoint.write_bytes(b'schema=1\nphase=redis_restored\n')
  return self.interrupted if seconds>60 else False


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

    @unittest.skipUnless(os.name == "posix", "Guest systemd links require POSIX")
    def test_optional_boot_services_resolve_absolute_links_inside_guest_rootfs(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            units = root / "lib/systemd/system"
            wants = root / "etc/systemd/system/multi-user.target.wants"
            units.mkdir(parents=True)
            wants.mkdir(parents=True)
            for name in ("nginx.service", "systemd-resolved.service"):
                (units / name).write_text("[Unit]\n")
                (wants / name).symlink_to("/lib/systemd/system/" + name)
            drill.disable_optional_boot_services(root)
            self.assertFalse(any((wants / name).exists() or (wants / name).is_symlink() for name in ("nginx.service", "systemd-resolved.service")))

    @unittest.skipUnless(os.name == "posix", "Guest systemd links require POSIX")
    def test_optional_boot_services_reject_root_escape(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            wants = root / "etc/systemd/system/multi-user.target.wants"
            wants.mkdir(parents=True)
            (wants / "nginx.service").symlink_to("/etc/passwd")
            with self.assertRaises(RuntimeError):
                drill.disable_optional_boot_services(root)
            self.assertTrue((wants / "nginx.service").is_symlink())

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

    def test_guest_run_removes_owned_helper_python_cache_before_authorization(self):
        with tempfile.TemporaryDirectory() as temp:
            helper = Path(temp) / "helper"; cache = helper / "__pycache__"
            cache.mkdir(parents=True)
            (cache / "module.pyc").write_bytes(b"cache")
            drill.remove_helper_caches(helper)
            self.assertFalse(cache.exists())

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

    def test_lazy_expiry_uses_atomic_pairs_and_accepts_no_natural_expiry(self):
        config=json.dumps([{'Config':{'Entrypoint':['redis-server'],'Cmd':['--requirepass','private']}}]).encode()
        for output,valid in ((b'20\n5\n18\n3\n',True),(b'42170\n39123\n37472\n34425\n',True),(b'18\n5\n18\n5\n',True),(b'18\n0\n18\n0\n',True),(b'20\n19\n0\n0\n',False),(b'20\n21\n18\n19\n',False),(b'20\n5\n18\n4\n',False),(b'ERR unsupported nonzero Redis DB\n',False)):
            with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,'observed',return_value=config),mock.patch.object(drill.subprocess,'run',return_value=subprocess.CompletedProcess([],0,output,b'')) as command:
                if valid: self.assertEqual(drill.redis_lazy_observation({},Path(temp)/'raw'),tuple(map(int,output.splitlines())))
                else:
                    with self.assertRaises(RuntimeError): drill.redis_lazy_observation({},Path(temp)/'raw')
                self.assertNotIn('private',command.call_args.args[0])
                self.assertEqual(command.call_args.kwargs['input'],b'private\n')
                lua=command.call_args.args[0][-1]
                self.assertLess(lua.index("redis.call('INFO','keyspace')"),lua.index("redis.call('SCAN'"))
                self.assertIn("local after=snapshot()",lua)

    def boundary_probe(self,*,failure=None):
        token='f'*32;name='sub2api-ttl-boundary-'+token;cid='a'*64;image='sha256:'+'b'*64
        clock=Clock();commands=[];exists=False
        isolated={'drill_id':self.config['drill_id'],'release_id':self.config['release_id'],'rootfs_identity_sha256':'c'*64,'guest_docker_engine_id_sha256':hashlib.sha256(b'engine').hexdigest()}
        item={'Id':cid,'Image':image,'Name':'/'+name,'Config':{'Labels':{'sub2api.full-dr-ttl-boundary':token}},'HostConfig':{'NetworkMode':'none','ReadonlyRootfs':True,'Tmpfs':{'/data':'rw,noexec,nosuid,size=32m'}},'Mounts':[]}
        def execute(args,env,timeout):
            nonlocal exists
            commands.append(args)
            def reply(value='',code=0,error=''):return subprocess.CompletedProcess(args,code,value.encode(),error.encode())
            if args[1]=='run':
                exists=True
                if failure=='created-timeout':raise subprocess.TimeoutExpired(args,timeout)
                return reply(cid)
            if args[1:3]==['container','inspect']:
                if failure in ('daemon','permission'):return reply(code=1,error='Cannot connect to daemon' if failure=='daemon' else 'permission denied')
                if not exists:return reply(code=1,error='Error response from daemon: No such container: '+name)
                observed=deepcopy(item)
                if failure=='ownership':observed['Config']['Labels']['sub2api.full-dr-ttl-boundary']='foreign'
                return reply(json.dumps([observed]))
            if args[1]=='rm':
                if failure=='cleanup':return reply(code=1,error='daemon refused cleanup')
                exists=False;return reply(cid)
            if args[1]=='exec':
                if args[3:]==['cat','/proc/mounts']:return reply('tmpfs /data tmpfs rw,nosuid,noexec 0 0')
                op=args[5:]
                if op==['PING']:return reply('PONG')
                if op==['CONFIG','GET','enable-debug-command']:return reply('enable-debug-command\nyes')
                if op[:1] in (['DEBUG'],['SET']):return reply('OK')
                if op==['GET','boundary:persistent']:return reply('persistent-'+token)
                if op==['PTTL','boundary:ttl']:return reply('1900' if clock.now==0 else '-2')
                if op[:1]==['EVAL']:return reply('2\n1\n1\n0')
            raise AssertionError(args)
        def sleep(seconds):clock.now+=seconds
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(drill,'observed',return_value=b'engine'),mock.patch.object(drill,'run',side_effect=execute),mock.patch.object(drill.uuid,'uuid4',return_value=mock.Mock(hex=token)),mock.patch.object(drill.time,'sleep',side_effect=sleep),mock.patch.object(drill.time,'monotonic',side_effect=clock.monotonic),mock.patch.object(drill.time,'time',side_effect=lambda:1000+clock.now):
            try:return drill.redis_boundary_probe({},Path(temp)/'raw',image_id=image,isolated=isolated),commands,None
            except (RuntimeError,subprocess.TimeoutExpired) as error:return None,commands,error

    def test_boundary_probe_measures_owned_container_then_confirms_absence(self):
        result,commands,error=self.boundary_probe()
        self.assertIsNone(error);self.assertIs(result['production_source'],False)
        self.assertTrue(result['owned_resources_cleaned']);self.assertEqual(result['wait_elapsed_ms'],2200)
        self.assertEqual(result['persistent_before_sha256'],result['persistent_after_sha256'])
        self.assertEqual(commands[-1][1:3],['container','inspect'])
        run=commands[0]
        self.assertEqual(run[run.index('--network')+1],'none');self.assertIn('--read-only',run)
        self.assertIn('--tmpfs',run);self.assertNotIn('--volume',run);self.assertNotIn('--publish',run)

    def test_boundary_probe_run_timeout_still_cleans_created_owned_container(self):
        result,commands,error=self.boundary_probe(failure='created-timeout')
        self.assertIsNone(result);self.assertIsInstance(error,subprocess.TimeoutExpired)
        self.assertTrue(any(args[1]=='rm' for args in commands))
        self.assertEqual(commands[-1][1:3],['container','inspect'])

    def test_boundary_probe_inspect_errors_are_not_absence(self):
        for failure in ('daemon','permission'):
            with self.subTest(failure=failure):
                result,commands,error=self.boundary_probe(failure=failure)
                self.assertIsNone(result);self.assertIsInstance(error,RuntimeError)
                self.assertIn('cleanup unverified',str(error))
                self.assertFalse(any(args[1]=='rm' for args in commands))

    def test_boundary_probe_ownership_mismatch_never_removes_foreign_container(self):
        result,commands,error=self.boundary_probe(failure='ownership')
        self.assertIsNone(result);self.assertIsInstance(error,RuntimeError)
        self.assertIn('ownership differs',str(error));self.assertFalse(any(args[1]=='rm' for args in commands))

    def test_boundary_probe_cleanup_failure_never_returns_success(self):
        result,commands,error=self.boundary_probe(failure='cleanup')
        self.assertIsNone(result);self.assertIsInstance(error,RuntimeError)
        self.assertTrue(any(args[1]=='rm' for args in commands))

    def simulate(self,*,phase_at=.1,deadline=1800,health='starting',restart=True,interrupted=False,unpause_failure=False,pause_timeout=False,cancel=False):
     with tempfile.TemporaryDirectory() as temp:
      cp=Path(temp).resolve()/'redis_restored.started';clock=Clock();commands=[];states=0
      def observe(args,**kwargs):
       nonlocal states
       commands.append(args)
       if args[1]=='inspect':
        states+=1
        return json.dumps({'StartedAt':'old' if states==1 or not restart else 'new','Running':True,'Health':{'Status':health}}).encode()
       if args[1]=='pause' and pause_timeout:raise subprocess.TimeoutExpired(args,5)
       if args[1]=='unpause' and unpause_failure:raise RuntimeError('private diagnostics must not escape')
       return b''
      with mock.patch.object(drill,'observed',side_effect=observe),mock.patch.object(drill,'sf'),mock.patch.object(drill.time,'monotonic',side_effect=clock.monotonic):
       delay=drill.RedisDelay(cp,deadline=deadline)
       delay.stop=Event(clock,cp,phase_at,interrupted)
       if cancel:delay.stop.set()
       delay.watch()
       delay.thread=mock.Mock();delay.thread.is_alive.return_value=False
       if delay.failure:
        with self.assertRaisesRegex(RuntimeError,delay.failure):delay.finish()
       else:delay.finish()
      return delay,commands,clock.now

    def test_actual_new_starting_container_only(self):
     delay,commands,_=self.simulate()
     self.assertIsNone(delay.failure);self.assertEqual(delay.elapsed,65)
     self.assertEqual(commands[-2:],[['docker','pause','sub2api-redis'],['docker','unpause','sub2api-redis']])

    def test_healthy_before_injection_refused(self):
     delay,commands,_=self.simulate(health='healthy')
     self.assertEqual(delay.failure,'redis_delay_missed_starting')
     self.assertFalse(any(c[1]=='pause' for c in commands))

    def test_interrupted_hold_refused(self):
     delay,_,_=self.simulate(interrupted=True)
     self.assertEqual(delay.failure,'redis_delay_interrupted')

    def test_pg_longer_than_old_budget_preserves_full_restart_budget(self):
     delay,_,elapsed=self.simulate(phase_at=232)
     self.assertIsNone(delay.failure);self.assertGreater(elapsed,297)
     self.assertEqual(delay.phase,'redis_delay_complete')

    def test_phase_wait_has_own_bounded_deadline(self):
     delay,_,elapsed=self.simulate(phase_at=None,deadline=2)
     self.assertEqual(delay.failure,'redis_delay_phase_timeout');self.assertLess(elapsed,2.2)

    def test_restart_wait_is_bounded_from_phase(self):
     delay,_,elapsed=self.simulate(phase_at=232,restart=False)
     self.assertEqual(delay.failure,'redis_delay_restart_timeout')
     self.assertGreater(elapsed,382);self.assertLess(elapsed,383)

    def test_early_exit_cancels_phase_wait(self):
     delay,_,elapsed=self.simulate(phase_at=None,cancel=True)
     self.assertEqual(delay.failure,'redis_delay_cancelled');self.assertEqual(elapsed,0)

    def test_unpause_failure_has_stable_reason_and_stage(self):
     delay,_,_=self.simulate(unpause_failure=True)
     self.assertEqual(delay.failure,'redis_delay_unpause_failed')
     self.assertEqual(delay.phase,'redis_delay_unpause')
     self.assertNotIn('private',delay.failure)

    def test_pause_timeout_still_attempts_owned_unpause(self):
     delay,commands,_=self.simulate(pause_timeout=True)
     self.assertEqual(delay.failure,'redis_delay_observation_failed')
     self.assertEqual(delay.phase,'redis_delay_pause')
     self.assertEqual(commands[-1],['docker','unpause','sub2api-redis'])

    def test_runner_budget_truncation_cannot_pass_short_hold(self):
     delay,_,_=self.simulate(deadline=30)
     self.assertEqual(delay.failure,'redis_delay_interrupted')

    def test_last_guest_stage_persisted_for_non_decrypt_failures(self):
        error=RuntimeError('actual Redis natural lazy expiry unavailable')
        with mock.patch.object(sys,'argv',['full_recovery_drill','guest-run']),mock.patch.object(drill,'guest_run',side_effect=error),mock.patch.object(drill,'CURRENT_GUEST_STAGE','guest_redis_lazy_expiry'),mock.patch.object(drill,'private_write') as write,mock.patch('builtins.print') as output:
            with self.assertRaises(SystemExit) as stopped:
                drill.main()
            self.assertEqual(stopped.exception.code,1)
        path,raw,mode=write.call_args.args
        self.assertEqual(path,Path('/opt/sub2api-full-recovery/failure.json'))
        self.assertEqual(mode,0o400)
        self.assertEqual(json.loads(raw),{'schema':1,'status':'blocked','stage':'guest_redis_lazy_expiry','error_type':'RuntimeError','reason_code':'redis_lazy_expiry_unavailable'})
        self.assertNotIn(str(error),str(output.call_args_list))

    def test_plain_synthetic_report_does_not_reach_schema2(self):
        with self.assertRaises(RuntimeError):
            evidence.validate_measurements({"checkpoints":{}},{})


class ForeignResourceTests(unittest.TestCase):
    def setUp(self):
        self.container={'Id':'container-secret-name','Image':'image-a','Config':{'Env':['TOKEN=private-token']},'HostConfig':{'SecretOption':'private-host-value'},'Mounts':[{'Destination':'/z','Source':'/private-source','RW':True,'Mode':'rw'},{'Destination':'/a','Source':'private-volume','RW':False,'Mode':'ro'}],'RestartCount':0,'State':{'Status':'running','Running':True,'Paused':False,'Restarting':False,'OOMKilled':False,'Dead':False,'Pid':123,'ExitCode':0,'StartedAt':'earlier','FinishedAt':''}}
        self.volume={'Name':'private-volume-name','Driver':'local','Options':{'credential':'volume-secret'},'Labels':{}}
        self.network={'Id':'network-private-name','Name':'private-network','IPAM':{'Config':[{'Subnet':'first'},{'Subnet':'second'}]},'Options':{'secret':'network-secret'}}
        self.images=['image-a']

    def observed(self, argv):
        if argv[:3]==['docker','ps','-aq']:return (self.container['Id']+'\n').encode()
        if argv[:4]==['docker','image','ls','-q']:return ('\n'.join(self.images)+'\n').encode()
        if argv[:4]==['docker','volume','ls','-q']:return (self.volume['Name']+'\n').encode()
        if argv[:4]==['docker','network','ls','-q']:return (self.network['Id']+'\n').encode()
        if argv[:2]==['docker','inspect']:return json.dumps([self.container]).encode()
        if argv[:3]==['docker','volume','inspect']:return json.dumps([self.volume]).encode()
        if argv[:3]==['docker','network','inspect']:return json.dumps([self.network]).encode()
        raise AssertionError(argv)

    def snapshot(self, path=None):
        with mock.patch.object(drill,'observed',side_effect=self.observed):return drill.foreign_resources(path)

    def test_mount_order_is_not_identity_and_does_not_modify_input(self):
        before=self.snapshot();self.container['Mounts'].reverse();original=deepcopy(self.container['Mounts'])
        self.assertEqual(before,self.snapshot())
        self.assertEqual(self.container['Mounts'],original)

    def test_invalid_mount_shapes_and_duplicate_destinations_fail_closed(self):
        for mounts in (None,{},[None],[{}],[{'Destination':1}],[{'Destination':''}],[{'Destination':'relative'}],[{'Destination':'/nul\x00'}],[{'Destination':'/a'},{'Destination':'/a','RW':False}]):
            with self.subTest(mounts=mounts),self.assertRaises(RuntimeError):drill.normalize_mounts(mounts)

    def test_real_mount_and_process_changes_remain_bound(self):
        before=self.snapshot();original=deepcopy(self.container)
        changes=(lambda: self.container['Mounts'][0].update(Source='/other'),lambda: self.container['Mounts'][0].update(RW=False),lambda: self.container['Mounts'][0].update(UnknownField='new'),lambda: self.container.update(Image='other-image'),lambda: self.container.update(RestartCount=1),lambda: self.container['State'].update(Pid=124),lambda: self.container['Config'].update(Extra='changed'),lambda: self.container['HostConfig'].update(Extra='changed'))
        for change in changes:
            self.container=deepcopy(original);change()
            self.assertNotEqual(before,self.snapshot())

    def test_other_resource_changes_and_network_list_order_remain_bound(self):
        before=self.snapshot()
        self.images.append('extra-image');self.assertNotEqual(before,self.snapshot());self.images.pop()
        self.volume['Options']['credential']='changed';self.assertNotEqual(before,self.snapshot());self.volume['Options']['credential']='volume-secret'
        self.network['IPAM']['Config'].reverse();self.assertNotEqual(before,self.snapshot())

    def test_fingerprints_contain_only_fixed_fields_and_hashes(self):
        with mock.patch.object(drill,'private_write') as write:
            digest=self.snapshot(Path('/owned/foreign-before.fingerprints.json'))
        path,raw,mode=write.call_args.args
        self.assertEqual(path,Path('/owned/foreign-before.fingerprints.json'));self.assertEqual(mode,0o400)
        value=json.loads(raw);self.assertEqual(value['metadata_sha256'],digest)
        for forbidden in ('private-token','private-host-value','private-source','private-volume','network-secret','volume-secret','container-secret-name','network-private-name'):
            self.assertNotIn(forbidden,raw.decode())
        container=next(iter(value['resources']['containers'].values()))
        self.assertIn('Pid',container['state_fields']);self.assertIn('mounts',container['fields'])
        for kind,resources in value['resources'].items():
            for identity,resource in resources.items():
                self.assertRegex(identity,r'^[0-9a-f]{64}$');self.assertRegex(resource['metadata_sha256'],r'^[0-9a-f]{64}$')

    def test_fingerprint_write_failure_never_returns_success(self):
        with mock.patch.object(drill,'private_write',side_effect=FileExistsError('existing evidence')),self.assertRaises(FileExistsError):
            self.snapshot(Path('/owned/foreign-before.fingerprints.json'))

    @unittest.skipUnless(os.name=='posix' and hasattr(os,'geteuid') and os.geteuid()==0,'root-only output contract requires POSIX root')
    def test_fingerprint_file_is_private_and_exclusive(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp).resolve()/'foreign-before.fingerprints.json'
            self.snapshot(path)
            self.assertEqual(path.stat().st_uid,0);self.assertEqual(path.stat().st_mode&0o777,0o400)
            original=path.read_bytes()
            with self.assertRaises(FileExistsError):self.snapshot(path)
            self.assertEqual(path.read_bytes(),original)


if __name__=="__main__": unittest.main()
