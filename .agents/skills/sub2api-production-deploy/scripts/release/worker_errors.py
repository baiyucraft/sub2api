"""Bounded, content-free diagnostics which never replace a worker failure."""

from __future__ import annotations


_ATOMIC_STAGES = {
    "snapshot", "create_absent", "replace_existing", "verify_target", "verify_backup", "cleanup_backup",
}

DOCTOR_FAILURE_CODES = frozenset({
    "doctor_local_failed", "doctor_vm_failed", "doctor_racknerd_failed",
    "doctor_dmit_failed", "doctor_backup_failed", "doctor_unknown_failed",
    "doctor_backup_health_transport_failed", "doctor_backup_health_tls_failed",
    "doctor_backup_health_http_failed", "doctor_backup_health_probe_failed",
})


class DoctorFailure(RuntimeError):
    """A fixed doctor failure category, without remote diagnostic content."""

    def __init__(self, code: str, message: str | None = None):
        if code not in DOCTOR_FAILURE_CODES:
            raise ValueError("unknown doctor failure code")
        self.doctor_failure_code = code
        super().__init__(message or code)


def failure_details(error: BaseException) -> dict:
    details = {"error_type": type(error).__name__}
    code = getattr(error, "doctor_failure_code", None)
    if isinstance(code, str) and code in DOCTOR_FAILURE_CODES:
        details["doctor_failure_code"] = code
    stage = getattr(error, "atomic_write_stage", None)
    if isinstance(stage, str) and stage in _ATOMIC_STAGES:
        details["atomic_write_stage"] = stage
        for name in ("atomic_write_committed", "atomic_write_state_uncertain"):
            value = getattr(error, name, None)
            if value is None or type(value) is bool:
                details[name] = value
        objects = getattr(error, "atomic_write_objects", None)
        if isinstance(objects, dict):
            details["atomic_write_objects"] = {
                name: objects.get(name) if type(objects.get(name)) is bool else None
                for name in ("target", "temporary", "backup")
            }
    return details


def preserve_secondary(primary: BaseException, secondary: BaseException, operation: str) -> dict:
    """Keep only fixed fields; raw exception messages may contain credentials."""
    details = {"operation": operation, **failure_details(secondary)}
    try:
        prior = getattr(primary, "release_secondary_errors", ())
        primary.release_secondary_errors = (*prior[-7:], details)
        if hasattr(primary, "add_note"):
            primary.add_note(f"release secondary failure: {operation} ({type(secondary).__name__})")
    except BaseException:
        pass
    return details
