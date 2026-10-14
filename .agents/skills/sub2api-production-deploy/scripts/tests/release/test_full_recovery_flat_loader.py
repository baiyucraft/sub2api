"""Import actual flat Guest aliases in a new process without checkout paths."""
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0,str(Path(__file__).resolve().parents[2]))
from release.process import run_hidden


class FullRecoveryFlatLoaderTests(unittest.TestCase):
    def test_actual_flat_loader_uses_verified_sources_package(self):
        source=Path(__file__).resolve().parents[2]/'release'
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp).resolve(); bundle=root/'bundle'; bundle.mkdir()
            scripts=bundle/'sources/.agents/skills/sub2api-production-deploy/scripts'
            shutil.copytree(source,scripts/'release',ignore=shutil.ignore_patterns('__pycache__'))
            for name in ('full_recovery_drill.py','full_recovery_guest.py','full_recovery_source.py','recovery_drill.py','recovery_bundle.py','full_recovery_drill_authorization.py','baseline_candidate_load.py','process.py'):
                shutil.copyfile(source/name,bundle/name)
            launcher=root/'check.py'
            launcher.write_text('''import json, pathlib, runpy, sys
bundle=pathlib.Path(sys.argv[1]).resolve()
sys.path.insert(0,str(bundle))
controller=runpy.run_path(str(bundle/'full_recovery_drill.py'))
assert not controller['__package__']
api=controller['mod']('recovery_evidence')
expected=bundle/'sources/.agents/skills/sub2api-production-deploy/scripts/release/recovery_evidence.py'
assert pathlib.Path(api.__file__).resolve()==expected
assert api.checksum({'schema':1})==controller['sha'](bundle/'expected.json')
for name in ('full_recovery_guest','full_recovery_source','recovery_drill','full_recovery_drill_authorization','baseline_candidate_load'):
    module=controller['mod'](name)
    assert pathlib.Path(module.__file__).resolve()==bundle/(name+'.py')
guest=controller['mod']('full_recovery_guest')
assert not hasattr(guest,'WORKSPACE')
try: controller['validate_guest_receipt']({'schema':1}, {})
except RuntimeError as error: assert 'Guest receipt fields' in str(error)
else: raise AssertionError('invalid receipt accepted')
print(json.dumps({'flat_loader':'verified_sources','guest_runtime':'standalone'}))
''',encoding='utf-8')
            (bundle/'expected.json').write_bytes(b'{"schema":1}')
            result=run_hidden([sys.executable,'-I','-B',str(launcher),str(bundle)],cwd=root,capture_output=True,timeout=30)
            self.assertEqual(result.returncode,0,result.stderr.decode('utf8','replace'))
            self.assertEqual(json.loads(result.stdout),{'flat_loader':'verified_sources','guest_runtime':'standalone'})


if __name__=='__main__': unittest.main()
