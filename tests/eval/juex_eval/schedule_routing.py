from __future__ import annotations

import json
import pathlib
import re
import shlex
from dataclasses import dataclass
from typing import Any

try:
    from . import contract_oracle
except ImportError:  # pragma: no cover - direct script fallback.
    import contract_oracle  # type: ignore[no-redef]


FORBIDDEN_TOOLS = {
    "observable_create",
}

SHELL_COMMAND_FIELDS = {
    "exec_command": ("cmd", "command"),
    "shell": ("cmd", "command"),
    "write_stdin": ("chars",),
}

SHELL_INTERPRETERS = {"bash", "dash", "ksh", "sh", "zsh"}

SHELL_OPTIONS_WITH_VALUE = {"-o", "+o", "-O", "+O", "--init-file", "--rcfile"}

SHELL_COMMAND_PREFIXES = {"!", "{"}

SHELL_CONTROL_PREFIXES = {"do", "elif", "else", "if", "then", "until", "while"}

WRAPPER_OPTIONS_WITH_VALUE = {
    "command": set(),
    "env": {"-a", "--argv0", "-C", "--chdir", "-S", "--split-string", "-u", "--unset"},
    "nohup": set(),
    "setsid": set(),
    "sudo": {
        "-C",
        "--close-from",
        "-D",
        "--chdir",
        "-g",
        "--group",
        "-h",
        "--host",
        "-p",
        "--prompt",
        "-R",
        "--chroot",
        "-r",
        "--role",
        "-t",
        "--type",
        "-T",
        "--command-timeout",
        "-u",
        "--user",
    },
    "time": {"-f", "--format", "-o", "--output"},
}


@dataclass(frozen=True)
class ScheduleRoutingExpectation:
    schedule_id: str
    every_seconds: int
    content: str
    completion_token: str


@dataclass(frozen=True)
class _ToolUse:
    name: str
    tool_use_id: str
    input: Any
    message_index: int
    block_index: int


@dataclass(frozen=True)
class _ToolResult:
    tool_use_id: str
    is_error: bool
    message_index: int
    block_index: int


def build_prompt(expectation: ScheduleRoutingExpectation) -> str:
    hours = expectation.every_seconds / 3600
    hours_text = str(int(hours)) if hours.is_integer() else f"{hours:g}"
    return "\n".join(
        [
            "Create recurring timed work using JueX native scheduling.",
            f"Use the fixed id {expectation.schedule_id} and run it every {hours_text} hours.",
            f"Each activation must emit observation content exactly: {expectation.content}",
            "Inspect all currently configured timed sources first, then create this one only if it is absent.",
            "Use native scheduling for recurrence; do not implement it with shell polling, background loops, or a managed command source.",
            f"After the timed work is created successfully, answer exactly: {expectation.completion_token}",
        ]
    )


def validate_contract(
    conversation_path: pathlib.Path,
    observables_path: pathlib.Path,
    expectation: ScheduleRoutingExpectation,
) -> contract_oracle.ContractReport:
    issues: list[str] = []
    messages = _load_jsonl(conversation_path, "conversation", issues)
    uses, results = _transcript_tools(messages)
    _validate_tool_contract(messages, uses, results, expectation, issues)
    _validate_persisted_config(observables_path, expectation, issues)
    return contract_oracle.ContractReport(passed=not issues, issues=issues)


def _load_jsonl(path: pathlib.Path, label: str, issues: list[str]) -> list[dict[str, Any]]:
    if not path.is_file():
        issues.append(f"missing {label} file: {path}")
        return []
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        issues.append(f"read {label}: {exc}")
        return []
    messages: list[dict[str, Any]] = []
    for line_number, line in enumerate(lines, start=1):
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            issues.append(f"{label} line {line_number} is invalid JSON: {exc}")
            continue
        if not isinstance(value, dict):
            issues.append(f"{label} line {line_number} must be a JSON object")
            continue
        messages.append(value)
    return messages


def _transcript_tools(messages: list[dict[str, Any]]) -> tuple[list[_ToolUse], list[_ToolResult]]:
    uses: list[_ToolUse] = []
    results: list[_ToolResult] = []
    for message_index, message in enumerate(messages):
        blocks = message.get("blocks")
        if not isinstance(blocks, list):
            continue
        for block_index, block in enumerate(blocks):
            if not isinstance(block, dict):
                continue
            if block.get("type") == "tool_use":
                name = block.get("tool_name")
                if isinstance(name, str):
                    uses.append(
                        _ToolUse(
                            name=name,
                            tool_use_id=str(block.get("tool_use_id") or ""),
                            input=block.get("input"),
                            message_index=message_index,
                            block_index=block_index,
                        )
                    )
            elif block.get("type") == "tool_result":
                results.append(
                    _ToolResult(
                        tool_use_id=str(block.get("tool_use_id") or ""),
                        is_error=block.get("is_error") is True,
                        message_index=message_index,
                        block_index=block_index,
                    )
                )
    return uses, results


def _validate_tool_contract(
    messages: list[dict[str, Any]],
    uses: list[_ToolUse],
    results: list[_ToolResult],
    expectation: ScheduleRoutingExpectation,
    issues: list[str],
) -> None:
    completion_after: tuple[int, int] | None = None
    for use in uses:
        if use.name in FORBIDDEN_TOOLS:
            issues.append(f"forbidden tool_use: {use.name}")
        if _is_shell_scheduling_command(use):
            issues.append(f"forbidden shell scheduling command: {use.name}")

    list_uses = [use for use in uses if use.name == "observable_list"]
    create_uses = [use for use in uses if use.name == "schedule_create"]
    if not list_uses:
        issues.append("expected at least one observable_list tool_use, saw 0")
    if not create_uses:
        issues.append("expected at least one schedule_create tool_use, saw 0")

    successful_creates = [
        (create_use, result)
        for create_use in create_uses
        if (result := _successful_result_after(create_use, results)) is not None
    ]
    if len(successful_creates) != 1:
        issues.append(
            f"expected exactly one successful schedule_create tool_use, saw {len(successful_creates)}"
        )

    if list_uses:
        for create_use in create_uses:
            if _has_successful_list_before(create_use, list_uses, results):
                continue
            if any(list_use.message_index == create_use.message_index for list_use in list_uses):
                issues.append(
                    "observable_list and schedule_create cannot be in the same assistant message "
                    "without an earlier successful observable_list result"
                )
            else:
                issues.append(
                    "at least one observable_list successful tool_result must occur "
                    "before every schedule_create"
                )

    if len(successful_creates) == 1:
        create_use, create_result = successful_creates[0]
        completion_after = _position(create_result)
        _validate_create_input(create_use.input, expectation, issues)

    if completion_after is None or not _has_exact_completion_after(
        messages,
        expectation.completion_token,
        completion_after,
    ):
        issues.append(f"exact completion token must appear after successful schedule_create: {expectation.completion_token}")


def _position(value: _ToolUse | _ToolResult) -> tuple[int, int]:
    return value.message_index, value.block_index


def _successful_result_after(use: _ToolUse, results: list[_ToolResult]) -> _ToolResult | None:
    for result in results:
        if (
            result.tool_use_id
            and result.tool_use_id == use.tool_use_id
            and _position(result) > _position(use)
        ):
            return None if result.is_error else result
    return None


def _has_successful_list_before(
    create_use: _ToolUse,
    list_uses: list[_ToolUse],
    results: list[_ToolResult],
) -> bool:
    return any(
        (result := _successful_result_after(list_use, results)) is not None
        and _position(result) < _position(create_use)
        for list_use in list_uses
    )


def _is_shell_scheduling_command(use: _ToolUse) -> bool:
    fields = SHELL_COMMAND_FIELDS.get(use.name)
    if fields is None or not isinstance(use.input, dict):
        return False
    command = next(
        (
            value
            for field in fields
            if isinstance(value := use.input.get(field), str)
        ),
        None,
    )
    if not isinstance(command, str):
        return False
    segments = _recursive_shell_segments(command)
    programs = [
        _program_name(segment[program_index])
        for segment in segments
        if (program_index := _command_program_index(segment)) is not None
    ]
    if any(program in {"while", "until", "for", "select"} for program in programs) and "sleep" in programs:
        return True
    if _contains_systemd_run_command(segments):
        return True
    if _contains_mutating_crontab(segments):
        return True
    if _contains_watch_command(segments):
        return True
    return _contains_detached_interval_sleep(command, segments)


def _contains_mutating_crontab(segments: list[list[str]]) -> bool:
    for segment in segments:
        program_index = _command_program_index(segment)
        if (
            program_index is not None
            and _program_name(segment[program_index]) == "crontab"
            and not _is_crontab_list(segment, program_index)
        ):
            return True
    return False


def _is_crontab_list(tokens: list[str], program_index: int) -> bool:
    saw_list = False
    index = program_index + 1
    while index < len(tokens):
        option = tokens[index].rstrip(")")
        if option in {"-l", "--list"}:
            saw_list = True
            index += 1
            continue
        if option in {"-u", "--user"}:
            if index + 1 >= len(tokens):
                return False
            index += 2
            continue
        if option.startswith("--user="):
            index += 1
            continue
        redirection_end = _shell_redirection_end(tokens, index)
        if redirection_end is not None:
            index = redirection_end
            continue
        return False
    return saw_list


def _shell_redirection_end(tokens: list[str], index: int) -> int | None:
    if index >= len(tokens):
        return None
    if tokens[index].isdigit():
        index += 1
    if index >= len(tokens) or tokens[index] not in {
        "<",
        ">",
        ">>",
        ">|",
        "<>",
        "<&",
        ">&",
        "<<",
        "<<-",
        "<<<",
        "&>",
        "&>>",
    }:
        return None
    return index + 2 if index + 1 < len(tokens) else None


def _contains_watch_command(segments: list[list[str]]) -> bool:
    for segment in segments:
        program_index = _command_program_index(segment)
        if program_index is not None and _program_name(segment[program_index]) == "watch":
            return True
    return False


def _contains_systemd_run_command(segments: list[list[str]]) -> bool:
    for segment in segments:
        program_index = _command_program_index(segment)
        if (
            program_index is not None
            and _program_name(segment[program_index]) == "systemd-run"
            and not _is_systemd_run_inspection(segment, program_index)
        ):
            return True
    return False


def _is_systemd_run_inspection(tokens: list[str], program_index: int) -> bool:
    index = program_index + 1
    while index < len(tokens):
        option = tokens[index].rstrip(")")
        if option in {"-h", "--help", "--version"}:
            return True
        redirection_end = _shell_redirection_end(tokens, index)
        if redirection_end is not None:
            index = redirection_end
            continue
        if option == "--" or not option.startswith("-"):
            return False
        index += 1
    return False


def _contains_detached_interval_sleep(command: str, segments: list[list[str]]) -> bool:
    for segment in segments:
        program_index = _command_program_index(segment)
        if program_index is None or _program_name(segment[program_index]) != "sleep":
            continue
        if not any(argument.lower() in {"21600", "6h"} for argument in segment[program_index + 1 :]):
            continue
        wrappers = {_program_name(token) for token in segment[:program_index]}
        if "&" in command or wrappers.intersection({"nohup", "setsid"}):
            return True
    return False


def _recursive_shell_segments(command: str, depth: int = 0) -> list[list[str]]:
    segments = _shell_command_segments(command)
    if depth >= 3:
        return segments
    expanded = list(segments)
    for payload in _shell_substitution_payloads(command):
        expanded.extend(_recursive_shell_segments(payload, depth + 1))
    nested_commands = list(segments)
    for segment in segments:
        program_index = _command_program_index(segment)
        if program_index is None:
            continue
        nested_start = _shell_control_command_start(segment, program_index)
        if nested_start is not None:
            nested_command = segment[nested_start:]
            expanded.append(nested_command)
            nested_commands.append(nested_command)
    for segment in nested_commands:
        env_payload = _env_split_string_payload(segment)
        if env_payload is not None:
            expanded.extend(_recursive_shell_segments(env_payload, depth + 1))
        program_index = _command_program_index(segment)
        if program_index is None:
            continue
        program = _program_name(segment[program_index])
        if program == "eval":
            eval_payload = _eval_command_payload(segment, program_index)
            if eval_payload is not None:
                expanded.extend(_recursive_shell_segments(eval_payload, depth + 1))
        if program not in SHELL_INTERPRETERS:
            continue
        shell_payload = _shell_command_payload(segment, program_index)
        if shell_payload is not None:
            expanded.extend(_recursive_shell_segments(shell_payload, depth + 1))
    return expanded


def _shell_control_command_start(tokens: list[str], program_index: int) -> int | None:
    program = _program_name(tokens[program_index])
    if program in SHELL_CONTROL_PREFIXES and program_index + 1 < len(tokens):
        return program_index + 1
    if program == "case":
        for index in range(program_index + 1, len(tokens)):
            if tokens[index].endswith(")") and index + 1 < len(tokens):
                return index + 1
        return None
    if tokens[program_index].endswith(")") and program_index + 1 < len(tokens):
        return program_index + 1
    return None


def _shell_substitution_payloads(command: str) -> list[str]:
    payloads: list[str] = []
    index = 0
    quote: str | None = None
    while index < len(command):
        char = command[index]
        if quote == "'":
            if char == "'":
                quote = None
            index += 1
            continue
        if char == "\\":
            index += 2
            continue
        if char == "'" and quote is None:
            quote = "'"
            index += 1
            continue
        if char == '"':
            quote = None if quote == '"' else '"'
            index += 1
            continue
        if command.startswith("$(", index) or (
            quote is None and char in {"<", ">"} and index + 1 < len(command) and command[index + 1] == "("
        ):
            parsed = _parenthesized_substitution(command, index + 2)
            if parsed is not None:
                payload, end = parsed
                payloads.append(payload)
                index = end + 1
                continue
        if char == "`":
            parsed = _backtick_substitution(command, index + 1)
            if parsed is not None:
                payload, end = parsed
                payloads.append(payload)
                index = end + 1
                continue
        index += 1
    return payloads


def _parenthesized_substitution(command: str, start: int) -> tuple[str, int] | None:
    depth = 1
    index = start
    quote: str | None = None
    while index < len(command):
        char = command[index]
        if quote == "'":
            if char == "'":
                quote = None
            index += 1
            continue
        if char == "\\":
            index += 2
            continue
        if char == "'" and quote is None:
            quote = "'"
            index += 1
            continue
        if char == '"':
            quote = None if quote == '"' else '"'
            index += 1
            continue
        if command.startswith("$(", index):
            nested = _parenthesized_substitution(command, index + 2)
            if nested is None:
                return None
            _, end = nested
            index = end + 1
            continue
        if quote is None and char == "(":
            depth += 1
        elif quote is None and char == ")":
            depth -= 1
            if depth == 0:
                return command[start:index], index
        index += 1
    return None


def _backtick_substitution(command: str, start: int) -> tuple[str, int] | None:
    index = start
    while index < len(command):
        if command[index] == "\\":
            index += 2
            continue
        if command[index] == "`":
            return command[start:index], index
        index += 1
    return None


def _eval_command_payload(tokens: list[str], program_index: int) -> str | None:
    arguments: list[str] = []
    index = program_index + 1
    while index < len(tokens):
        if tokens[index] == "--" and not arguments:
            index += 1
            continue
        redirection_end = _shell_redirection_end(tokens, index)
        if redirection_end is not None:
            index = redirection_end
            continue
        arguments.append(tokens[index])
        index += 1
    return " ".join(arguments) if arguments else None


def _env_split_string_payload(tokens: list[str]) -> str | None:
    index = 0
    while index < len(tokens):
        while index < len(tokens) and _is_assignment(tokens[index]):
            index += 1
        if index >= len(tokens):
            return None
        program = _program_name(tokens[index])
        if program == "env":
            return _env_split_option_payload(tokens, index + 1)
        if program == "command" and _is_command_inspection(tokens, index + 1):
            return None
        options_with_value = WRAPPER_OPTIONS_WITH_VALUE.get(program)
        if options_with_value is None:
            return None
        index = _skip_wrapper_arguments(tokens, index + 1, options_with_value)
    return None


def _env_split_option_payload(tokens: list[str], index: int) -> str | None:
    env_options_with_value = WRAPPER_OPTIONS_WITH_VALUE["env"]
    while index < len(tokens):
        option = tokens[index]
        if _is_assignment(option):
            index += 1
            continue
        if option == "--" or not option.startswith("-"):
            return None
        option_name, separator, value = option.partition("=")
        if option_name in {"-S", "--split-string"}:
            if separator:
                return value
            return tokens[index + 1] if index + 1 < len(tokens) else None
        if option.startswith("-S") and len(option) > 2:
            return option[2:]
        if option_name in env_options_with_value and not separator:
            index += 2
        else:
            index += 1
    return None


def _shell_command_segments(command: str) -> list[list[str]]:
    try:
        lexer = shlex.shlex(command, posix=True, punctuation_chars=";&|<>\n")
        lexer.whitespace = " \t\r"
        lexer.whitespace_split = True
        lexer.commenters = ""
        tokens = list(lexer)
    except ValueError:
        return []
    segments: list[list[str]] = []
    segment: list[str] = []
    for token in tokens:
        if token and all(char in ";&|\n" for char in token):
            if segment:
                segments.append(segment)
                segment = []
            continue
        segment.append(token)
    if segment:
        segments.append(segment)
    return segments


def _command_program_index(tokens: list[str]) -> int | None:
    index = 0
    while index < len(tokens):
        while index < len(tokens) and (
            _is_assignment(tokens[index]) or tokens[index] in SHELL_COMMAND_PREFIXES
        ):
            index += 1
        if index >= len(tokens):
            return None
        program = _program_name(tokens[index])
        if program == "command" and _is_command_inspection(tokens, index + 1):
            return index
        options_with_value = WRAPPER_OPTIONS_WITH_VALUE.get(program)
        if options_with_value is None:
            return index
        index = _skip_wrapper_arguments(tokens, index + 1, options_with_value)
    return None


def _shell_command_payload(tokens: list[str], program_index: int) -> str | None:
    index = program_index + 1
    while index < len(tokens):
        option = tokens[index]
        if option == "--" or not option.startswith(("-", "+")):
            return None
        if option.startswith("-") and not option.startswith("--") and "c" in option[1:]:
            return tokens[index + 1] if index + 1 < len(tokens) else None
        option_name, separator, _ = option.partition("=")
        if option_name in SHELL_OPTIONS_WITH_VALUE and not separator:
            index += 2
        else:
            index += 1
    return None


def _is_command_inspection(tokens: list[str], index: int) -> bool:
    while index < len(tokens):
        option = tokens[index]
        if option == "--" or not option.startswith("-"):
            return False
        if "v" in option[1:] or "V" in option[1:]:
            return True
        index += 1
    return False


def _skip_wrapper_arguments(
    tokens: list[str],
    index: int,
    options_with_value: set[str],
) -> int:
    while index < len(tokens):
        token = tokens[index]
        if _is_assignment(token):
            index += 1
            continue
        if token == "--":
            return index + 1
        if not token.startswith("-") or token == "-":
            return index
        option_name, separator, _ = token.partition("=")
        if option_name in options_with_value and not separator:
            index += 2
        else:
            index += 1
    return index


def _is_assignment(token: str) -> bool:
    return re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", token) is not None


def _program_name(token: str) -> str:
    name = token.rsplit("/", 1)[-1].lower()
    if name.startswith("(") and not name.startswith("(("):
        name = name.lstrip("(").rstrip(")")
    return name


def _validate_create_input(value: Any, expectation: ScheduleRoutingExpectation, issues: list[str]) -> None:
    if not isinstance(value, dict):
        issues.append("schedule_create input must be an object")
        return
    if value.get("id") != expectation.schedule_id:
        issues.append(f"schedule_create id must equal {expectation.schedule_id!r}")
    interval = value.get("interval")
    if not isinstance(interval, dict) or interval.get("every_seconds") != expectation.every_seconds:
        issues.append(f"schedule_create interval.every_seconds must equal {expectation.every_seconds}")
    observation = value.get("observation")
    if not isinstance(observation, dict) or observation.get("content") != expectation.content:
        issues.append(f"schedule_create observation.content must equal {expectation.content!r}")


def _has_exact_completion_after(
    messages: list[dict[str, Any]],
    token: str,
    after: tuple[int, int],
) -> bool:
    for message_index, message in enumerate(messages):
        if message.get("role") != "assistant":
            continue
        blocks = message.get("blocks")
        if not isinstance(blocks, list):
            continue
        for block_index, block in enumerate(blocks):
            if (
                (message_index, block_index) > after
                and isinstance(block, dict)
                and block.get("type") == "text"
                and isinstance(block.get("text"), str)
                and block["text"].strip() == token
            ):
                return True
    return False


def _validate_persisted_config(
    path: pathlib.Path,
    expectation: ScheduleRoutingExpectation,
    issues: list[str],
) -> None:
    if not path.is_file():
        issues.append(f"missing observables config: {path}")
        return
    try:
        value = json.loads(path.read_text(encoding="utf-8", errors="replace"))
    except json.JSONDecodeError as exc:
        issues.append(f"observables config is invalid JSON: {exc}")
        return
    except OSError as exc:
        issues.append(f"read observables config: {exc}")
        return
    if not isinstance(value, dict):
        issues.append("observables config must be a JSON object")
        return
    observables = value.get("observables")
    if not isinstance(observables, list):
        issues.append("observables config must contain an observables array")
        return
    if len(observables) != 1:
        issues.append(f"observables config must contain exactly one entry, saw {len(observables)}")
        return
    entry = observables[0]
    if not isinstance(entry, dict):
        issues.append("persisted observable entry must be an object")
        return
    if entry.get("id") != expectation.schedule_id:
        issues.append(f"persisted id must equal {expectation.schedule_id!r}")
    if entry.get("type") != "schedule":
        issues.append("persisted type must equal 'schedule'")
    allowed_fields = {"id", "name", "type", "schedule_config"}
    unexpected_fields = sorted(set(entry) - allowed_fields)
    if unexpected_fields:
        issues.append("persisted entry contains legacy or unknown fields: " + ", ".join(unexpected_fields))
    schedule_config = entry.get("schedule_config")
    if not isinstance(schedule_config, dict):
        issues.append("persisted entry must contain schedule_config")
        return
    interval = schedule_config.get("interval")
    if not isinstance(interval, dict) or interval.get("every_seconds") != expectation.every_seconds:
        issues.append(f"persisted schedule_config.interval.every_seconds must equal {expectation.every_seconds}")
    observation = schedule_config.get("observation")
    if not isinstance(observation, dict) or observation.get("content") != expectation.content:
        issues.append(f"persisted schedule_config.observation.content must equal {expectation.content!r}")
