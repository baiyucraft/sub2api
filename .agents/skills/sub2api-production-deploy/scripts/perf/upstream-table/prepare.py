from __future__ import annotations
import argparse, hashlib, io, json, os, pathlib, shutil, subprocess, tarfile
ROOT=pathlib.Path(__file__).resolve().parents[6]
HERE=pathlib.Path(__file__).resolve().parent
BASELINE="8afa2ffe47800773f781279e945bf95d6c2e1067"
def main():
 p=argparse.ArgumentParser();p.add_argument("--version",choices=("baseline","optimized"),required=True);a=p.parse_args()
 out=ROOT/".tmp/upstream-table-perf";out.mkdir(parents=True,exist_ok=True);tree=out/a.version
 # Rebuild only the verified generated snapshot; deleted Go/embed files must
 # never survive from an earlier build and silently change the measured code.
 if not out.resolve().is_relative_to(ROOT.resolve()):raise ValueError("artifact directory outside workspace")
 if tree.exists():
  if tree.is_symlink() or tree.resolve()!=out.resolve()/a.version:raise ValueError("snapshot path")
  shutil.rmtree(tree)
 tree.mkdir()
 if a.version=="baseline":
  if not (tree/"backend/go.mod").exists():
   archive=subprocess.run(["git","archive",BASELINE,"backend"],cwd=ROOT,capture_output=True,check=True).stdout
   with tarfile.open(fileobj=io.BytesIO(archive)) as tf:
    for member in tf.getmembers():
     if not (tree/member.name).resolve().is_relative_to(tree.resolve()):raise ValueError("archive path")
    tf.extractall(tree)
 else:
  paths=subprocess.run(["git","ls-files","--cached","--others","--exclude-standard","-z","backend"],cwd=ROOT,capture_output=True,check=True).stdout.split(b"\0")
  for raw in paths:
   if not raw:continue
   relative=pathlib.Path(os.fsdecode(raw));src=ROOT/relative
   if not src.is_file():continue
   dest=tree/relative;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(src,dest)
 package=tree/"backend/internal/tableperf";package.mkdir(parents=True,exist_ok=True)
 shutil.copyfile(HERE/"matrix_test.go.txt",package/"matrix_test.go")
 shutil.copyfile(HERE/"perfmatrix_cache_stats.go.txt",tree/"backend/internal/upstreamcacheguard/perfmatrix_cache_stats.go")
 shutil.copyfile(HERE/"perfmatrix_cache_snapshot.go.txt",tree/"backend/internal/service/perfmatrix_cache_snapshot.go")
 if a.version=="baseline":shutil.copyfile(HERE/"baseline_quality_cache.go.txt",tree/"backend/internal/handler/admin/tableperf_quality_cache.go")
 manifest={str(p.relative_to(tree)):hashlib.sha256(p.read_bytes()).hexdigest() for p in tree.rglob("*.go")}
 manifest["fixture"]=hashlib.sha256((HERE/"matrix_test.go.txt").read_bytes()).hexdigest()
 manifest["fixture_sha256"]={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in HERE.glob("*.go.txt")}
 (out/(a.version+"-sources.json")).write_text(json.dumps(manifest,indent=2)+"\n",encoding="utf-8")
 env={**os.environ,"GOTOOLCHAIN":"go1.27.2","GOOS":"linux","GOARCH":"amd64","CGO_ENABLED":"0"}
 subprocess.run(["go","test","-c","-p","2","-parallel","2","-o",str(out/(a.version+"-matrix.test")),"./internal/tableperf"],cwd=tree/"backend",env=env,check=True)
 binary_sha256=hashlib.sha256((out/(a.version+"-matrix.test")).read_bytes()).hexdigest()
 manifest["binary_sha256"]=binary_sha256
 (out/(a.version+"-sources.json")).write_text(json.dumps(manifest,indent=2)+"\n",encoding="utf-8")
 print(json.dumps({"built":a.version,"sha256":binary_sha256}))
if __name__=="__main__":main()
