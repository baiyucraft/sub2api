import asyncio
import importlib.util
import sys
import types
from datetime import datetime
from pathlib import Path
from unittest.mock import AsyncMock, Mock

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))

from client import Sub2APIClient, Sub2APIError, normalize_base_url, unwrap_response  # noqa: E402
from renderer import format_status, format_status_pages  # noqa: E402


NOW = datetime(2026, 9, 23, 12, 30)


class Snapshot:
    def __init__(self, monitor, group=None, history=()):
        self.monitor = monitor
        self.group = group
        self.history = history


def test_normalize_base_url_rejects_credentials_and_query():
    with pytest.raises(Sub2APIError):
        normalize_base_url("https://user:password@example.test")
    with pytest.raises(Sub2APIError):
        normalize_base_url("https://example.test?token=secret")


def test_unwrap_response_reads_standard_sub2api_envelope():
    assert unwrap_response({"code": 0, "message": "success", "data": {"items": [1]}}) == {"items": [1]}
    with pytest.raises(Sub2APIError) as error:
        unwrap_response({"code": 401, "message": "unauthorized"})
    assert error.value.status == 401


def _history_line(text):
    return next(line for line in text.splitlines() if line.startswith(("🟩", "🟨", "🟥", "⬜")))


def test_history_has_ten_real_squares_in_oldest_to_newest_order():
    statuses = ["operational", "degraded", "failed", "unknown"] * 3
    history = tuple(
        {"status": status, "checked_at": f"2026-09-22T12:{index:02d}:00Z"}
        for index, status in reversed(list(enumerate(statuses)))
    )
    text = format_status([Snapshot({"name": "顺序检查", "provider": "openai"}, history=history)], now=NOW)
    expected = "".join({"operational": "🟩", "degraded": "🟨", "failed": "🟥", "unknown": "⬜"}[s] for s in statuses[-10:])
    assert _history_line(text).startswith(expected)
    assert text.index(expected) < text.index("顺序检查")
    assert sum(_history_line(text).count(square) for square in ("🟩", "🟨", "🟥", "⬜")) == 10


def test_short_history_is_not_padded():
    snapshot = Snapshot({"name": "少量记录", "provider": "openai"}, history=({"status": "healthy"}, {"status": "degraded"}))
    assert _history_line(format_status([snapshot], now=NOW)).startswith("🟩🟨  ")
    assert "⬜" not in _history_line(format_status([snapshot], now=NOW))


def test_format_status_includes_all_fields_and_real_group_rate():
    snapshot = Snapshot(
        {"name": "完整字段渠道", "provider": "openai", "primary_status": "healthy", "primary_model": "gpt-5.6-sol",
         "group_name": "gpt-pro", "primary_ttft_ms": 123, "primary_latency_ms": 2345,
         "availability_24h": 99.61, "availability_7d": 98.25, "rate_multiplier": 0.99, "max_multiplier": 0.88},
        group={"rate_multiplier": 0.22},
        history=({"status": "healthy", "ping_latency_ms": 22},),
    )
    text = format_status([snapshot], now=NOW)
    assert text == ("[OpenAI · 1 个渠道]\n🟩  正常\n"
                    "完整字段渠道 [0.22x] | 123 ms | 99.61%\n\n更新时间：2026-09-23 12:30:00")
    for value in ("gpt-5.6-sol", "gpt-pro", "2.35 s", "22 ms", "98.25%", "首字耗时"):
        assert value not in text
    assert "0.99x" not in text and "0.88x" not in text


def test_format_status_groups_platforms_and_keeps_time_at_end():
    snapshots = [
        Snapshot({"name": "其他渠道", "provider": "grok", "primary_status": "mystery"}),
        Snapshot({"name": "正常渠道", "provider": "openai", "primary_status": "operational"}),
        Snapshot({"name": "降级渠道", "provider": "openai", "primary_status": "degraded"}, history=({"status": "degraded"},)),
        Snapshot({"name": "异常渠道", "provider": "anthropic", "primary_status": "failed"}, history=({"status": "failed"},)),
    ]
    text = format_status(snapshots, now=NOW)
    assert "[OpenAI · 2 个渠道]" in text
    assert "[Anthropic · 1 个渠道]" in text
    assert "[Grok · 1 个渠道]" in text
    assert "🟨  降级" in text and "🟥  异常" in text and "暂无探测记录  未知" in text
    assert text.index("OpenAI") < text.index("Anthropic") < text.index("Grok")
    assert text.splitlines()[-1] == "更新时间：2026-09-23 12:30:00"


def test_format_status_handles_empty_and_missing_data():
    empty = format_status([], now=NOW)
    assert "暂无已启用的渠道监控数据" in empty
    assert empty.splitlines()[-1] == "更新时间：2026-09-23 12:30:00"
    assert format_status_pages([], now=NOW) == [empty]
    missing = format_status([Snapshot({"name": "缺失字段", "provider": "openai", "availability_7d": 98.3})], now=NOW)
    assert "缺失字段 [—] | — | —" in missing
    assert "98.30%" not in missing
    assert "暂无探测记录" in missing
    zero = format_status([Snapshot({"name": "故障", "availability_24h": 0})], now=NOW)
    assert "0.00%" in zero


def test_pages_keep_whole_channel_blocks_and_repeat_platform_title():
    names = [f"渠道-{index}-" + "长名称" * 9 for index in range(5)]
    snapshots = [Snapshot({"name": name, "provider": "openai", "primary_model": "gpt-5"}) for name in names]
    pages = format_status_pages(snapshots, now=NOW, max_chars=200)
    assert len(pages) > 1
    assert all(len(page) <= 200 for page in pages)
    for name in names:
        assert sum(name in page for page in pages) == 1
    for page in pages:
        assert "OpenAI" in page
        assert page.index("OpenAI") < min(page.index(name) for name in names if name in page)
        assert page.splitlines()[-1] == "更新时间：2026-09-23 12:30:00"
    complete = format_status(snapshots, now=NOW)
    assert all(name in complete for name in names)
    assert format_status_pages(snapshots, now=NOW, max_chars=10000) == [complete]


def test_pages_repeat_heading_when_platform_changes_at_page_boundary():
    rows = [Snapshot({"name": "A", "provider": "openai"}), Snapshot({"name": "B", "provider": "anthropic"})]
    pages = format_status_pages(rows, now=NOW, max_chars=60)
    assert len(pages) == 2
    assert pages[0].startswith("[OpenAI · 1 个渠道]")
    assert pages[1].startswith("[Anthropic · 1 个渠道]")


def test_fetch_snapshots_filters_primary_model_and_limits_history(monkeypatch):
    client = Sub2APIClient("https://example.test", "test-key")
    requests = []

    async def fake_json(_session, path, **params):
        requests.append((path, params))
        if path == "admin/channel-monitors":
            return {"items": [{"id": 17, "provider": "openai", "primary_model": "gpt-5.6-sol", "availability_24h": 99.61}]}
        if path.endswith("/history"):
            return {"items": [
                {"status": "degraded", "checked_at": "2026-09-23T12:02:00Z"},
                {"status": "operational", "checked_at": "2026-09-23T12:01:00Z"},
            ]}
        return {}

    monkeypatch.setattr(client, "_json", fake_json)
    snapshots = asyncio.run(client.fetch_snapshots())
    assert ("admin/channel-monitors/17/history", {"model": "gpt-5.6-sol", "limit": 10}) in requests
    assert [row["status"] for row in snapshots[0].history] == ["operational", "degraded"]
    assert "99.61%" in format_status(snapshots[0:1], now=NOW)


@pytest.fixture
def mocked_main(monkeypatch):
    class MessageChain:
        def __init__(self, components=()):
            self.components = list(components)

        def message(self, text):
            self.components.append(text)
            return self

    class Star:
        def __init__(self, context):
            self.context = context

    class Filter:
        class EventMessageType:
            ALL = object()

        @staticmethod
        def command(_name):
            return lambda method: method

        @staticmethod
        def event_message_type(_kind):
            return lambda method: method

    package = types.ModuleType("astrbot")
    package.__path__ = []
    api = types.ModuleType("astrbot.api")
    api.AstrBotConfig = dict
    api.logger = Mock()
    event_api = types.ModuleType("astrbot.api.event")
    event_api.AstrMessageEvent = object
    event_api.MessageChain = MessageChain
    event_api.filter = Filter
    components = types.ModuleType("astrbot.api.message_components")
    components.At = object
    components.Image = type("Image", (), {"fromFileSystem": Mock(side_effect=AssertionError("image send"))})
    components.Plain = str
    star = types.ModuleType("astrbot.api.star")
    star.Context = object
    star.Star = Star
    for name, module in (
        ("astrbot", package), ("astrbot.api", api), ("astrbot.api.event", event_api),
        ("astrbot.api.message_components", components), ("astrbot.api.star", star),
    ):
        monkeypatch.setitem(sys.modules, name, module)
    spec = importlib.util.spec_from_file_location("isolated_astrbot_main", Path(__file__).parents[1] / "main.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module, MessageChain


def test_status_command_sends_text_pages_only(mocked_main, monkeypatch):
    module, MessageChain = mocked_main
    plugin = module.Main(Mock(), {})
    plugin._snapshots = AsyncMock(return_value=(Snapshot({"name": "查询渠道", "provider": "openai"}),))
    plugin.html_render = AsyncMock(side_effect=AssertionError("image render"))
    event = Mock(send=AsyncMock(), image_result=Mock(side_effect=AssertionError("image result")))
    monkeypatch.setattr(module, "format_status_pages", Mock(return_value=["第一页", "第二页"]), raising=False)

    asyncio.run(plugin.status(event))

    assert [call.args[0].components for call in event.send.await_args_list] == [["第一页"], ["第二页"]]
    assert all(isinstance(call.args[0], MessageChain) for call in event.send.await_args_list)
    plugin.html_render.assert_not_awaited()
    event.image_result.assert_not_called()


def test_hourly_targets_send_text_pages_to_both_platforms(mocked_main, monkeypatch):
    module, MessageChain = mocked_main
    context = Mock(send_message=AsyncMock())
    plugin = module.Main(context, {"qq_status_group_ids": "qq-1", "telegram_status_chat_ids": "tg-1"})
    plugin.html_render = AsyncMock(side_effect=AssertionError("image render"))
    monkeypatch.setattr(module, "format_status_pages", Mock(return_value=["第一页", "第二页"]), raising=False)

    asyncio.run(plugin._send_status_targets((Snapshot({"name": "定时渠道"}),)))

    sent = [(call.args[0], call.args[1]) for call in context.send_message.await_args_list]
    assert [(origin, chain.components) for origin, chain in sent] == [
        ("qq_official:GroupMessage:qq-1", ["第一页"]),
        ("qq_official:GroupMessage:qq-1", ["第二页"]),
        ("telegram:GroupMessage:tg-1", ["第一页"]),
        ("telegram:GroupMessage:tg-1", ["第二页"]),
    ]
    assert all(isinstance(chain, MessageChain) for _, chain in sent)
    plugin.html_render.assert_not_awaited()
