"""Release logging primitives with redaction and retention planning.

The package deliberately lives below ``scripts/logging`` while using the
import name ``release_logging``.  Importing a top-level package named
``logging`` would shadow Python's standard library module.
"""

from .events import EventContext, JSONLEventLogger, validate_event
from .query import LogQuery, LogQueryResult, LogReadIssue, query_events
from .redact import REDACTED, redact_text, redact_value
from .retention import (
    ReleaseLogRecord,
    RetentionPlan,
    build_retention_plan,
    verify_retention_plan,
)

__all__ = [
    "EventContext",
    "JSONLEventLogger",
    "LogQuery",
    "LogQueryResult",
    "LogReadIssue",
    "REDACTED",
    "ReleaseLogRecord",
    "RetentionPlan",
    "build_retention_plan",
    "query_events",
    "redact_text",
    "redact_value",
    "validate_event",
    "verify_retention_plan",
]
