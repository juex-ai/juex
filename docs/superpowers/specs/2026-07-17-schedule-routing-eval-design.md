# Schedule Routing Evaluation Design

## Goal

Turn the one-off live Schedule routing check from Taskline task `5dac160d`
into a repeatable provider evaluation. A model asked to create timed work must
use the Schedule tool path, not a polling shell command or Command Observable,
and the resulting workspace config must use the tagged Schedule persistence
shape.

## Product contract

Every provider/model case selected by `provider-model-smoke` runs two isolated
workflows:

1. the existing file, shell, TTY, and stdin capability smoke;
2. a Schedule routing workflow in a separate temporary workspace.

The Schedule workflow uses a natural-language request for recurring six-hour
timed work with a fixed id and unique observation token. The prompt describes
the intent, the requirement to inspect existing timed sources first, and the
ban on commands and polling. It deliberately does not name either creation
tool, so the evaluation measures routing rather than prompt copying. The model
must:

- successfully load the required `juex-observables` guide before its first
  Observable tool use;
- successfully complete `observable_list` before issuing `schedule_create`;
- call `schedule_create` exactly once;
- not call `observable_create`, `exec_command`, `write_stdin`, or
  `list_shell_sessions`, including the legacy `shell` and `shell_input` names;
- persist the requested interval and observation under a tagged
  `type: schedule` entry with `schedule_config`;
- finish with the expected evaluation token.

The six-hour interval is deliberate: it is stable across timezones and test
run times, cannot fire during an ordinary evaluation, and exercises the same
one-time/daily/interval routing rule as the earlier one-shot manual check.

A routing failure fails that provider/model smoke result. The ordinary rotating
`make development-eval` path therefore gains Schedule routing regression
coverage without a new operator flag.

## Scope and non-goals

In scope:

- prompt construction and deterministic artifact validation in `tests/eval`;
- an isolated live Schedule subcase inside each selected provider smoke case;
- provider summary and development-record coverage counts;
- documentation and contract tests for the new evaluation.

Out of scope:

- production Observable, Schedule, runtime, tool, or skill behavior changes;
- deterministic claims about provider quality without a live provider;
- changing the provider/model rotation list;
- creating a new top-level eval command or a second credential/config loader;
- exercising actual Schedule delivery timing.

## Approaches considered

### A. Extend the existing seven-step capability conversation

This reuses one process invocation, but mixes unrelated file/TTY and Schedule
contracts in one transcript. A failure becomes harder to classify, and the
created Schedule shares workspace state with the shell workflow.

### B. Add a standalone `schedule-routing` command and development step

This gives a clean command surface but duplicates provider selection,
credentials, rotation, retry policy, report layout, and development-record
wiring. It also allows routine provider smoke to pass without the routing
regression.

### C. Add an isolated Schedule subcase to provider smoke

This is the selected design. It reuses the existing live-provider adapter and
selection policy while keeping the Schedule workspace, session, config, and
artifacts separate from the capability smoke. The default rotating development
evaluation covers it automatically.

## Architecture

`tests/eval/juex_eval/schedule_routing.py` owns the evaluation contract:

```python
@dataclass(frozen=True)
class ScheduleRoutingExpectation:
    schedule_id: str
    every_seconds: int
    content: str
    completion_token: str
```

Its public functions are:

```python
def build_prompt(expectation: ScheduleRoutingExpectation) -> str

def validate_contract(
    conversation_path: pathlib.Path,
    observables_path: pathlib.Path,
    expectation: ScheduleRoutingExpectation,
) -> contract_oracle.ContractReport
```

The validator is a pure artifact oracle. It parses canonical
`conversation.jsonl` tool-use and tool-result blocks in execution order and the
persisted `.juex/observables.json` file. It reports malformed JSON as explicit
contract issues and reuses the existing `contract_oracle.ContractReport`
instead of defining a second report type. It does not know how to select a
model, run a binary, or copy artifacts.

`tests/eval/juex_eval/helper.py` remains the live-provider adapter. After the
existing capability contract passes, it:

1. creates a never-before-used Schedule work directory and report directory
   for the current attempt;
2. writes the same selected provider/model config into that workspace;
3. runs a fresh `juex run --json --new` with the routing prompt;
4. locates the resulting session transcript and persisted config;
5. calls the Schedule routing oracle;
6. copies the Schedule transcript, events, config, stdout, and stderr under the
   provider case's `schedule-routing/attempt-N/` artifact directory;
7. marks the provider result failed when the contract fails.

Every retry receives both a new workspace and a new `--new` session; it must
not use the existing retry helper that reuses a case directory. Failed retry
artifacts are retained in attempt-specific report directories so a partially
created Schedule cannot make the next attempt pass or fail for the wrong
reason. Only the successful attempt is eligible for contract validation.

No production package imports evaluation code.

## Artifact contract

Before the first Observable tool use, the transcript must contain a successful
`skill_load` of `juex-observables`; `skill_search` may precede it. It must then
contain exactly one `observable_list` and exactly one `schedule_create`.
Ordering is result-aware:

```text
observable_list tool use
  -> successful observable_list tool result
  -> schedule_create tool use
  -> successful schedule_create tool result
```

Putting list and create in the same assistant tool batch therefore fails, even
if their blocks appear in that textual order. Missing or error tool results
also fail.

Forbidden tool names are:

```text
observable_create
exec_command
write_stdin
list_shell_sessions
shell
shell_input
```

The `schedule_create` input must contain:

```json
{
  "id": "<expected id>",
  "interval": {"every_seconds": 21600},
  "observation": {
    "content": "<expected tokenized content>"
  }
}
```

The persisted entry must contain:

```json
{
  "id": "<expected id>",
  "type": "schedule",
  "schedule_config": {
    "interval": {"every_seconds": 21600},
    "observation": {
      "content": "<expected tokenized content>"
    }
  }
}
```

Validation starts from the clean attempt directory, so the persisted
`observables` array must contain exactly this one entry. The oracle asserts the
fixed id, tagged type, exact interval, and exact tokenized observation content;
it does not depend on unspecified defaults. The entry must not contain
`command_config`, old top-level command fields, or a nested legacy `source`
union.

## Reporting

`SmokeResult` gains `schedule_routing_status` with `yes` or `no`.
Provider summary JSON gains `schedule_routing_verified`; the Markdown table
shows one Schedule routing column. Development records surface the same
aggregate when provider smoke ran.

Existing report keys remain unchanged, so readers that ignore unknown JSON
fields continue to work. One selected provider/model remains one `SmokeResult`
and contributes one item to `total`; the subscenario does not introduce a
second rotation or matrix dimension.

## Implementation plan

1. Add failing eval contract tests for the prompt, tool sequence, forbidden
   tools, and persisted Schedule shape.
2. Implement the pure `schedule_routing.py` expectation, prompt, and validator.
3. Add failing provider-result/report tests for the new routing status count.
4. Extend the provider smoke adapter with a fresh retry-safe Schedule
   workspace and artifact subtree.
5. Add the routing aggregate to provider summaries and development records.
6. Update evaluation and architecture documentation.
7. Run deterministic, build, integration, and one bounded live development
   evaluation against the rebuilt binary.

## Test plan

Deterministic contract tests create temporary transcript and config artifacts
and cover:

- passing guide load, ordered list result, create result, and tagged config;
- create-before-list and list/create in the same assistant tool batch;
- missing or error tool results for guide, list, and create;
- duplicate `schedule_create`;
- every forbidden tool;
- malformed transcript/config JSON;
- wrong id, recurrence, observation, type, or config branch;
- old `source`, `command_config`, and extra persisted entries;
- prompt content and stable six-hour expectation;
- additive provider summary/development-record coverage;
- retry orchestration uses different workspace/session paths and retains the
  failed attempt artifacts.

Focused verification:

```bash
go test ./tests/eval -count=1
uv run --project . python -m tests.eval.juex_eval --help
```

Repository verification:

```bash
make test
make build
make integration
```

Live verification uses the rebuilt binary through the same path users run:

```bash
bash tests/eval/development_eval.sh --only <provider:model>
```

The Test Report records the selected live model, routing sequence, persisted
shape, artifact paths, deterministic pass rate, and any provider limitation.
