from __future__ import annotations

import asyncio
import time
from typing import Any

from astrbot.api import AstrBotConfig, logger
from astrbot.api.event import AstrMessageEvent, MessageChain, filter
from astrbot.api.message_components import At, Image, Plain
from astrbot.api.star import Context, Star

try:
    from .client import Sub2APIClient, Sub2APIError
    from .renderer import format_status, render_status_svg, split_pages, svg_html_document
except ImportError:
    from client import Sub2APIClient, Sub2APIError
    from renderer import format_status, render_status_svg, split_pages, svg_html_document


def _as_bool(value: Any, default: bool = False) -> bool:
    if isinstance(value, bool):
        return value
    if value is None:
        return default
    return str(value).strip().lower() in {"1", "true", "yes", "on", "是", "开启"}


def parse_ids(value: Any) -> list[str]:
    text = str(value or "")
    result: list[str] = []
    for item in text.replace("，", ",").replace("\n", ",").replace("\r", ",").split(","):
        result.extend(part for part in item.split() if part)
    return list(dict.fromkeys(result))


def _platform_id(event: AstrMessageEvent) -> str:
    try:
        return str(event.get_platform_id() or "").lower()
    except Exception:
        return ""


class Main(Star):
    def __init__(self, context: Context, config: AstrBotConfig):
        super().__init__(context)
        self.config = config
        self._client_instance: Sub2APIClient | None = None
        self._cache: tuple[float, tuple[Any, ...]] | None = None
        self._fetch_lock = asyncio.Lock()
        self._status_task: asyncio.Task[None] | None = None
        self._welcome_seen: dict[tuple[str, str], float] = {}

    def _client(self) -> Sub2APIClient:
        if self._client_instance is None:
            self._client_instance = Sub2APIClient(
                str(self.config.get("base_url", "")),
                str(self.config.get("SUB2API_ADMIN_KEY", "")),
            )
        return self._client_instance

    def _status_interval(self) -> int:
        try:
            return max(300, int(self.config.get("status_interval_seconds", 3600) or 3600))
        except (TypeError, ValueError):
            return 3600

    async def _snapshots(self) -> tuple[Any, ...]:
        now = time.monotonic()
        if self._cache and now - self._cache[0] < 30:
            return self._cache[1]
        async with self._fetch_lock:
            now = time.monotonic()
            if self._cache and now - self._cache[0] < 30:
                return self._cache[1]
            snapshots = await self._client().fetch_snapshots()
            self._cache = (now, snapshots)
            return snapshots

    @filter.command("status")
    async def status(self, event: AstrMessageEvent):
        try:
            snapshots = await self._snapshots()
        except Sub2APIError as exc:
            logger.warning("Sub2API 状态查询失败: %s", exc)
            snapshots = None
            pages = [f"Sub2API 渠道状态查询失败：{exc}"]
        except Exception:
            logger.exception("Sub2API 状态查询出现未处理异常")
            snapshots = None
            pages = ["Sub2API 渠道状态查询失败，请稍后重试。"]
        if snapshots is not None:
            pages = []
            html_render = getattr(self, "html_render", None)
            image_result = getattr(event, "image_result", None)
            if snapshots and callable(html_render) and callable(image_result):
                try:
                    svg = render_status_svg(snapshots)
                    image_path = await html_render(
                        svg_html_document(svg),
                        {},
                        return_url=False,
                        options={"type": "png", "full_page": True, "animations": "disabled"},
                    )
                    await event.send(image_result(image_path))
                    return
                except Exception:
                    logger.warning("Sub2API 状态图片渲染失败，回退文本")
            pages = split_pages(format_status(snapshots))
        for page in pages:
            await event.send(MessageChain().message(page))

    @staticmethod
    def _origin(platform: str, target: str) -> str:
        if ":" in target:
            return target
        return f"{platform}:GroupMessage:{target}"

    async def _send_status_targets(self, snapshots: tuple[Any, ...]) -> None:
        image_path: str | None = None
        html_render = getattr(self, "html_render", None)
        if snapshots and callable(html_render):
            try:
                image_path = await html_render(
                    svg_html_document(render_status_svg(snapshots)),
                    {},
                    return_url=False,
                    options={"type": "png", "full_page": True, "animations": "disabled"},
                )
            except Exception:
                logger.warning("定时状态图片渲染失败，回退文本")
        pages = split_pages(format_status(snapshots)) if image_path is None else []
        targets = [
            ("qq_official", parse_ids(self.config.get("qq_status_group_ids"))),
            ("telegram", parse_ids(self.config.get("telegram_status_chat_ids"))),
        ]
        for platform, items in targets:
            for target in items:
                origin = self._origin(platform, target)
                try:
                    if image_path is not None:
                        await self.context.send_message(origin, MessageChain([Image.fromFileSystem(image_path)]))
                    else:
                        for page in pages:
                            await self.context.send_message(origin, MessageChain().message(page))
                except Exception as exc:
                    logger.warning("定时状态发送失败（平台=%s，目标=%s）：%s", platform, target, exc)

    async def _status_loop(self) -> None:
        interval = self._status_interval()
        while True:
            await asyncio.sleep(interval)
            try:
                await self._send_status_targets(await self._snapshots())
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception("Sub2API 定时状态任务失败")

    async def initialize(self):
        if self._status_task is None or self._status_task.done():
            self._status_task = asyncio.create_task(self._status_loop(), name="sub2api-status-loop")

    async def terminate(self):
        if self._status_task and not self._status_task.done():
            self._status_task.cancel()
            await asyncio.gather(self._status_task, return_exceptions=True)
        self._status_task = None

    @staticmethod
    def _raw_event(event: AstrMessageEvent) -> dict[str, Any]:
        message_obj = getattr(event, "message_obj", None)
        for candidate in (getattr(message_obj, "raw_data", None), getattr(message_obj, "raw_message", None)):
            if isinstance(candidate, dict):
                return candidate
        return {}

    @filter.event_message_type(filter.EventMessageType.ALL)
    async def welcome_new_member(self, event: AstrMessageEvent):
        if not _as_bool(self.config.get("welcome_enabled"), False):
            return
        if _platform_id(event) != "qq_official":
            return
        raw = self._raw_event(event)
        event_type = str(raw.get("event_type") or raw.get("type") or raw.get("event") or "").lower()
        supported_names = {"group_member_add", "group_member_added", "member_join", "group_increase"}
        if event_type not in supported_names:
            return
        group_id = str(raw.get("group_openid") or raw.get("group_id") or "").strip()
        user_id = str(raw.get("member_openid") or raw.get("user_id") or raw.get("openid") or "").strip()
        if not group_id or not user_id:
            return
        configured_groups = parse_ids(self.config.get("welcome_group_ids"))
        if configured_groups and group_id not in configured_groups:
            return
        key = (group_id, user_id)
        now = time.monotonic()
        self._welcome_seen = {item: seen for item, seen in self._welcome_seen.items() if now - seen < 600}
        if key in self._welcome_seen:
            return
        self._welcome_seen[key] = now
        mention = f"<@{user_id}>"
        message = str(self.config.get("welcome_message") or "欢迎加入本群！\n发送 /status 查询渠道状态。")
        message = message.replace("{user_id}", user_id).replace("{group_id}", group_id).replace("{mention}", mention)
        try:
            chain = MessageChain([At(name="", qq=user_id), Plain(message)]) if _as_bool(self.config.get("welcome_at_member"), True) else MessageChain().message(message)
            await event.send(chain)
        except Exception:
            logger.exception("QQ 官方机器人新人欢迎发送失败")
