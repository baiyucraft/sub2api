import sys
from datetime import datetime
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))

from client import Sub2APIError, normalize_base_url, unwrap_response  # noqa: E402
from renderer import format_status, render_status_svg, split_pages  # noqa: E402


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


def test_split_pages_keeps_page_size():
    assert split_pages("\n".join(f"line-{i}" for i in range(17)), page_size=8) == [
        "\n".join(f"line-{i}" for i in range(8)),
        "\n".join(f"line-{i}" for i in range(8, 16)),
        "line-16",
    ]


def test_format_status_includes_counts_and_channel_metrics():
    text = format_status(
        [
            Snapshot({
                "id": 1,
                "name": "OpenAI",
                "provider": "openai",
                "primary_status": "healthy",
                "primary_model": "gpt-5",
                "primary_latency_ms": 123.456,
                "availability_7d": 99.9,
                "show_group_rate": True,
                "provider": "openai",
            }, {"rate_multiplier": 0.22}),
            Snapshot({"id": 2, "name": "备用", "status": "down", "provider": "grok"}),
        ],
        now=datetime(2026, 9, 6, 12, 30, 0),
    )
    assert "渠道总数：2" in text
    assert "正常：1" in text
    assert "异常：1" in text
    assert "123 ms" in text
    assert "2026-09-06 12:30:00" in text
    assert "倍率：0.22x" in text


def test_format_status_handles_empty_data():
    text = format_status([], now=datetime(2026, 9, 6, 12, 30, 0))
    assert "渠道总数：0" in text
    assert "暂无已启用的渠道监控数据" in text


def test_render_status_svg_escapes_channel_name():
    svg = render_status_svg([Snapshot({"name": "A<&", "primary_status": "healthy", "provider": "openai"}, {"rate_multiplier": 0.2})])
    assert "A&lt;&amp;" in svg
    assert "倍率 0.20x" in svg
    assert "<script" not in svg


def test_render_status_svg_uses_detail_card_and_history_bars():
    svg = render_status_svg([
        Snapshot(
            {
                "name": "gpt-plus",
                "provider": "openai",
                "primary_status": "healthy",
                "primary_model": "gpt-5.6-sol",
                "availability_24h": 99.61,
            },
            {"rate_multiplier": 0.22},
            ({"status": "healthy"}, {"status": "degraded"}),
        )
    ])
    assert "端点 PING" in svg
    assert "可用性 · 24 小时" in svg
    assert "近 60 次记录" in svg
    assert "普通倍率 0.22x" in svg
    assert "趋势" not in svg
    assert svg.count("<title>") == 60
