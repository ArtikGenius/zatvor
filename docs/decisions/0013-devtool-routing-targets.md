# 0013: Dev-tool routing targets

Status: accepted, 2026-11-22 (spec §9; `internal/devtools`, `internal/daemon`).

## Context

Developer tools (shells, Claude Code, VS Code, Codex, Gemini CLI) do not read the system proxy. Routing them
through Zatvor means editing user files and wrapping commands, which must be undone exactly and must never
overwrite the user's own edits.

## Decision

- **Targets.** Five files and two wrappers:
  - `pwsh5`: `Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`
  - `pwsh7`: `Documents\PowerShell\Microsoft.PowerShell_profile.ps1`
  - `bash`: `~/.bashrc`
  - `claude`: `~/.claude/settings.json`, keys `env.HTTPS_PROXY` and `env.NO_PROXY`
  - `vscode`: `%APPDATA%\Code\User\settings.json`, keys `http.proxy` and `http.proxySupport`
  - wrappers `codex` and `gemini` (`.cmd`, `.ps1` and a shell script) in our own `BinDir`, which Zatvor does
    not put on `PATH`.

  A target whose tool directory does not exist is `Missing` and is not created (parent directories are never
  created).
- **State file (R23).** Shell blocks and wrappers read a state file (`Run\devtools.proxy`) at every prompt or
  start. The daemon writes it from engine events: `http://127.0.0.1:<port>` while connected, empty
  otherwise. Anything that is not exactly that URL counts as empty and is never run as code. So the files
  we edited stay valid when the tunnel is down. The daemon empties it at start and at exit.
- **Projection (R28).** One journal entry per file. `Prev` and `Applied` hold only our part of the file: the
  marked block for a shell profile, our two keys for a JSON file (plus an `exists` flag, R30). The user's
  other edits never look foreign, and a revert removes only our part. Files are edited through symlinks with
  an atomic write, a one-time `.zatvor.bak` copy and a size, mtime and SHA-256 check before the rename. A
  file that keeps changing is retried three times, then reported as `devtools_conflict`; we never force.
  JSON is patched in place (comments and formatting kept). A file we created, and nothing else of the user's
  in it, is deleted on disable.
- **Wrappers and depth (R29, R32).** Wrappers are our files and are not journaled: enable writes them,
  disable deletes them. A wrapper finds the real tool as the first match on `PATH` outside its own directory
  (the directory is skipped only for that lookup). `ZATVOR_WRAPPED` counts nesting: each wrapper passes
  depth+1 to the tool, and one that receives 8 or more refuses with exit code 1. A tool that runs itself
  through `PATH` keeps working; an accidental self-start loop stops early.
- **pwsh5 policy (R31).** A missing Windows PowerShell 5.1 profile is created only when the effective
  execution policy (group policy, then CurrentUser, then LocalMachine, read-only from the registry; the
  default on client Windows is Restricted) lets profiles run. Otherwise the target is `Missing` with a note.
  An existing profile is edited regardless.
- **Toggle.** The setting is saved in `settings`. A change while connected applies at the next connect.
  When the connect re-picks the dev-tool port, the daemon enables the targets again with the new port.

## Consequences

- The `.ps1` wrapper does not run under a Restricted policy (UI note in 1c).
- Diagnostics redact URL user info and dev-tool values from the journal (R50).
