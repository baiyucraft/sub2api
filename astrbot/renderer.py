from __future__ import annotations

from datetime import datetime
from typing import Any, Iterable


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
STATUS_SYMBOLS = {"正常": "🟩", "降级": "🟨", "异常": "🟥", "未知": "⬜"}


def _platform(snapshot: Any) -> str:
    return str(snapshot.monitor.get("provider") or "other").lower()


def _platform_groups(snapshots: Iterable[Any]) -> list[tuple[str, list[Any]]]:
    grouped: dict[str, list[Any]] = {}
    for snapshot in snapshots:
        grouped.setdefault(_platform(snapshot), []).append(snapshot)
    order = {name: index for index, name in enumerate(PLATFORM_ORDER)}
    return sorted(grouped.items(), key=lambda pair: (order.get(pair[0], len(order)), pair[0]))


def _status(item: dict[str, Any]) -> str:
    value = str(item.get("primary_status") or item.get("status") or item.get("health_status") or item.get("state") or "").lower()
    if value in {"operational", "healthy", "ok", "normal", "up", "available"}:
        return "正常"
    if value in {"degraded", "warning", "partial"}:
        return "降级"
    if value in {"error", "failed", "down", "unavailable", "offline"}:
        return "异常"
    return "未知"


def _history_status(item: dict[str, Any]) -> str:
    if item.get("success") is True or item.get("ok") is True:
        return "正常"
    if item.get("success") is False or item.get("ok") is False:
        return "异常"
    return _status(item)


def _ordered_history(snapshot: Any) -> list[dict[str, Any]]:
    history = list(getattr(snapshot, "history", ()) or ())
    if history and all(item.get("checked_at") for item in history):
        history.sort(key=lambda item: str(item["checked_at"]))
    return history


def _history_symbols(snapshot: Any) -> str:
    history = _ordered_history(snapshot)
    return "".join(STATUS_SYMBOLS[_history_status(item)] for item in history[-10:]) or "暂无探测记录"


def _first_value(item: dict[str, Any], *keys: str) -> Any:
    for key in keys:
        value = item.get(key)
        if value is not None and value != "":
            return value
    return None


def _format_latency(value: Any) -> str:
    try:
        number = float(value)
    except (TypeError, ValueError):
        return "—"
    if number < 1000:
        return f"{number:.0f} ms"
    return f"{number / 1000:.2f} s"


def _metric(snapshot: Any, *keys: str) -> str:
    value = _first_value(snapshot.monitor, *keys)
    if value is None:
        history = _ordered_history(snapshot)
        if history:
            value = _first_value(history[-1], *keys)
    return _format_latency(value)


def _rate(snapshot: Any) -> str:
    if not snapshot.monitor.get("show_group_rate", True):
        return "—"
    try:
        return f"{float((snapshot.group or {}).get('rate_multiplier')):.2f}x"
    except (TypeError, ValueError):
        return "—"


def _availability(item: dict[str, Any]) -> str:
    value = item.get("availability_24h")
    try:
        return f"{float(value):.2f}%" if value is not None else "—"
    except (TypeError, ValueError):
        return "—"


def _display(value: Any) -> str:
    return " ".join(str(value).split()) if value is not None and str(value).strip() else "—"


def _channel_block(snapshot: Any) -> str:
    item = snapshot.monitor
    name = _display(item.get("name") or item.get("channel_name") or item.get("id"))
    status = _status(item)
    return "\n".join((
        f"{_history_symbols(snapshot)}  {status}",
        f"{name} [{_rate(snapshot)}] | {_metric(snapshot, 'primary_ttft_ms', 'ttft_ms')} | {_availability(item)}",
    ))


def format_status_pages(snapshots: Iterable[Any], now: datetime | None = None, max_chars: int = 1200) -> list[str]:
    rows = list(snapshots)
    timestamp = (now or datetime.now()).strftime("%Y-%m-%d %H:%M:%S")
    footer = f"更新时间：{timestamp}"
    if not rows:
        return [f"暂无已启用的渠道监控数据。\n{footer}"]

    pages: list[str] = []
    current = ""
    for platform, items in _platform_groups(rows):
        section = f"[{PLATFORM_LABELS.get(platform, platform.upper())} · {len(items)} 个渠道]"
        section_started = False
        for snapshot in items:
            block = _channel_block(snapshot)
            separator = "\n\n" if current else ""
            addition = separator + (section + "\n" if not section_started else "") + block
            candidate = current + addition
            if current and len(candidate + "\n\n" + footer) > max_chars:
                pages.append(current + "\n\n" + footer)
                current = section + "\n" + block
            else:
                current = candidate
            section_started = True
    pages.append(current + "\n\n" + footer)
    return pages


def format_status(snapshots: Iterable[Any], now: datetime | None = None) -> str:
    return format_status_pages(snapshots, now=now, max_chars=10**9)[0]
