"""Live identity includes termination state, not just retained OS objects."""
import ctypes
import os
from pathlib import Path
import sys
from types import SimpleNamespace
from unittest import mock
import pytest
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import process as p

class Function:
    def __init__(self, call): self.call = call
    def __call__(self, *args): return self.call(*args)

class Kernel:
    def __init__(self, states=(258,258), opened=True, queried=True):
        self.handle = (1 << 48) + 123
        self.closed = []
        self.states = iter(states)
        self.OpenProcess = Function(lambda *_: self.handle if opened else 0)
        self.WaitForSingleObject = Function(self.wait)
        self.GetProcessTimes = Function(lambda h,c,*_: self.times(h,c,queried))
        self.CloseHandle = Function(lambda h: self.closed.append(h) or 1)
    def wait(self,h,_):
        assert h == self.handle
        return next(self.states)
    def times(self,h,c,ok):
        assert h == self.handle
        c._obj.value=123456789
        return ok

@pytest.mark.parametrize("states,expected", [((258,258),"win:123456789"),((0,),None),((258,0),None)])
def test_windows_live_retained_exit_and_exit_race(states,expected):
    k=Kernel(states)
    with mock.patch.object(p,"os",SimpleNamespace(name="nt")), mock.patch.object(p.ctypes,"WinDLL",return_value=k,create=True):
        assert p.process_identity(42)==expected
    assert k.closed==[k.handle]
    assert k.OpenProcess.restype is ctypes.c_void_p
    assert k.WaitForSingleObject.argtypes[0] is ctypes.c_void_p

@pytest.mark.parametrize("states,queried", [((0xffffffff,),True),((258,),False),((258,0xffffffff),True)])
def test_windows_unknown_never_means_dead_and_closes(states,queried):
    k=Kernel(states,queried=queried)
    with mock.patch.object(p,"os",SimpleNamespace(name="nt")), mock.patch.object(p.ctypes,"WinDLL",return_value=k,create=True):
        with pytest.raises(RuntimeError,match="uncertain"): p.process_identity(42)
    assert k.closed==[k.handle]

@pytest.mark.parametrize("error,dead",[(87,True),(5,False)])
def test_windows_missing_vs_denied(error,dead):
    k=Kernel(opened=False)
    with mock.patch.object(p,"os",SimpleNamespace(name="nt")), mock.patch.object(p.ctypes,"WinDLL",return_value=k,create=True), mock.patch.object(p.ctypes,"get_last_error",return_value=error,create=True):
        if dead: assert p.process_identity(42) is None
        else:
            with pytest.raises(RuntimeError,match="uncertain"): p.process_identity(42)
    assert not k.closed

@pytest.mark.parametrize("status,expected",[("S","linux:boot:99"),("Z",None),("X",None)])
def test_linux_states(status,expected):
    data="1 (complex ) name) "+" ".join([status]+["0"]*18+["99"])
    with mock.patch.object(p,"os",SimpleNamespace(name="posix")),mock.patch.object(p,"Path",return_value=SimpleNamespace(read_text=mock.Mock(side_effect=[data,"boot"]))):
        assert p.process_identity(1)==expected

@pytest.mark.parametrize("failure",[PermissionError(),UnicodeError(),OSError()])
def test_linux_unknown(failure):
    with mock.patch.object(p,"os",SimpleNamespace(name="posix")),mock.patch.object(p,"Path",return_value=SimpleNamespace(read_text=mock.Mock(side_effect=failure))):
        with pytest.raises(RuntimeError,match="uncertain"):p.process_identity(1)

def test_linux_missing():
    with mock.patch.object(p,"os",SimpleNamespace(name="posix")),mock.patch.object(p,"Path",return_value=SimpleNamespace(read_text=mock.Mock(side_effect=FileNotFoundError()))):
        assert p.process_identity(1) is None

@pytest.mark.skipif(os.name!="nt",reason="native Windows retained handle")
@pytest.mark.parametrize("exit_code",[1,259])
def test_native_exited_object_with_retained_popen_handle(exit_code):
    child=p.popen_detached_worker([sys.executable,"-c",f"raise SystemExit({exit_code})"])
    try:
        child.wait(timeout=15)
        assert child._handle
        assert p.process_identity(child.pid) is None
    finally:
        if child.poll() is None:child.terminate();child.wait()

@pytest.mark.skipif(os.name!="nt",reason="native Windows live identity")
def test_native_live_identity():
    first=p.process_identity(os.getpid())
    assert first.startswith("win:") and first==p.process_identity(os.getpid())


@pytest.mark.parametrize("data",["malformed", "1 (name) S", "1 (name) S "+"0 "*18+"invalid"])
def test_linux_malformed_is_unknown(data):
    with mock.patch.object(p,"os",SimpleNamespace(name="posix")),mock.patch.object(p,"Path",return_value=SimpleNamespace(read_text=mock.Mock(return_value=data))):
        with pytest.raises(RuntimeError,match="uncertain"): p.process_identity(1)

def test_entrypoints_share_helper_and_keep_unknown_fail_closed():
    from release import supervisor, plugin_supervisor, finalization
    for module,name in ((supervisor,"_process_token"),(plugin_supervisor,"_process_token"),(finalization,"_process_identity")):
        with mock.patch.object(module,"process_identity",return_value="win:999") as query:
            assert getattr(module,name)(42)=="win:999"
            query.assert_called_once_with(42)
        with mock.patch.object(module,"process_identity",side_effect=RuntimeError("uncertain")):
            with pytest.raises(RuntimeError,match="uncertain"): getattr(module,name)(42)
    for module in (supervisor,plugin_supervisor):
        with mock.patch.object(module,"process_identity",return_value="win:999"):
            assert not module._runner_alive({"pid":42,"process_token":"win:998","status":"running"})
