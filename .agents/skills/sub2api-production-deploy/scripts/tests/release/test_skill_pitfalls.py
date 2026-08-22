from pathlib import Path


SKILL_ROOT = Path(__file__).parents[3]
SKILL = SKILL_ROOT / "SKILL.md"
PITFALLS = SKILL_ROOT / "references" / "known-pitfalls.md"


def test_skill_requires_associated_surface_audit_after_repairs() -> None:
    text = SKILL.read_text(encoding="utf-8")
    assert "每次修复后的关联面审计" in text
    assert "重新生成完整 40 位 commit、Gate、candidate archive/image 和唯一 release ID" in text
    assert "先按结构化状态做 reconciliation" in text


def test_profile_242_health_only_contract_is_documented() -> None:
    skill = SKILL.read_text(encoding="utf-8")
    pitfalls = PITFALLS.read_text(encoding="utf-8")
    for text in (skill, pitfalls):
        assert "profile 242" in text
        assert "health-only" in text
        assert "不读取账号池" in text or "不得读取账号池" in text
        assert "不发送模型/upstream 请求" in text
        assert "usage attribution" in text
    assert "probe_*" in pitfalls


def test_pitfalls_keep_repair_identity_and_reconciliation_contract() -> None:
    text = PITFALLS.read_text(encoding="utf-8")
    assert "修复后只重跑局部阶段导致证据漂移" in text
    assert "新的完整 40 位 commit" in text
    assert "新的 candidate archive/image" in text
    assert "禁止并发或重复 `deploy`" in text


def test_skill_forbids_incremental_trial_and_error_runner_retries() -> None:
    skill = SKILL.read_text(encoding="utf-8")
    pitfalls = PITFALLS.read_text(encoding="utf-8")
    assert "一次性修复门禁" in skill
    assert "清单未完成不得启动 runner" in skill
    assert "禁止在旧 candidate、旧 Gate、旧 release 或已启动 runner 上追加修复" in skill
    assert "逐点试错修复造成重复 runner" in pitfalls
    assert "一次性合并同根因修复" in pitfalls
