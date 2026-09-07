from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Any
from urllib.parse import urlsplit

import aiohttp


class Sub2APIError(RuntimeError):
    def __init__(self, message: str, status: int | None = None):
        super().__init__(message)
        self.status = status


@dataclass(frozen=True)
class ChannelSnapshot:
    monitor: dict[str, Any]
    history: tuple[dict[str, Any], ...] = ()
    group: dict[str, Any] | None = None


def unwrap_response(payload: Any) -> Any:
    if not isinstance(payload, dict) or "code" not in payload:
        return payload
    if payload.get("code") != 0:
        try:
            status = int(payload.get("code"))
        except (TypeError, ValueError):
            status = None
        raise Sub2APIError(str(payload.get("message") or "Sub2API 接口返回错误"), status)
    return payload.get("data")


def normalize_base_url(value: str) -> str:
    raw = str(value or "").strip().rstrip("/")
    if not raw:
        raise Sub2APIError("未配置 Sub2API base_url")
    parsed = urlsplit(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise Sub2APIError("base_url 必须是 http 或 https 地址")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise Sub2APIError("base_url 不允许包含账号、密码、查询参数或片段")
    return raw


class Sub2APIClient:
    def __init__(self, base_url: str, admin_key: str, timeout_seconds: float = 15):
        self.base_url = normalize_base_url(base_url)
        self.admin_key = str(admin_key or "").strip()
        self.timeout = aiohttp.ClientTimeout(total=timeout_seconds)
        self.history_semaphore = asyncio.Semaphore(8)

    def _url(self, path: str) -> str:
        return f"{self.base_url}/api/v1/{path.lstrip('/')}"

    async def _json(self, session: aiohttp.ClientSession, path: str, **params: Any) -> Any:
        headers = {"x-api-key": self.admin_key}
        try:
            async with session.get(self._url(path), params=params, headers=headers) as response:
                if response.status == 401 or response.status == 403:
                    raise Sub2APIError("Sub2API 管理 API Key 无效或无权限", response.status)
                if response.status >= 400:
                    raise Sub2APIError(f"Sub2API 接口返回 HTTP {response.status}", response.status)
                try:
                    payload = await response.json(content_type=None)
                except (ValueError, aiohttp.ContentTypeError) as exc:
                    raise Sub2APIError("Sub2API 返回了非 JSON 数据") from exc
        except asyncio.TimeoutError as exc:
            raise Sub2APIError("Sub2API 请求超时") from exc
        except aiohttp.ClientError as exc:
            raise Sub2APIError(f"无法连接 Sub2API: {exc}") from exc
        return unwrap_response(payload)

    async def _fetch_history(self, session: aiohttp.ClientSession, monitor_id: Any, model: Any = None) -> tuple[dict[str, Any], ...]:
        params: dict[str, Any] = {"limit": 60}
        if model:
            params["model"] = model
        async with self.history_semaphore:
            payload = await self._json(session, f"admin/channel-monitors/{monitor_id}/history", **params)
        if isinstance(payload, dict):
            items = payload.get("items") or payload.get("data") or payload.get("history") or []
        else:
            items = payload or []
        rows = [item for item in items if isinstance(item, dict)]
        if all(item.get("checked_at") for item in rows):
            rows.sort(key=lambda item: str(item.get("checked_at")))
        return tuple(rows)

    async def _fetch_group(self, session: aiohttp.ClientSession, group_id: Any) -> dict[str, Any] | None:
        if group_id is None:
            return None
        try:
            payload = await self._json(session, f"admin/groups/{group_id}")
        except Sub2APIError:
            return None
        return payload if isinstance(payload, dict) else None

    async def fetch_snapshots(self) -> tuple[ChannelSnapshot, ...]:
        if not self.admin_key:
            raise Sub2APIError("未配置 Sub2API 管理 API Key")
        async with aiohttp.ClientSession(timeout=self.timeout) as session:
            try:
                await self._json(session, "admin/channel-monitor-v2/snapshot", range="24h")
            except Sub2APIError:
                pass

            monitors_payload = await self._json(session, "admin/channel-monitors", page=1, page_size=100, enabled="true")
            if isinstance(monitors_payload, dict):
                monitors = monitors_payload.get("items") or monitors_payload.get("data") or monitors_payload.get("monitors") or []
                total = int(monitors_payload.get("total") or len(monitors))
            else:
                monitors = monitors_payload or []
                total = len(monitors)
            monitors = [item for item in monitors if isinstance(item, dict)]
            page = 2
            while len(monitors) < total and page <= 20:
                page_payload = await self._json(session, "admin/channel-monitors", page=page, page_size=100, enabled="true")
                if isinstance(page_payload, dict):
                    page_items = page_payload.get("items") or page_payload.get("data") or page_payload.get("monitors") or []
                else:
                    page_items = page_payload or []
                page_items = [item for item in page_items if isinstance(item, dict)]
                if not page_items:
                    break
                monitors.extend(page_items)
                page += 1
            histories = await asyncio.gather(
                *[
                    self._fetch_history(
                        session,
                        item.get("id"),
                        item.get("primary_model") or item.get("model"),
                    )
                    if item.get("id") is not None and not item.get("timeline")
                    else asyncio.sleep(0, result=tuple(item.get("timeline") or ()))
                    for item in monitors
                ],
                return_exceptions=True,
            )
            group_ids = list(dict.fromkeys(item.get("group_id") for item in monitors if item.get("group_id") is not None))
            group_rows = await asyncio.gather(*(self._fetch_group(session, group_id) for group_id in group_ids))
            groups = dict(zip(group_ids, group_rows))

        snapshots: list[ChannelSnapshot] = []
        for monitor, history in zip(monitors, histories):
            if isinstance(history, Exception):
                history = ()
            snapshots.append(ChannelSnapshot(monitor, tuple(history), groups.get(monitor.get("group_id"))))
        return tuple(snapshots)
