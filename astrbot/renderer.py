from __future__ import annotations

from datetime import datetime
import html
from typing import Any, Iterable


PAGE_SIZE = 8

PLATFORM_ORDER = ("openai", "anthropic", "grok", "gemini", "antigravity", "kimi", "zhipu", "deepseek")
PLATFORM_LABELS = {
    "openai": "OpenAI",
    "anthropic": "Anthropic",
    "grok": "Grok",
    "gemini": "Gemini",
    "antigravity": "Antigravity",
    "kimi": "Kimi",
    "zhipu": "智谱",
    "deepseek": "DeepSeek",
}


def svg_html_document(svg: str) -> str:
    return "<!doctype html><html><head><meta charset=\"utf-8\"></head><body style=\"margin:0\">" + svg + "</body></html>"


def _platform(snapshot: Any) -> str:
    return str(snapshot.monitor.get("provider") or "other").lower()


def _platform_groups(snapshots: Iterable[Any]) -> list[tuple[str, list[Any]]]:
    grouped: dict[str, list[Any]] = {}
    for snapshot in snapshots:
        grouped.setdefault(_platform(snapshot), []).append(snapshot)
    order = {name: index for index, name in enumerate(PLATFORM_ORDER)}
    return sorted(grouped.items(), key=lambda pair: (order.get(pair[0], len(order)), pair[0]))


def _format_latency(value: Any) -> str:
    try:
        number = float(value)
    except (TypeError, ValueError):
        return "—"
    if number < 1000:
        return f"{number:.0f} ms"
    return f"{number / 1000:.2f} s"


def _format_rate(value: Any) -> str:
    try:
        return f"{float(value):.2f}x"
    except (TypeError, ValueError):
        return "—"


def _group_rate(snapshot: Any) -> Any:
    group = snapshot.group or {}
    return group.get("rate_multiplier")


def _rate_text(snapshot: Any) -> str:
    if not snapshot.monitor.get("show_group_rate", True):
        return "—"
    return _format_rate(_group_rate(snapshot))


def _short(value: Any, limit: int) -> str:
    text = str(value or "-")
    return text if len(text) <= limit else text[: limit - 1] + "…"


def render_status_svg(snapshots: Iterable[Any], now: datetime | None = None) -> str:
    rows = list(snapshots)
    groups = _platform_groups(rows)
    row_height = 76
    group_gap = 30
    height = 126 + sum(42 + len(items) * row_height + group_gap for _, items in groups)
    generated_at = (now or datetime.now()).strftime("%Y-%m-%d %H:%M:%S")
    counts = {"正常": 0, "降级": 0, "异常": 0, "未知": 0}
    for snapshot in rows:
        counts[_status(snapshot.monitor)] += 1
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="980" height="{height}" viewBox="0 0 980 {height}">',
        '<rect width="980" height="100%" fill="#0b1220"/>',
        '<style>text{font-family:Arial,"Microsoft YaHei",sans-serif;letter-spacing:0}.title{fill:#f5f7fb;font-size:27px;font-weight:700}.muted{fill:#99a8bc;font-size:13px}.stat{fill:#dce5f1;font-size:14px;font-weight:700}.section{fill:#8fb8ff;font-size:14px;font-weight:700}.name{fill:#f5f7fb;font-size:16px;font-weight:700}.detail{fill:#aebdd0;font-size:13px}.rate{fill:#e7c77a;font-size:14px;font-weight:700}.ok{fill:#64d6a0;font-size:13px;font-weight:700}.warn{fill:#e7c77a;font-size:13px;font-weight:700}.bad{fill:#f28b8b;font-size:13px;font-weight:700}.unknown{fill:#aebdd0;font-size:13px;font-weight:700}</style>',
        '<text x="36" y="42" class="title">Sub2API 渠道状态</text>',
        f'<text x="36" y="67" class="muted">更新时间：{html.escape(generated_at)} · 共 {len(rows)} 个渠道</text>',
        f'<text x="36" y="96" class="stat">正常 {counts["正常"]}</text><text x="142" y="96" class="stat">降级 {counts["降级"]}</text><text x="248" y="96" class="stat">异常 {counts["异常"]}</text><text x="354" y="96" class="stat">未知 {counts["未知"]}</text>',
    ]
    if not rows:
        parts.append('<text x="36" y="135" class="detail">暂无已启用的渠道监控数据。</text>')
    index = 0
    y = 120
    for platform, items in groups:
        label = html.escape(PLATFORM_LABELS.get(platform, platform.upper()))
        parts.append(f'<text x="36" y="{y}" class="section">{label} · {len(items)} 个渠道</text>')
        y += 16
        for snapshot in items:
            index += 1
            item = snapshot.monitor
            status = _status(item)
            status_class = {"正常": "ok", "降级": "warn", "异常": "bad", "未知": "unknown"}[status]
            name = html.escape(_short(item.get("name") or item.get("id") or f"渠道 {index}", 34))
            model = html.escape(_short(item.get("primary_model"), 30))
            availability = html.escape(_number(item, "availability_7d", "availability", "success_rate"))
            group_name = html.escape(_short(item.get("group_name"), 24))
            parts.extend([
                f'<rect x="24" y="{y}" width="932" height="60" rx="6" fill="#111d2f" stroke="#243650"/>',
                f'<text x="44" y="{y + 25}" class="name">{name}</text>',
                f'<text x="44" y="{y + 46}" class="detail">{model} · {_format_latency(item.get("primary_latency_ms"))} · 可用率 {availability}% · 分组 {group_name}</text>',
                f'<text x="745" y="{y + 25}" class="rate">倍率 {_rate_text(snapshot)}</text>',
                f'<text x="882" y="{y + 25}" text-anchor="end" class="{status_class}">{status}</text>',
            ])
            y += row_height
        y += group_gap
    parts.append('</svg>')
    return "".join(parts)


def split_pages(text: str, page_size: int = PAGE_SIZE) -> list[str]:
    lines = text.splitlines() or [text]
    pages: list[str] = []
    for offset in range(0, len(lines), page_size):
        pages.append("\n".join(lines[offset : offset + page_size]))
    return pages or ["暂无状态数据"]


def _status(item: dict[str, Any]) -> str:
    value = str(item.get("primary_status") or item.get("status") or item.get("health_status") or item.get("state") or "").lower()
    if value in {"operational", "healthy", "ok", "normal", "up", "available"}:
        return "正常"
    if value in {"degraded", "warning", "partial"}:
        return "降级"
    if value in {"error", "failed", "down", "unavailable", "offline"}:
        return "异常"
    return "未知"


def _number(item: dict[str, Any], *keys: str) -> str:
    for key in keys:
        value = item.get(key)
        if value is not None and value != "":
            if isinstance(value, (float, int)):
                return f"{value:.2f}"
            return str(value)
    return "-"


def format_status(snapshots: Iterable[Any], now: datetime | None = None) -> str:
    rows = list(snapshots)
    counts = {"正常": 0, "降级": 0, "异常": 0, "未知": 0}
    for snapshot in rows:
        counts[_status(snapshot.monitor)] += 1
    timestamp = (now or datetime.now()).strftime("%Y-%m-%d %H:%M:%S")
    lines = [
        "Sub2API 渠道状态",
        f"更新时间：{timestamp}",
        f"渠道总数：{len(rows)} | 正常：{counts['正常']} | 降级：{counts['降级']} | 异常：{counts['异常']} | 未知：{counts['未知']}",
    ]
    if not rows:
        lines.append("暂无已启用的渠道监控数据。")
        return "\n".join(lines)
    index = 0
    for platform, items in _platform_groups(rows):
        lines.append(f"[{PLATFORM_LABELS.get(platform, platform.upper())}]")
        for snapshot in items:
            index += 1
            item = snapshot.monitor
            name = item.get("name") or item.get("channel_name") or item.get("id") or f"渠道 {index}"
            model = item.get("primary_model") or item.get("model") or item.get("model_name") or "-"
            availability = _number(item, "availability_7d", "availability", "uptime", "success_rate")
            lines.append(f"{index}. {name} [{_status(item)}] 模型：{model} | 延迟：{_format_latency(item.get('primary_latency_ms'))} | 可用率：{availability}% | 倍率：{_rate_text(snapshot)}")
    return "\n".join(lines)
