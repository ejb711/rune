---
sidebar_position: 2
---

# Configuration

Rune Agent is installed as the `rune-agent` package, so its settings live
under `extensions.rune-agent.config` in your Rune config. Rune manages the
extension's entry and `path` for you; you normally only tweak the nested
`config` block. Every key is optional and falls back to a sensible
default.

```yaml
extensions:
  rune-agent:
    config:
      agents_file: AGENTS.md
      attribution:
        commit: 'Co-Authored-By: Rune Agent ({{provider}}/{{model}}) <agent@rune.build>'
      skills:
        - .rune/skills
        - ~/.rune/skills
        - .agents/skills
        - ~/.agents/skills
      editor_modal_start_insert: true
```

## Behavior

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `agents_file` | string | `AGENTS.md` | Name of the [project instructions](./intro.md#project-instructions) file to discover. The agent walks up from the workspace root looking for files with this name and injects their contents into the system prompt. Set it empty to disable discovery. |
| `attribution.commit` | string | `Co-Authored-By: Rune Agent ({{provider}}/{{model}}) <agent@rune.build>` | Attribution text Rune Agent is instructed to append to commits it creates. `{{provider}}` and `{{model}}` expand to the resolved model. Set it to an empty string to disable commit attribution. |
| `skills` | list | the four directories below | Ordered list of [skill directories](./skills.md#skill-directories) to search. Overrides the default set when present. |
| `max_line_bytes` | integer | `500` | Truncation limit, in bytes, for a single line of `read_file` output. |
| `max_tool_output_bytes` | integer | `40000` | Truncation limit, in bytes, for a single tool's output before it is trimmed in the transcript. |
| `auto_compact_ratio` | float | `0.85` | Fraction of the model's context window that must be filled before the agent automatically compacts the conversation. |
| `review_context_lines` | integer | `3` | How many surrounding lines of code to show around each hunk when you [review a chat's changes](./intro.md#reviewing-changes). Set it to `0` to show only the lines the agent actually changed. |
| `editor_modal_start_insert` | boolean | `true` | When the modal (vi-style) composer opens, start in insert mode so the first keystroke types instead of requiring `i`. Set to `false` to open in normal mode. Only affects the modal composer. |
| `audit_enabled` | boolean | `false` | Record a per-request LLM token audit log you can review with `chatlog` or `agent chats export --audit`. |
| `force_builtin_tools` | boolean | prompt | When set, decides up front whether bare `grep`-style shell commands are rejected in favor of the agent's built-in search tools. Left unset, the agent asks you the first time and remembers your choice. |

## Too many permission prompts?

Rune asks before Rune Agent runs workspace commands or uses other protected
workspace APIs. If those prompts are getting a bit overwhelming, you can have
Rune authorize workspace commands automatically:

```yaml
authorizer:
  auto_authorize_commands: true
```

`authorizer` is a top-level Rune setting, not part of
`extensions.rune-agent.config`. This setting applies to workspace commands from
other extensions and plugins too, so only enable it if you trust the code
installed in Rune. Existing saved denials still apply.

Rune separately grants non-command extension and plugin permissions
automatically by default. Set `authorizer.auto_authorize_extensions` to `false`
if you want Rune to prompt for those permissions too.

`auto_authorize_commands` only silences command prompts for extensions from a
**verified publisher** (and only when `authorizer.auto_authorize_verified` is
also on, which it is by default). Unverified extensions always prompt before
they run a command. See [Verified publishers](../develop/extensions.md#verified-publishers)
for the full trust model.

Models are configured host-side rather than in this block. Point the
`default`, `query`, `compact`, and `dream` aliases at real models with the
`models alias` command (see [Model aliases](./intro.md#model-aliases)), and
set the output token budget with `agent max_tokens`.

## Hooks

`hooks` wires shell commands or HTTP requests to fire at defined points in
the agent loop, letting you run scripts, post to a service, inject extra
context, or block an action. The configuration mirrors
[Claude Code's hook format](./claude-code.md#notify-rune-when-claude-is-done),
so the same block works in either tool.

### Events

A hook is registered against an **event**. These are the events Rune
Agent fires:

| Event | When it fires | Matched against |
| --- | --- | --- |
| `SessionStart` | A conversation starts. | the source (e.g. `startup`, `resume`) |
| `SessionEnd` | A conversation ends. | the end reason |
| `UserPromptSubmit` | You submit a prompt, before the model sees it. | (matches all) |
| `PostToolUse` | A tool call finishes. | the tool name (e.g. `read_file`, `bash`) |
| `Stop` | The agent finishes a turn. | (matches all) |
| `SubagentStop` | A spawned [sub-agent](./skills.md#sub-agents-and-planning) finishes. | (matches all) |
| `PreCompact` | Before a conversation is compacted. | the trigger (`auto` or `manual`) |
| `Notification` | The agent posts a notification. | the level (`error`, `warn`, `info`, `success`) |

### Shape

`hooks` maps each event name to a list of **groups**. A group pairs a
`matcher` with one or more hooks. The matcher decides which of that
event's occurrences the group applies to:

- `*` or omitted matches every occurrence.
- A plain name (or `|`-separated list like `read_file|bash`) matches those
  exact values.
- Anything else is treated as an [RE2](https://github.com/google/re2/wiki/Syntax)
  regular expression.

Each hook is either a `command` (a shell command) or an `http` request:

| Field | Applies to | Purpose |
| --- | --- | --- |
| `type` | both | `command` or `http`. Required. |
| `command` | `command` | The shell command to run. Required for `command`. |
| `shell` | `command` | Shell used to run `command`. Defaults to `bash`. |
| `url` | `http` | URL to `POST` the payload to. Required for `http`. |
| `headers` | `http` | Map of request headers. |
| `allowed_env_vars` | `http` | Env-var names allowed for `${VAR}` interpolation in header values. |
| `timeout` | both | Per-hook timeout, e.g. `10s` or a number of seconds. Defaults to `60s`. |

Every hook receives the event payload as JSON: command hooks read it on
stdin, HTTP hooks get it as the request body. The payload carries the
session ID, workspace path, event name, and event-specific fields such as
`tool_name`, `tool_input`, and `tool_response` for `PostToolUse`, or
`prompt` for `UserPromptSubmit`. Command hooks also get `RUNE_HOOK_EVENT`,
`RUNE_DIALOGUE_ID`, and `RUNE_PROJECT_DIR` in their environment.

### Reacting to the result

A hook can stay silent (exit `0`, empty output) or steer the agent:

- **Inject context.** For `UserPromptSubmit` and `SessionStart`, a
  command hook's plain-text stdout is added to the model's context. Any
  hook can also return JSON with
  `hookSpecificOutput.additionalContext` to do the same.
- **Block an action.** A command hook that exits with code `2` blocks the
  action, and its stderr becomes the reason shown to the agent.
  Equivalently, return JSON with `"decision": "block"` and a `"reason"`.
- Any other non-zero exit is logged as a non-blocking warning, so a
  failing hook never wedges the agent.

### Examples

Give every new conversation the current git branch and working-tree
status. On `SessionStart`, a command hook's stdout is folded into the
model's context, so the agent starts already knowing where the repo
stands:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        SessionStart:
          - hooks:
              - type: command
                command: git status --short --branch
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "SessionStart": [
                    {"hooks": [
                        {"type": "command",
                         "command": "git status --short --branch"},
                    ]},
                ],
            },
        },
    },
},
```

Run a formatter after every file write, matching only the `apply_patch`
tool:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        PostToolUse:
          - matcher: apply_patch
            hooks:
              - type: command
                command: make fmt
                timeout: 30s
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "PostToolUse": [
                    {"matcher": "apply_patch",
                     "hooks": [
                        {"type": "command", "command": "make fmt",
                         "timeout": "30s"},
                     ]},
                ],
            },
        },
    },
},
```

Add project context to every prompt, and block edits to a protected file.
The `UserPromptSubmit` hook prints text that is folded into the model's
context; the `PostToolUse` hook exits `2` to reject the tool call:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        UserPromptSubmit:
          - hooks:
              - type: command
                command: echo "Current sprint goal: ship the auth rewrite."
        PostToolUse:
          - matcher: apply_patch
            hooks:
              - type: command
                command: |
                  path=$(jq -r '.tool_input.path // empty')
                  case "$path" in
                    *config/secrets.yaml)
                      echo "Refusing to edit secrets.yaml" >&2
                      exit 2 ;;
                  esac
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "UserPromptSubmit": [
                    {"hooks": [
                        {"type": "command",
                         "command": 'echo "Current sprint goal: ship the auth rewrite."'},
                    ]},
                ],
                "PostToolUse": [
                    {"matcher": "apply_patch",
                     "hooks": [
                        {"type": "command", "command": '\n'.join([
                            'path=$(jq -r \'.tool_input.path // empty\')',
                            'case "$path" in',
                            '  *config/secrets.yaml)',
                            '    echo "Refusing to edit secrets.yaml" >&2',
                            '    exit 2 ;;',
                            'esac',
                        ])},
                     ]},
                ],
            },
        },
    },
},
```

Notify an external service over HTTP, interpolating a token from the
environment into a header:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        Stop:
          - hooks:
              - type: http
                url: https://hooks.internal/agent-done
                headers:
                  Authorization: "Bearer ${AGENT_HOOK_TOKEN}"
                allowed_env_vars:
                  - AGENT_HOOK_TOKEN
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "Stop": [
                    {"hooks": [
                        {"type": "http",
                         "url": "https://hooks.internal/agent-done",
                         "headers": {"Authorization": "Bearer ${AGENT_HOOK_TOKEN}"},
                         "allowed_env_vars": ["AGENT_HOOK_TOKEN"]},
                    ]},
                ],
            },
        },
    },
},
```

## Appearance

These keys style the chat tab and its composer. Each attribute value takes
an optional `fg` (foreground color), `bg` (background color), and `flags`
(such as `dim`, `italic`, or `bold`).

| Key | What it styles |
| --- | --- |
| `background_attr` | The chat tab background. |
| `user_msg_attr` | Your messages in the transcript. |
| `user_msg_background_attr` | The background behind your messages. |
| `assistant_msg_attr` | The agent's messages in the transcript. |
| `input_box_attr` | The composer's text content. |
| `input_box_placeholder_attr` | The composer's placeholder text. |
| `input_box_frame_attr` | The composer's border. |
| `input_box_frame_charset` | The character set used to draw the composer's border. |
| `input_background_color` | The composer's background color. |
| `context_hint_attr` | The context-usage hint shown near the composer. |
| `completion_matched_text_attr` | The matched substring in `#` context completion results. |
| `completion_focus_element_attr` | The selected row in the `#` context completion list. |
| `completion_element_attr` | Unselected rows in the `#` context completion list. |
| `completion_radar_color` | The radar sweep drawn over the composer while `#` candidates load. |
| `inline_attachment_attr` | A file or symbol name inserted into the composer from `#` completion. |

The `completion_*_attr` keys default to the command prompt's styling so both
search overlays look the same. `completion_radar_color` defaults to a neutral
gray instead, so the sweep reads the same under every theme.

Accepting a `#` completion keeps the attachment chip above the composer and
also inserts its readable name into the sentence. `inline_attachment_attr`
styles that name while it remains linked to the attachment. Rune maps the
link to an internal, turn-local attachment reference when sending the message;
that internal reference is not durable across conversation compaction.

```yaml
extensions:
  rune-agent:
    config:
      background_attr:
        bg: default
      user_msg_attr:
        fg: default
        bg: default
        flags: dim
      user_msg_background_attr:
        bg: gray
      assistant_msg_attr:
        fg: default
        bg: default
        flags: italic
      input_box_placeholder_attr:
        fg: red
        bg: default
      context_hint_attr:
        fg: red
      completion_matched_text_attr:
        fg: blue
        bg: default
        flags: bold
      completion_focus_element_attr:
        fg: purple
        bg: default
      completion_element_attr:
        fg: default
        bg: default
        flags: dim
      completion_radar_color: dimgray
      inline_attachment_attr:
        fg: aqua
        bg: default
        flags: underline
```
