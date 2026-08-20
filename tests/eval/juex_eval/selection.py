"""Provider-config-backed live evaluation candidate selection."""

from __future__ import annotations

import hashlib
import json
import pathlib
import secrets
import shlex
from dataclasses import dataclass
from typing import Any, Iterable


DEFAULT_CONTEXT_WINDOW = 256000
SELECTION_SOURCE = "provider_config"
PROVIDER_UNAVAILABLE = "provider_unavailable"


@dataclass(frozen=True)
class Candidate:
    provider_id: str
    model_id: str
    protocol: str
    reasoning_effort_capability: str
    tools_capability: str
    thinking_effort: str
    ref: str
    context_window: int = DEFAULT_CONTEXT_WINDOW
    context_window_declared: bool = False

    def redacted_projection(self) -> dict[str, Any]:
        return {
            "context_window": self.context_window,
            "context_window_declared": self.context_window_declared,
            "provider_model": self.ref,
            "tools_capability": self.tools_capability,
        }


@dataclass(frozen=True)
class SelectionEvidence:
    selected_refs: tuple[str, ...]
    seed: str
    eligible_refs: tuple[str, ...]
    resolved_config_path: str
    redacted_config_hash: str
    reproduction_command: str
    mode: str

    def as_dict(self) -> dict[str, Any]:
        return {
            "selection_source": SELECTION_SOURCE,
            "selected_provider_model": self.selected_refs[0] if len(self.selected_refs) == 1 else None,
            "selected_provider_models": list(self.selected_refs),
            "selection_seed": self.seed,
            "eligible_candidate_count": len(self.eligible_refs),
            "eligible_candidate_refs": list(self.eligible_refs),
            "resolved_config_path": self.resolved_config_path,
            "redacted_config_hash": self.redacted_config_hash,
            "reproduction_command": self.reproduction_command,
            "selection_mode": self.mode,
        }


class ProviderUnavailable(ValueError):
    def __init__(self, message: str, evidence: SelectionEvidence):
        super().__init__(message)
        self.evidence = evidence
        self.failure_category = PROVIDER_UNAVAILABLE


def generated_seed() -> str:
    return secrets.token_hex(16)


def resolved_path(value: str | pathlib.Path) -> pathlib.Path:
    return pathlib.Path(value).expanduser().resolve()


def enumerate_candidates(cfg: dict[str, Any]) -> list[Candidate]:
    providers = cfg.get("providers") or []
    if isinstance(providers, dict):
        providers = list(providers.values())
    by_ref: dict[str, Candidate] = {}
    for provider in providers:
        if not isinstance(provider, dict):
            continue
        provider_id = str(provider.get("id") or "").strip()
        if not provider_id:
            continue
        protocol = str(provider.get("protocol") or "").strip() or "preset"
        provider_capabilities = _capabilities(provider)
        models = provider.get("models") or []
        for model in models:
            model_id = _model_id(model)
            if not model_id:
                continue
            model_capabilities = _capabilities(model) if isinstance(model, dict) else {}
            tools = _effective_capability(provider_capabilities, model_capabilities, "tools")
            reasoning_effort = _effective_capability(
                provider_capabilities,
                model_capabilities,
                "reasoning_effort",
            )
            thinking_effort = "unset"
            context_window = DEFAULT_CONTEXT_WINDOW
            context_window_declared = False
            if isinstance(model, dict):
                if "thinking_effort" in model:
                    thinking_effort = _jsonish(model["thinking_effort"])
                raw_context_window = model.get("context_window")
                if isinstance(raw_context_window, int) and not isinstance(raw_context_window, bool) and raw_context_window > 0:
                    context_window = raw_context_window
                    context_window_declared = True
            ref = f"{provider_id}:{model_id}"
            by_ref.setdefault(
                ref,
                Candidate(
                    provider_id=provider_id,
                    model_id=model_id,
                    protocol=protocol,
                    reasoning_effort_capability=_jsonish(reasoning_effort) if reasoning_effort is not None else "default",
                    tools_capability=_jsonish(tools) if tools is not None else "default",
                    thinking_effort=thinking_effort,
                    ref=ref,
                    context_window=context_window,
                    context_window_declared=context_window_declared,
                ),
            )
    return [by_ref[ref] for ref in sorted(by_ref)]


def eligible_candidates(
    cfg: dict[str, Any],
    kind: str,
    *,
    required_context_window: int = 0,
) -> list[Candidate]:
    candidates = enumerate_candidates(cfg)
    if kind == "provider-smoke":
        return [candidate for candidate in candidates if candidate.tools_capability != "false"]
    if kind == "compaction":
        return [candidate for candidate in candidates if candidate.context_window >= required_context_window]
    raise ValueError(f"unsupported live eval kind: {kind}")


def select(
    cfg: dict[str, Any],
    *,
    kind: str,
    config_path: pathlib.Path,
    seed: str,
    only: Iterable[str] = (),
    all_models: bool = False,
    required_context_window: int = 0,
    command_prefix: Iterable[str],
) -> tuple[list[Candidate], SelectionEvidence]:
    seed = seed.strip()
    if not seed:
        raise ValueError("selection seed cannot be empty")
    resolved_config = resolved_path(config_path)
    all_candidates = enumerate_candidates(cfg)
    eligible = eligible_candidates(cfg, kind, required_context_window=required_context_window)
    eligible_by_ref = {candidate.ref: candidate for candidate in eligible}
    requested = _normalized_refs(only)
    mode = "only" if requested else "all_models" if all_models else "seeded"

    selected: list[Candidate] = []
    message = ""
    if requested:
        missing = [ref for ref in requested if ref not in {candidate.ref for candidate in all_candidates}]
        ineligible = [ref for ref in requested if ref in {candidate.ref for candidate in all_candidates} and ref not in eligible_by_ref]
        if missing:
            message = "requested provider:model is not configured: " + ", ".join(missing)
        elif ineligible:
            message = "requested provider:model is not eligible for this eval: " + ", ".join(ineligible)
        else:
            selected = [eligible_by_ref[ref] for ref in requested]
    elif not eligible:
        message = "no eligible provider:model candidates found in provider config"
    elif all_models:
        selected = eligible
    else:
        selected = [eligible[_seeded_index(seed, [candidate.ref for candidate in eligible])]]

    reproduction = _reproduction_command(
        command_prefix,
        resolved_config,
        seed,
        mode,
        [candidate.ref for candidate in selected] if selected else requested,
    )
    evidence = SelectionEvidence(
        selected_refs=tuple(candidate.ref for candidate in selected),
        seed=seed,
        eligible_refs=tuple(candidate.ref for candidate in eligible),
        resolved_config_path=str(resolved_config),
        redacted_config_hash=redacted_config_hash(all_candidates),
        reproduction_command=reproduction,
        mode=mode,
    )
    if message:
        raise ProviderUnavailable(message, evidence)
    return selected, evidence


def unavailable_evidence(
    *,
    config_path: pathlib.Path,
    seed: str,
    command_prefix: Iterable[str],
    only: Iterable[str] = (),
    all_models: bool = False,
) -> SelectionEvidence:
    resolved_config = resolved_path(config_path)
    requested = _normalized_refs(only)
    mode = "only" if requested else "all_models" if all_models else "seeded"
    return SelectionEvidence(
        selected_refs=(),
        seed=seed,
        eligible_refs=(),
        resolved_config_path=str(resolved_config),
        redacted_config_hash=redacted_config_hash([]),
        reproduction_command=_reproduction_command(command_prefix, resolved_config, seed, mode, requested),
        mode=mode,
    )


def redacted_config_hash(candidates: Iterable[Candidate]) -> str:
    projection = [candidate.redacted_projection() for candidate in sorted(candidates, key=lambda item: item.ref)]
    encoded = json.dumps(projection, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def _seeded_index(seed: str, refs: list[str]) -> int:
    material = json.dumps({"refs": refs, "seed": seed}, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return int.from_bytes(hashlib.sha256(material.encode("utf-8")).digest()[:8], "big") % len(refs)


def _normalized_refs(values: Iterable[str]) -> list[str]:
    refs: list[str] = []
    seen: set[str] = set()
    for raw in values:
        ref = str(raw or "").strip()
        if not ref:
            continue
        if ":" not in ref or not all(part.strip() for part in ref.split(":", 1)):
            raise ValueError(f"invalid model ref format (expected provider:model): {raw!r}")
        provider_id, model_id = (part.strip() for part in ref.split(":", 1))
        canonical = f"{provider_id}:{model_id}"
        if canonical not in seen:
            seen.add(canonical)
            refs.append(canonical)
    return refs


def _reproduction_command(
    prefix: Iterable[str],
    config_path: pathlib.Path,
    seed: str,
    mode: str,
    refs: list[str],
) -> str:
    command = [*prefix, "--config", str(config_path)]
    if mode == "only":
        for ref in refs:
            command.extend(["--only", ref])
    else:
        command.extend(["--selection-seed", seed])
        if mode == "all_models":
            command.append("--all-models")
    return shlex.join(str(part) for part in command)


def _capabilities(value: dict[str, Any]) -> dict[str, Any]:
    capabilities = value.get("capabilities")
    return capabilities if isinstance(capabilities, dict) else {}


def _effective_capability(provider: dict[str, Any], model: dict[str, Any], name: str) -> Any:
    if name in model:
        return model[name]
    return provider.get(name)


def _model_id(model: Any) -> str:
    if isinstance(model, dict):
        return str(model.get("id") or "").strip()
    return str(model or "").strip()


def _jsonish(value: Any) -> str:
    try:
        return json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    except TypeError:
        return str(value)
