from __future__ import annotations

from datetime import datetime, timedelta, timezone
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


def _first_value(item: dict[str, Any], *keys: str) -> Any:
    for key in keys:
        value = item.get(key)
        if value is not None and value != "":
            return value
    return None


def _format_percent(value: Any) -> str:
    try:
        return f"{float(value):.2f}%"
    except (TypeError, ValueError):
        return "—"


def _metric_text(snapshot: Any, *keys: str) -> str:
    value = _first_value(snapshot.monitor, *keys)
    if value is None:
        for entry in reversed(getattr(snapshot, "history", ()) or ()):
            value = _first_value(entry, *keys)
            if value is not None:
                break
    return _format_latency(value)


def _history_status(entry: dict[str, Any]) -> str:
    if entry.get("success") is True or entry.get("ok") is True:
        return "正常"
    if entry.get("success") is False or entry.get("ok") is False:
        return "异常"
    return _status(entry)


def _history_bars(snapshot: Any, count: int = 60) -> list[str]:
    history = list(getattr(snapshot, "history", ()) or ())[-count:]
    bars = ["未知"] * max(0, count - len(history))
    bars.extend(_history_status(entry) for entry in history)
    return bars[-count:]


def _parse_checked_at(value: Any) -> datetime | None:
    if not value:
        return None
    try:
        parsed = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def _availability(snapshot: Any, now: datetime) -> tuple[str, str]:
    item = snapshot.monitor
    direct = _first_value(item, "availability_24h", "availability_1d", "uptime_24h", "success_rate_24h")
    if direct is not None:
        return _format_percent(direct), "24 小时"

    history = list(getattr(snapshot, "history", ()) or ())
    current = now.astimezone() if now.tzinfo is None else now
    current = current.astimezone(timezone.utc)
    recent = [
        entry
        for entry in history
        if (checked_at := _parse_checked_at(entry.get("checked_at"))) is not None
        and current - timedelta(hours=24) <= checked_at <= current
    ]
    if recent:
        healthy = sum(_history_status(entry) == "正常" for entry in recent)
        return f"{healthy / len(recent) * 100:.2f}%", "24 小时"

    fallback = _first_value(item, "availability_7d", "availability", "success_rate")
    if fallback is not None:
        label = "7 天" if item.get("availability_7d") is not None else "可用性"
        return _format_percent(fallback), label
    return "—", "近 60 次"


def render_status_svg(snapshots: Iterable[Any], now: datetime | None = None) -> str:
    rows = list(snapshots)
    groups = _platform_groups(rows)
    card_height = 292
    group_gap = 28
    height = 132 + sum(32 + len(items) * card_height + group_gap for _, items in groups)
    render_time = now or datetime.now()
    generated_at = render_time.strftime("%Y-%m-%d %H:%M:%S")
    counts = {"正常": 0, "降级": 0, "异常": 0, "未知": 0}
    for snapshot in rows:
        counts[_status(snapshot.monitor)] += 1
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="980" height="{height}" viewBox="0 0 980 {height}">',
        '<rect width="980" height="100%" fill="#eef5f5"/>',
        '<style>text{font-family:Arial,"Microsoft YaHei",sans-serif;letter-spacing:0}.title{fill:#1e293b;font-size:27px;font-weight:700}.muted{fill:#718096;font-size:13px}.stat{fill:#334155;font-size:14px;font-weight:700}.section{fill:#334155;font-size:16px;font-weight:700}.name{fill:#1f2937;font-size:18px;font-weight:700}.detail{fill:#64748b;font-size:13px}.label{fill:#94a3b8;font-size:12px}.metric{fill:#1e293b;font-size:18px;font-weight:700}.unit{fill:#94a3b8;font-size:11px}.rate{fill:#ca8a04;font-size:13px;font-weight:700}.availability{fill:#16a34a;font-size:31px;font-weight:700}.availability-unit{fill:#16a34a;font-size:14px;font-weight:700}.status-ok{fill:#16a34a;font-size:13px;font-weight:700}.status-warn{fill:#ca8a04;font-size:13px;font-weight:700}.status-bad{fill:#dc2626;font-size:13px;font-weight:700}.status-unknown{fill:#64748b;font-size:13px;font-weight:700}</style>',
        '<text x="36" y="42" class="title">Sub2API 渠道状态</text>',
        f'<text x="36" y="67" class="muted">更新时间：{html.escape(generated_at)} · 共 {len(rows)} 个渠道</text>',
        f'<text x="36" y="96" class="stat">正常 {counts["正常"]}</text><text x="142" y="96" class="stat">降级 {counts["降级"]}</text><text x="248" y="96" class="stat">异常 {counts["异常"]}</text><text x="354" y="96" class="stat">未知 {counts["未知"]}</text>',
    ]
    if not rows:
        parts.append('<text x="36" y="135" class="detail">暂无已启用的渠道监控数据。</text>')
    index = 0
    y = 122
    for platform, items in groups:
        label = html.escape(PLATFORM_LABELS.get(platform, platform.upper()))
        parts.append(f'<circle cx="38" cy="{y - 5}" r="5" fill="#22c55e"/><text x="52" y="{y}" class="section">{label} · {len(items)} 个渠道</text>')
        y += 18
        for snapshot in items:
            index += 1
            item = snapshot.monitor
            status = _status(item)
            status_class = {"正常": "status-ok", "降级": "status-warn", "异常": "status-bad", "未知": "status-unknown"}[status]
            name = html.escape(_short(item.get("name") or item.get("id") or f"渠道 {index}", 34))
            provider = html.escape(_short(item.get("provider") or platform, 18))
            model = html.escape(_short(item.get("primary_model") or item.get("model"), 30))
            availability, availability_window = _availability(snapshot, render_time)
            rate = html.escape(_rate_text(snapshot))
            card_y = y
            card_x = 24
            card_w = 932
            metric_y = card_y + 78
            metric_w = 278
            metrics = [
                ("首字耗时", _metric_text(snapshot, "primary_ttft_ms", "ttft_ms", "first_byte_ms", "time_to_first_byte_ms")),
                ("总耗时", _metric_text(snapshot, "primary_latency_ms", "latency_ms", "total_latency_ms", "response_time_ms", "duration_ms", "elapsed_ms")),
                ("端点 PING", _metric_text(snapshot, "primary_ping_latency_ms", "ping_latency_ms", "endpoint_ping_ms", "ping_ms", "network_latency_ms", "connect_latency_ms")),
            ]
            status_fill = {"正常": "#22c55e", "降级": "#f59e0b", "异常": "#ef4444", "未知": "#94a3b8"}[status]
            parts.extend([
                f'<rect x="{card_x}" y="{card_y}" width="{card_w}" height="268" rx="18" fill="#fbfdfd" stroke="#cbd5e1"/>',
                f'<circle cx="54" cy="{card_y + 31}" r="17" fill="#dcfce7"/><circle cx="54" cy="{card_y + 31}" r="6" fill="#22c55e"/>',
                f'<text x="82" y="{card_y + 37}" class="name">{name}</text>',
                f'<rect x="852" y="{card_y + 16}" width="78" height="30" rx="15" fill="{status_fill}20"/><text x="891" y="{card_y + 36}" text-anchor="middle" class="{status_class}">{status}</text>',
                f'<text x="82" y="{card_y + 59}" class="detail">{provider} · {model}</text>',
                f'<text x="742" y="{card_y + 59}" class="rate">普通倍率 {rate}</text>',
            ])
            for offset, (metric_label, metric_value) in enumerate(metrics):
                metric_x = 44 + offset * 294
                parts.extend([
                    f'<rect x="{metric_x}" y="{metric_y}" width="{metric_w}" height="58" rx="13" fill="#f8fafc" stroke="#e2e8f0"/>',
                    f'<text x="{metric_x + 16}" y="{metric_y + 20}" class="label">{html.escape(metric_label)}</text>',
                    f'<text x="{metric_x + 16}" y="{metric_y + 43}" class="metric">{html.escape(metric_value)}</text>',
                ])
            availability_y = card_y + 151
            bars = _history_bars(snapshot)
            parts.extend([
                f'<rect x="44" y="{availability_y}" width="892" height="92" rx="12" fill="#f8fcfc" stroke="#dbe5e5"/>',
                f'<text x="60" y="{availability_y + 27}" class="label">可用性 · {html.escape(availability_window)}</text>',
                f'<text x="858" y="{availability_y + 34}" text-anchor="end" class="availability">{html.escape(availability.replace("%", ""))}</text><text x="870" y="{availability_y + 34}" class="availability-unit">%</text>',
                f'<line x1="60" y1="{availability_y + 44}" x2="920" y2="{availability_y + 44}" stroke="#e2e8f0"/>',
                f'<text x="60" y="{availability_y + 66}" class="label">近 60 次记录</text>',
                f'<text x="920" y="{availability_y + 66}" text-anchor="end" class="label">过去 ←　　　　　　　　　现在</text>',
            ])
            bar_x = 60
            for bar_status in bars:
                bar_fill = {"正常": "#10b981", "降级": "#f59e0b", "异常": "#ef4444", "未知": "#cbd5e1"}[bar_status]
                parts.append(f'<rect x="{bar_x}" y="{availability_y + 74}" width="5" height="20" rx="2" fill="{bar_fill}"><title>{html.escape(bar_status)}</title></rect>')
                bar_x += 14
            y += card_height
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
