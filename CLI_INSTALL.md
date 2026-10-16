# Multica CLI — Installation Guide for AI Agents

> **Audience:** This document is designed for AI agents (Claude Code, Codex, etc.) to read and execute step-by-step. Each step includes the exact shell commands to run, expected outcomes, and error handling.

## One-Line Instruction for Users

Give this instruction to your AI agent:

```
Fetch https://github.com/dunzic/multica-worker/blob/main/CLI_INSTALL.md and follow the instructions to install Multica CLI, log in, and start the daemon on this machine.
```

---

## Step 1: Check if Multica CLI is already installed

Run:

```bash
multica version
```

- **If it prints a version string**: continue to **Step 2** unless its fork provenance is known. A community version number does not prove that our defaults are installed.
- **If command not found**: continue to **Step 2**.

---

## Step 2: Install the Multica CLI

> **Windows users:** Skip to [Option C: Windows (PowerShell)](#option-c-windows-powershell) below.

### Option A: macOS / Linux

Install only from this fork, even if another Multica CLI has the same version:

```bash
curl -fsSL https://raw.githubusercontent.com/dunzic/multica-worker/main/scripts/install.sh | bash
```

The installer verifies SHA-256 and the CLI's embedded version/baseline metadata
before installing to `~/.local/bin`, outside Homebrew.
Open a new terminal before using the CLI. Do not use the community Homebrew tap.
If no owned release is published, installation fails; it does not fall back to community binaries.

`No published CLI release` means this fork has not published an installable
release, not that the computer needs a different login. A branch push, server
deployment, or locally built DMG is not a GitHub Release. The maintainer must
publish the approved versioned CLI archives and matching `checksums.txt` under
this fork. Do not substitute a community download or bypass verification.
Network/HTTP failures and missing release assets have separate diagnostics.

### Option C: Windows (PowerShell)

Run in PowerShell (no admin required):

```powershell
irm https://raw.githubusercontent.com/dunzic/multica-worker/main/scripts/install.ps1 | iex
```

This downloads the latest Windows binary from GitHub Releases, installs it to `%USERPROFILE%\.multica\bin\`, and adds it to your user PATH.

Verify:

```powershell
multica version
```

**If this fails:**
- Restart your terminal so the updated PATH takes effect.
- For an offline ZIP, extract it and double-click `install.cmd`; then run `multica --profile turingomni login`.
- If your execution policy blocks the script: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1` then re-run.

---

## Step 3: Log in

Service: `https://workers.turingomni.com`. Community accounts do not automatically exist here. Use the isolated `turingomni` profile below so existing default/private configurations and credentials remain untouched.

Run:

```bash
multica setup --profile turingomni
```

**Important:** This command opens a browser window for OAuth authentication. Tell the user:

> "A browser window will open for Multica login. Please complete the authentication in your browser, then come back here."

Wait for the command to complete. It will automatically discover and watch all workspaces the user belongs to.
It also starts the daemon. If the `turingomni` profile already exists, setup
asks before resetting it; use `multica --profile turingomni login` to refresh
authentication without resetting an already-correct profile.

Verify:

```bash
multica --profile turingomni auth status
```

Expected output should show the authenticated user and server URL.

**If login fails:**
- If no browser is available (headless environment), the user can generate a Personal Access Token at `https://workers.turingomni.com/settings?tab=tokens` and run: `multica --profile turingomni login --token <mul_...>` (use `--token=` with an empty value to be prompted interactively).
- If the server URL needs to be customized: `multica --profile turingomni config set server_url <url>` before logging in.

---

## Step 4: Start the daemon

First, check if the daemon is already running:

```bash
multica --profile turingomni daemon status
```

- **If status is "running"**: skip to **Step 5**.
- **If status is "stopped"**: start it:

```bash
multica --profile turingomni daemon start
```

Wait 3 seconds, then verify:

```bash
multica --profile turingomni daemon status
```

Expected output should show `running` status with detected agents (e.g. `claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `pi`, `cursor-agent`, `grok`).

**If daemon fails to start:**
- Check logs: `multica --profile turingomni daemon logs`
- If a port conflict occurs, the daemon may already be running under a different profile.
- If no agents are detected, ensure at least one AI CLI (`claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `pi`, `cursor-agent`, or `grok`) is installed and on the `$PATH`.

---

## Step 5: Verify everything is working

Run:

```bash
multica --profile turingomni daemon status
```

Confirm:
1. Status is `running`
2. At least one agent is listed (e.g. `claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `pi`, `cursor-agent`, or `grok`)
3. At least one workspace is being watched

If the agents list is empty, tell the user:

> "The Multica daemon is running but no AI agent CLIs were detected. Please install at least one supported CLI (`claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `pi`, `cursor-agent`, or `grok`), then restart the daemon with `multica --profile turingomni daemon stop && multica --profile turingomni daemon start`."

---

## Version and update verification

```bash
multica version --output json
multica version --check-compatibility
multica update
```

The JSON report distinguishes the package version, full build commit, absorbed
upstream baseline, and implemented capabilities. The compatibility check is
offline; it does not contact GitHub or log in. `multica update` contacts the
owned release channel and validates the downloaded binary before replacement.
Missing metadata or capabilities, an older baseline, a mismatched tag, or an
invalid checksum stops the update. Local development builds are not eligible
for public self-updates; keep their real Git-derived label and rebuild locally.
The executable name remains `multica` / `multica.exe`.

---

## Summary

When all steps are complete, inform the user:

> "Multica CLI is installed and the daemon is running. Agents in your workspaces can now execute tasks on this machine. You can manage workspaces with `multica --profile turingomni workspace list` and view daemon logs with `multica --profile turingomni daemon logs -f`."
