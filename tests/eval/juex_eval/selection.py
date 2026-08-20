"""Provider-config-backed live evaluation candidate selection."""

from __future__ import annotations

import copy
import hashlib
import json
import pathlib
import re
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
    endpoint_identity: str = "default"
    runtime_profile_identity: str = "default"
    context_window: int = DEFAULT_CONTEXT_WINDOW
    context_window_declared: bool = False

    def redacted_projection(self) -> dict[str, Any]:
        return {
            "context_window": self.context_window,
            "context_window_declared": self.context_window_declared,
            "endpoint_identity": self.endpoint_identity,
            "provider_model": self.ref,
            "protocol": self.protocol,
            "reasoning_effort_capability": self.reasoning_effort_capability,
            "runtime_profile_identity": self.runtime_profile_identity,
            "thinking_effort": self.thinking_effort,
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


def enumerate_candidates(
    cfg: dict[str, Any],
    *,
    provider_api_base_override: str | None = None,
) -> list[Candidate]:
    by_ref: dict[str, Candidate] = {}
    if provider_api_base_override is None:
        environment = _environment_variables(cfg)
        api_base_override = str(environment.get("PROVIDER_API_BASE") or "")
    else:
        api_base_override = provider_api_base_override
    for provider in merged_providers(cfg):
        provider_id = str(provider.get("id") or "").strip()
        if not provider_id:
            continue
        protocol = str(provider.get("protocol") or "").strip() or "preset"
        provider_capabilities = _capabilities(provider)
        endpoint_identity = _opaque_endpoint_identity(api_base_override or str(provider.get("base_url") or ""))
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
            by_ref[ref] = Candidate(
                provider_id=provider_id,
                model_id=model_id,
                endpoint_identity=endpoint_identity,
                runtime_profile_identity=_opaque_runtime_profile_identity(provider, model),
                protocol=protocol,
                reasoning_effort_capability=_jsonish(reasoning_effort) if reasoning_effort is not None else "default",
                tools_capability=_jsonish(tools) if tools is not None else "default",
                thinking_effort=thinking_effort,
                ref=ref,
                context_window=context_window,
                context_window_declared=context_window_declared,
            )
    return [by_ref[ref] for ref in sorted(by_ref)]


def merged_providers(cfg: dict[str, Any]) -> list[dict[str, Any]]:
    """Return the runtime-equivalent merged provider view, without resolving secrets."""
    providers = cfg.get("providers")
    if providers is None:
        return []
    if not isinstance(providers, list):
        raise ValueError("provider config 'providers' must be a YAML sequence")
    merged: dict[str, dict[str, Any]] = {}
    for provider_index, raw_provider in enumerate(providers):
        if not isinstance(raw_provider, dict):
            raise ValueError(f"provider config providers[{provider_index}] must be a YAML mapping")
        provider_label = f"providers[{provider_index}]"
        _require_string_fields(raw_provider, ("id", "protocol", "base_url", "api_key"), provider_label)
        provider_id = str(raw_provider.get("id") or "").strip()
        if not provider_id:
            raise ValueError(f"provider config providers[{provider_index}] requires an id")
        if ":" in provider_id:
            raise ValueError(f"provider config providers[{provider_index}] id must not contain ':'")
        _require_mapping_fields(raw_provider, ("headers", "query", "capabilities", "compat"), provider_label)
        _require_capability_types(raw_provider, provider_label)
        _require_compat_types(raw_provider, provider_label)
        models = raw_provider.get("models")
        if models is not None and not isinstance(models, list):
            raise ValueError(f"provider config providers[{provider_index}].models must be a YAML sequence")
        for model_index, raw_model in enumerate(models or []):
            if not isinstance(raw_model, dict):
                raise ValueError(
                    f"provider config providers[{provider_index}].models[{model_index}] must be a YAML mapping"
                )
            model_label = f"providers[{provider_index}].models[{model_index}]"
            _require_string_fields(raw_model, ("id", "thinking_effort"), model_label)
            _require_integer_fields(raw_model, ("context_window",), model_label)
            if not str(raw_model.get("id") or "").strip():
                raise ValueError(f"provider config providers[{provider_index}].models[{model_index}] requires an id")
            _require_mapping_fields(
                raw_model,
                ("headers", "query", "capabilities", "compat"),
                model_label,
            )
            _require_capability_types(raw_model, model_label)
            _require_compat_types(raw_model, model_label)
        provider = copy.deepcopy(raw_provider)
        provider["id"] = provider_id
        merged[provider_id] = _merge_provider(merged.get(provider_id, {}), provider)
    return list(merged.values())


def eligible_candidates(
    cfg: dict[str, Any],
    kind: str,
    *,
    required_context_window: int = 0,
    provider_api_base_override: str | None = None,
) -> list[Candidate]:
    candidates = enumerate_candidates(cfg, provider_api_base_override=provider_api_base_override)
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
    provider_api_base_override: str | None = None,
    command_prefix: Iterable[str],
) -> tuple[list[Candidate], SelectionEvidence]:
    seed = seed.strip()
    if not seed:
        raise ValueError("selection seed cannot be empty")
    resolved_config = resolved_path(config_path)
    all_candidates = enumerate_candidates(cfg, provider_api_base_override=provider_api_base_override)
    eligible = eligible_candidates(
        cfg,
        kind,
        required_context_window=required_context_window,
        provider_api_base_override=provider_api_base_override,
    )
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
        redacted_config_hash=redacted_config_hash(all_candidates, cfg),
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


def redacted_config_hash(candidates: Iterable[Candidate], cfg: dict[str, Any] | None = None) -> str:
    projection = {
        "candidates": [candidate.redacted_projection() for candidate in sorted(candidates, key=lambda item: item.ref)],
        "environment_overrides": _environment_override_projection(cfg or {}),
    }
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


def _environment_override_projection(cfg: dict[str, Any]) -> dict[str, Any]:
    variables = _environment_variables(cfg)
    projection: dict[str, Any] = {}
    raw_context_window = variables.get("PROVIDER_CONTEXT_WINDOW")
    context_text = str(raw_context_window) if raw_context_window is not None else ""
    context_window = int(context_text) if re.fullmatch(r"[+-]?[0-9]+", context_text) else 0
    if context_window > 2**63 - 1:
        context_window = 0
    if context_window > 0:
        projection["provider_context_window"] = context_window
    raw_thinking_effort = str(variables.get("PROVIDER_THINKING_EFFORT") or "").strip()
    if raw_thinking_effort in {"low", "medium", "high", "xhigh", "max"}:
        projection["provider_thinking_effort"] = raw_thinking_effort
    elif raw_thinking_effort:
        projection["provider_thinking_effort"] = "invalid"
    return projection


def _environment_variables(cfg: dict[str, Any]) -> dict[str, Any]:
    environment = cfg.get("environment")
    variables = environment.get("variables") if isinstance(environment, dict) else None
    return variables if isinstance(variables, dict) else {}


def _opaque_endpoint_identity(value: str) -> str:
    if not value:
        return "default"
    return "sha256:" + hashlib.sha256(value.encode("utf-8")).hexdigest()


def _opaque_runtime_profile_identity(provider: dict[str, Any], model: Any) -> str:
    model = model if isinstance(model, dict) else {}
    projection = {
        "capabilities": _merge_mapping(_capabilities(provider), _capabilities(model)),
        "compat": _merge_compat(provider.get("compat"), model.get("compat")),
        "headers": _merge_mapping(provider.get("headers"), model.get("headers")),
        "query": _merge_mapping(provider.get("query"), model.get("query")),
    }
    encoded = json.dumps(projection, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def _merge_provider(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    merged = copy.deepcopy(base)
    known = {"id", "protocol", "base_url", "api_key", "headers", "query", "capabilities", "compat", "models"}
    for name, value in override.items():
        if name not in known:
            merged[name] = copy.deepcopy(value)
    merged["id"] = override["id"]
    protocol = override.get("protocol")
    if _nonempty(protocol):
        merged["protocol"] = protocol.strip()
    for name in ("base_url", "api_key"):
        value = override.get(name)
        if isinstance(value, str) and value != "":
            merged[name] = value
    for name in ("headers", "query", "capabilities"):
        merged[name] = _merge_mapping(merged.get(name), override.get(name))
    merged["compat"] = _merge_compat(merged.get("compat"), override.get("compat"))
    merged["models"] = _merge_models(merged.get("models"), override.get("models"))
    return merged


def _merge_models(base: Any, overrides: Any) -> list[dict[str, Any]]:
    merged: dict[str, dict[str, Any]] = {}
    for raw_model in [*(base if isinstance(base, list) else []), *(overrides if isinstance(overrides, list) else [])]:
        model = _model_mapping(raw_model)
        model_id = _model_id(model)
        if not model_id:
            continue
        model["id"] = model_id
        merged[model_id] = _merge_model(merged.get(model_id, {}), model)
    return list(merged.values())


def _merge_model(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    merged = copy.deepcopy(base)
    known = {"id", "thinking_effort", "context_window", "headers", "query", "capabilities", "compat"}
    for name, value in override.items():
        if name not in known:
            merged[name] = copy.deepcopy(value)
    merged["id"] = override["id"]
    thinking_effort = override.get("thinking_effort")
    if _nonempty(thinking_effort):
        merged["thinking_effort"] = thinking_effort.strip()
    context_window = override.get("context_window")
    if isinstance(context_window, int) and not isinstance(context_window, bool) and context_window > 0:
        merged["context_window"] = context_window
    for name in ("headers", "query", "capabilities"):
        merged[name] = _merge_mapping(merged.get(name), override.get(name))
    merged["compat"] = _merge_compat(merged.get("compat"), override.get("compat"))
    return merged


def _merge_mapping(base: Any, override: Any) -> dict[str, Any]:
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    if isinstance(override, dict):
        merged.update(copy.deepcopy(override))
    return merged


def _merge_compat(base: Any, override: Any) -> dict[str, Any]:
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    if not isinstance(override, dict):
        return merged
    known = {"reasoning_replay_fields", "codex_transport"}
    for name, value in override.items():
        if name not in known:
            merged[name] = copy.deepcopy(value)
    fields = override.get("reasoning_replay_fields")
    if isinstance(fields, list) and fields:
        merged["reasoning_replay_fields"] = copy.deepcopy(fields)
    if _nonempty(override.get("codex_transport")):
        merged["codex_transport"] = override["codex_transport"].strip()
    return merged


def _model_mapping(model: Any) -> dict[str, Any]:
    if isinstance(model, dict):
        return copy.deepcopy(model)
    return {"id": str(model or "").strip()}


def _nonempty(value: Any) -> bool:
    return isinstance(value, str) and bool(value.strip())


def _require_mapping_fields(value: dict[str, Any], fields: Iterable[str], label: str) -> None:
    for name in fields:
        if name in value and value[name] is not None and not isinstance(value[name], dict):
            raise ValueError(f"provider config {label}.{name} must be a YAML mapping")


def _require_string_fields(value: dict[str, Any], fields: Iterable[str], label: str) -> None:
    for name in fields:
        if name in value and value[name] is not None and not isinstance(value[name], str):
            raise ValueError(f"provider config {label}.{name} must be a YAML string")


def _require_integer_fields(value: dict[str, Any], fields: Iterable[str], label: str) -> None:
    for name in fields:
        if name in value and value[name] is not None and (
            not isinstance(value[name], int) or isinstance(value[name], bool)
        ):
            raise ValueError(f"provider config {label}.{name} must be a YAML integer")


def _require_capability_types(value: dict[str, Any], label: str) -> None:
    capabilities = value.get("capabilities")
    if not isinstance(capabilities, dict):
        return
    known = {"tools", "vision", "streaming", "reasoning_effort", "reasoning_replay", "max_output_tokens"}
    for name in known:
        if name in capabilities and capabilities[name] is not None and not isinstance(capabilities[name], bool):
            raise ValueError(f"provider config {label}.capabilities.{name} must be a YAML boolean")


def _require_compat_types(value: dict[str, Any], label: str) -> None:
    compat = value.get("compat")
    if not isinstance(compat, dict):
        return
    _require_string_fields(compat, ("codex_transport",), f"{label}.compat")
    fields = compat.get("reasoning_replay_fields")
    if fields is not None and (
        not isinstance(fields, list) or any(not isinstance(field, str) for field in fields)
    ):
        raise ValueError(f"provider config {label}.compat.reasoning_replay_fields must be a YAML string sequence")


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
