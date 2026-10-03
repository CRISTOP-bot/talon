# Tools

Tools are the only way the model can affect anything. Each tool is a
`tools.Definition` with a name, a description sent to the model verbatim, a JSON
schema, a risk level and a handler.

## The 19 built-in tools

### Files

| Tool | Arguments | Notes |
|---|---|---|
| `read_file` | `path`, `offset?`, `limit?` | numbered lines; refuses files above `context`-independent size limits |
| `write_file` | `path`, `content`, `create_dirs?` | creates parents; preserves the existing mode |
| `edit_file` | `path`, `old_string`, `new_string`, `replace_all?` | requires a unique match, reports the count otherwise |
| `apply_patch` | `path`, `patch` | verifies context lines before writing anything |
| `delete_file` | `path`, `recursive?` | always confirmed |
| `list_directory` | `path?`, `recursive?`, `limit?` | sorted, directories first |

### Discovery

| Tool | Arguments | Notes |
|---|---|---|
| `search_files` | `pattern`, `limit?` | glob or substring |
| `search_text` | `query`, `regex?`, `glob?`, `case_sensitive?`, `include_tests?`, `max_results?` | returns `file:line: text` |
| `list_symbols` | `path` | declarations only — cheaper than reading a file |
| `inspect_project` | `include_tree?`, `depth?` | language, build, tests, git, hot files |

### Execution

| Tool | Arguments | Notes |
|---|---|---|
| `run_command` | `command`, `timeout_seconds?`, `cwd?` | own process group; output streamed and truncated |
| `run_tests` | `command?` | falls back to the detected runner |
| `build_project` | `command?` | falls back to the detected build |

### Git

| Tool | Arguments | Notes |
|---|---|---|
| `git_status` | — | branch plus changed files by category |
| `git_diff` | `staged?`, `path?`, `stat?` | unified diff |
| `git_log` | `limit?`, `path?`, `author?`, `since?` | |
| `git_branch` | — | marks the current branch |
| `git_commit` | `message`, `paths?` | stages then commits locally; never pushes |
| `git_checkout` | `branch`, `create?` | |

There is no push tool, and no code path in Talon runs `git push`.

## How arguments are checked

1. The raw JSON is decoded into the argument object.
2. `Validate` enforces `type`, `required`, `enum`, `pattern`, `minimum` and
   `maximum` from the schema.
3. The handler decodes into a typed struct and checks domain rules (for example,
   `edit_file` refusing an ambiguous match).
4. Permission checks run before the handler, using `path`/`command` metadata
   declared on the definition.

Validation errors are returned to the model as text, so it can correct the call.

## Output

Every result carries:

- `Content` — the text the model receives, truncated to `agent.max_tool_output`.
- `Display` — a short line for the terminal.
- `Diff` — present when files changed, so the UI can show a patch and `/diff`
  can replay it.
- `Changed` — the paths written, used for session summaries.
- `Metadata` — structured extras (exit codes, match counts, statistics).

## Adding a tool

```go
func myToolDef() *Definition {
    return &Definition{
        Name:        "my_tool",
        Description: "One precise paragraph: what it does and when to use it.",
        Parameters: llm.JSONSchema(map[string]any{
            "path": llm.Prop("string", "workspace-relative path"),
        }, "path"),
        Risk:      perm.RiskRead,
        PathArg:   "path",
        Summarize: summarizeWith("path", "Do the thing to %s"),
        Handler:   handleMyTool,
    }
}
```

The description is the model's only documentation: say what the tool does, when
to prefer it over a sibling tool, and any constraint worth knowing.

Add the definition to the matching slice in `internal/tools` and to `All()`, then
cover it in `tools_test.go`: happy path, error paths, schema validation and — if
it writes files — that the undo journal recorded the change.

## Plugins and MCP

External tools are produced as `tools.Definition` values too, so everything above
applies unchanged, including permissions. They are labelled by source:
`plugin:<name>` or `mcp:<name>` in `talon tools`. See
[plugins.md](plugins.md).
