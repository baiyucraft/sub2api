from __future__ import annotations

import base64
import hashlib
import json
from typing import Any

from .migration_planner import plan_migrations


def snapshot_script() -> str:
    return r'''set -Eeuo pipefail
slot=/opt/sub2api/active-app
test -f "$slot" && test ! -L "$slot"
active_container=$(sed -n 's/^container=//p' "$slot")
test "$active_container" && [[ "$active_container" =~ ^[A-Za-z0-9_.-]{1,100}$ ]]
image=$(docker inspect -f '{{.Image}}' "$active_container")
rows=$(docker exec sub2api-postgres psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api -c "SELECT COALESCE(json_agg(json_build_object('filename',filename,'checksum',checksum) ORDER BY filename),'[]'::json) FROM schema_migrations" | tr -d '\r\n')
test "$image" = sha256:* && printf '%s' "$rows" | jq -e 'type == "array"' >/dev/null
payload=$(jq -cn --arg image "$image" --argjson rows "$rows" '{current_image_id:$image, schema_migrations:$rows}')
encoded=$(printf '%s' "$payload" | base64 -w0)
printf 'snapshot_b64=%s\n' "$encoded"
'''


def decode_snapshot(value: str) -> dict[str, Any]:
    try:
        data = json.loads(base64.b64decode(value).decode("utf-8"))
    except Exception as exc:
        raise RuntimeError("production snapshot is invalid") from exc
    if not isinstance(data, dict) or not isinstance(data.get("schema_migrations"), list):
        raise RuntimeError("production snapshot shape is invalid")
    return data


def enrich_snapshot(snapshot: dict[str, Any], catalog: list[dict[str, str]]) -> dict[str, Any]:
    plan = plan_migrations(catalog, snapshot["schema_migrations"])
    value = dict(snapshot)
    value["plan"] = plan
    canonical = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    value["snapshot_sha256"] = hashlib.sha256(canonical).hexdigest()
    return value


def snapshot_sha256(snapshot: dict[str, Any]) -> str:
    """Digest the immutable production image + schema_migrations snapshot."""
    payload = {
        "current_image_id": snapshot.get("current_image_id"),
        "schema_migrations": snapshot.get("schema_migrations", []),
    }
    canonical = json.dumps(payload, ensure_ascii=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(canonical).hexdigest()
