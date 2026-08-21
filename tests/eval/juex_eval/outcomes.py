"""Machine-readable validation outcomes and bounded retry classification."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass


PASSED = "passed"
FLAKY_PASS = "flaky_pass"
PRODUCT_FAILURE = "product_failure"
ENVIRONMENT_FAILURE = "environment_failure"
PROVIDER_UNAVAILABLE = "provider_unavailable"
TRANSIENT_FAILURE = "transient_failure"
OUTCOME_VALUES = (
    PRODUCT_FAILURE,
    ENVIRONMENT_FAILURE,
    PROVIDER_UNAVAILABLE,
    TRANSIENT_FAILURE,
    PASSED,
    FLAKY_PASS,
)
STRUCTURED_PREFIX = "JUEX_VALIDATION_OUTCOME "

_ENVIRONMENT_RULES = (
    (
        "environment-command-missing",
        re.compile(r"(?:command not found|not recognized as an internal or external command|executable file not found)", re.I),
        "required validation command is unavailable",
    ),
    (
        "environment-permission-denied",
        re.compile(r"(?:permission denied|operation not permitted|access is denied)", re.I),
        "validation environment denied required access",
    ),
    (
        "environment-provider-credentials",
        re.compile(r"(?:HTTP(?:/\S+)?\s+401\b|\bunauthorized\b|invalid api key|authentication failed)", re.I),
        "provider credentials are missing or invalid",
    ),
    (
        "environment-invalid-config",
        re.compile(r"(?:provider config not found|invalid (?:local |provider )?config|must contain a YAML mapping)", re.I),
        "local validation configuration is missing or invalid",
    ),
)
_PROVIDER_RULES = (
    (
        "provider-selection-unavailable",
        re.compile(
            r"(?:provider_unavailable|no eligible provider|requested provider:model .* not (?:found|configured|eligible)|provider:model .* unavailable)",
            re.I,
        ),
        "requested provider/model is unavailable",
    ),
    (
        "provider-unreachable",
        re.compile(r"(?:connection refused|no route to host|network is unreachable)", re.I),
        "provider endpoint is unreachable",
    ),
)
_TRANSIENT_RULES = (
    (
        "transient-structured-retryable",
        re.compile(r'"retryable"\s*:\s*true', re.I),
        "provider reported a retryable transient failure",
    ),
    (
        "transient-http-status",
        re.compile(
            r'(?:(?:"(?:status|status_code)"\s*:\s*)|(?:HTTP(?:/\S+)?\s+)|(?:\bstatus(?:\s+code)?\s*[:=]?\s*))(?:429|502|503|504)\b',
            re.I,
        ),
        "provider returned an allowlisted transient HTTP status",
    ),
    (
        "transient-network",
        re.compile(
            r"(?:TLS handshake timeout|context deadline exceeded|connection reset(?: by peer)?|temporary failure in name resolution|server closed idle connection)",
            re.I,
        ),
        "provider request matched an allowlisted transient network failure",
    ),
)


@dataclass(frozen=True)
class ValidationOutcome:
    outcome: str
    reason: str
    matched_rule: str
    blocks_merge: bool
    recommended_action: str
    retryable: bool = False

    def as_dict(self) -> dict[str, object]:
        return {
            "outcome": self.outcome,
            "reason": self.reason,
            "matched_rule": self.matched_rule,
            "blocks_merge": self.blocks_merge,
            "recommended_action": self.recommended_action,
            "retryable": self.retryable,
        }


def success(*, attempt_count: int) -> ValidationOutcome:
    if attempt_count < 1:
        raise ValueError("attempt_count must be positive")
    if attempt_count == 1:
        return ValidationOutcome(PASSED, "validation passed on the first attempt", "first-attempt-pass", False, "continue")
    return ValidationOutcome(FLAKY_PASS, "allowlisted transient failure passed on the only retry", "transient-retry-pass", False, "continue")


def invalid_config_failure(reason: str) -> ValidationOutcome:
    return ValidationOutcome(
        ENVIRONMENT_FAILURE,
        reason or "local provider configuration is missing or invalid",
        "environment-invalid-config",
        True,
        "fix_environment",
    )


def classify_failure(
    log_text: str,
    *,
    deterministic: bool,
    exit_status: int,
    process_error: OSError | None = None,
) -> ValidationOutcome:
    if exit_status == 0 and process_error is None:
        return success(attempt_count=1)
    if process_error is not None:
        return ValidationOutcome(
            ENVIRONMENT_FAILURE,
            f"validation process could not start: {process_error.__class__.__name__}",
            "environment-process-start",
            True,
            "fix_environment",
        )
    structured = _structured_outcome(log_text)
    if (
        structured is not None
        and structured.blocks_merge
        and (not deterministic or structured.outcome not in {TRANSIENT_FAILURE, PROVIDER_UNAVAILABLE})
    ):
        return structured

    for rule, pattern, reason in _ENVIRONMENT_RULES:
        if pattern.search(log_text):
            return ValidationOutcome(ENVIRONMENT_FAILURE, reason, rule, True, "fix_environment")
    if exit_status in {126, 127}:
        return ValidationOutcome(
            ENVIRONMENT_FAILURE,
            "validation command could not be executed",
            "environment-command-status",
            True,
            "fix_environment",
        )

    if not deterministic:
        for rule, pattern, reason in _PROVIDER_RULES:
            if pattern.search(log_text):
                return ValidationOutcome(PROVIDER_UNAVAILABLE, reason, rule, True, "stop")
        for rule, pattern, reason in _TRANSIENT_RULES:
            if pattern.search(log_text):
                return ValidationOutcome(TRANSIENT_FAILURE, reason, rule, True, "stop", retryable=True)
        if exit_status == 124:
            return ValidationOutcome(
                TRANSIENT_FAILURE,
                "live provider step exceeded its bounded timeout",
                "transient-provider-timeout",
                True,
                "stop",
                retryable=True,
            )

    return ValidationOutcome(
        PRODUCT_FAILURE,
        f"validation command failed with exit status {exit_status}",
        "deterministic-step-nonzero" if deterministic else "live-contract-or-product-failure",
        True,
        "fix_code",
    )


def marker(result: ValidationOutcome) -> str:
    return STRUCTURED_PREFIX + json.dumps(result.as_dict(), ensure_ascii=False, separators=(",", ":"))


def _structured_outcome(log_text: str) -> ValidationOutcome | None:
    for line in reversed(log_text.splitlines()):
        if not line.startswith(STRUCTURED_PREFIX):
            continue
        try:
            value = json.loads(line.removeprefix(STRUCTURED_PREFIX))
        except json.JSONDecodeError:
            continue
        outcome = value.get("outcome") if isinstance(value, dict) else None
        if outcome not in OUTCOME_VALUES:
            continue
        return ValidationOutcome(
            outcome=str(outcome),
            reason=str(value.get("reason") or "structured validation outcome"),
            matched_rule=str(value.get("matched_rule") or "structured-outcome"),
            blocks_merge=outcome not in {PASSED, FLAKY_PASS},
            recommended_action=str(value.get("recommended_action") or _recommended_action(str(outcome))),
            retryable=bool(value.get("retryable")) and outcome == TRANSIENT_FAILURE,
        )
    return None


def _recommended_action(outcome: str) -> str:
    if outcome == PRODUCT_FAILURE:
        return "fix_code"
    if outcome == ENVIRONMENT_FAILURE:
        return "fix_environment"
    if outcome in {PROVIDER_UNAVAILABLE, TRANSIENT_FAILURE}:
        return "stop"
    return "continue"
