<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/logo-dark.svg">
    <img src=".github/assets/logo-light.svg" width="72" height="72" alt="">
  </picture>
</p>

<h1 align="center">wong</h1>

<p align="center">
  <strong>An external brain for coding agents.</strong><br>
  Memory, preferences and skills on your own server, with a copy on every machine,<br>
  and every note checked against the code, commits and issues it cites.
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-2a78d6" alt="License: AGPL-3.0"></a>
  <img src="https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white" alt="Go 1.27">
  <img src="https://img.shields.io/badge/Claude_Code-plugin-eb6834" alt="Claude Code plugin">
  <img src="https://img.shields.io/badge/MCP-server-1baf7a" alt="MCP server">
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#why">Why</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#the-server">Server</a> ·
  <a href="#obsidian">Obsidian</a> ·
  <a href="#commands">Commands</a>
</p>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/graph-dark.png">
  <img src=".github/assets/graph-light.png" alt="The wong dashboard showing a brain as a graph: notes coloured by project, tags as square nodes, and outdated notes ringed in red and yellow">
</picture>

## Install

wong has two parts: the **server**, which keeps the brain and its history, and **wong on
each machine**, which keeps a copy that Claude Code reads and writes. The server starts
empty, and the first machine you connect fills it with its memory.

### 1. Start the server

On any machine your others can reach:

```sh
docker run -d --name wong --restart unless-stopped -p 8787:8787 -v wong:/data ghcr.io/17tayyy/wong
docker exec wong /wong serve --new-token browser    # signs you in to the dashboard
```

Open the server's address in a browser and sign in with that token. Machines are let in
from there.

<details>
<summary>With Docker Compose, or without Docker</summary>

```yaml
# compose.yaml
services:
  wong:
    image: ghcr.io/17tayyy/wong:latest
    restart: unless-stopped
    ports: ["8787:8787"]
    volumes: ["wong-data:/data"]
volumes:
  wong-data:
```

Without Docker, download the binary for your platform from the
[releases](https://github.com/17tayyy/wong/releases) and run
`wong serve --db /var/lib/wong/wong.db` under systemd or any supervisor.

</details>

### 2. Connect each machine

On macOS or Linux, one line:

```sh
curl -fsSL https://wong.bytay.dev/install.sh | sh -s -- http://wong.lan:8787
```

The script installs wong and runs `wong setup`, which shows a code. Enter it on the
dashboard's **Connect a machine** page, and the machine gets a token of its own. Then
`wong setup`:

1. Pulls the brain from the server.
2. Imports this machine's Claude Code memory and pushes it.
3. Points Claude Code at the brain: every repo to its project, every other folder to
   `me/`.
4. Installs the wong plugin for Claude Code.

On the first machine your memory goes up to the empty server. On the others the brain
comes down, and their own memory is added. From then on, the plugin pulls when a session
starts and pushes when it ends.

<details>
<summary>Windows, and how memory is grouped into projects</summary>

On Windows, download `wong-windows-amd64.exe` from the
[releases](https://github.com/17tayyy/wong/releases), save it as `wong.exe` in a folder
on your PATH, and run `wong setup http://wong.lan:8787`.

Repos that share a parent folder become one project (`~/acme/*` becomes `acme`). A
folder of unrelated repos such as `~/code` is the exception: each repo there is its own
project. `wong import --dry-run` shows the grouping first, and `wong setup --map PATH=ID`
changes it. Nothing in `~/.claude` is changed, and `wong link --remove` undoes the links.

</details>

### Without a server

To try wong on a single machine, run the script without a url:

```sh
curl -fsSL https://wong.bytay.dev/install.sh | sh
```

`wong setup <url>` connects that machine to a server later.

## Why

Claude Code keeps its memory in one folder per path, and that memory rots:

- **It is split.** A product made of several repos ends up with several memories. What
  the agent learned in one repo, the next one never sees, and your other machine has
  nothing.
- **It goes stale.** Notes copy state that lives somewhere else ("not deployed yet",
  "ENG-15 is in progress"). A month later they swear an issue is open that closed weeks
  ago, or cite a file that moved.
- **It is scattered.** Skills end up in `~/.claude/skills`, `~/.agents/skills` and every
  repo.

Syncing `~/.claude` with git fixes the first point and none of the others. wong keeps the
memory true: each note says what it relies on, wong checks it, and the agents that work on
the project fix what went stale.

The name comes from Wong, the librarian of Kamar-Taj.

## How it works

```mermaid
flowchart LR
  subgraph machine ["Each machine"]
    agent["Claude Code<br>+ wong plugin"] <--> brain[("~/.wong<br>markdown notes")]
  end
  brain <-->|"pull · push"| server["wong serve<br>SQLite · dashboard"]
  server <-->|"MCP"| others["Codex · Cursor<br>other agents"]
  github["GitHub"] -->|"webhooks"| server
  linear["Linear"] -->|"webhooks"| server
```

- **One brain.** It is a folder of markdown notes. `me/` holds what applies everywhere,
  and `projects/<id>/` holds each project, shared by all of its repos. Claude Code reads
  and writes it directly, and you can always export it as plain markdown.
- **Checked against its sources.** `wong check` finds notes that:
  - cite a file that was deleted;
  - call an issue open that is closed;
  - cite a commit that is on no remote branch;
  - link to a note that does not exist;
  - leave work for a deploy that has already happened.
- **Kept by the agents.** wong never calls a model. The plugin brings the outdated notes
  into each session, and the agent fixes them with the `maintain-brain` skill.
- **There when it matters.** When an agent opens a file, it sees the notes that cite that
  file. When it saves a note, wong tells it what the note is missing.
- **Your server.** `wong serve` is a single binary with SQLite, or a Docker image. It
  syncs your machines and keeps every version of every note. It also turns GitHub and Linear
  events into tasks, serves a dashboard and speaks MCP.

## What the agent sees

When a session starts in a repo of the `acme` project:

```text
wong: this session's memory is the brain's project "acme" (~/.wong/projects/acme).

Global preferences from the brain; the notes are in ~/.wong/me, read one when it applies:
- [use-uv](use-uv.md) — Use uv for Python, never pip
- [no-commit-trailers](no-commit-trailers.md) — Commits carry no Co-Authored-By or other trailers

wong: notes of this project leave work for a deploy that has happened. Tell the user
about it before you start on their request:
- projects/acme/run-backfill-after-release.md: acme-api released v1.2.0 on 2026-10-20:
  tell the user what this note leaves for the release, do your part, then update the note

wong check: 1 finding says notes of this project's memory contradict their sources. When
you have finished what the user asked, fix them with the wong:maintain-brain skill:
- projects/acme/old-invoice-pdf.md: app/billing/pdf.py deleted or moved in c5fadf0 on
  2026-10-05, after the note was last updated on 2026-09-15
```

When it is about to edit `app/billing/webhooks.py`:

```text
wong: notes of the brain's project "acme" cite app/billing/webhooks.py or its folder.
Read the ones that bear on what you are about to do:
- billing-webhooks (~/.wong/projects/acme/billing-webhooks.md): Stripe webhooks are
  handled in app/billing/webhooks.py
```

When it saves a note the quick way, the way Claude Code writes memory:

```text
wong: projects/acme/stripe-retries.md is saved, but needs this so wong can check it.
Fix it now, in one edit:
- add `kind`: preference, project, person, client, decision or reference
- set `scope: project:acme`
- add a few `tags` for what it is about, reusing those in use: deploy, oncall, billing, …
- projects/acme/billing-webhooks.md (Stripe webhooks are handled in
  app/billing/webhooks.py) seems to cover the same subject: if it does, add what is new
  to it and delete this one
- declare what it cites in `sources`, so wong notices when it changes: linear:ENG-15,
  file:acme-api/app/billing/webhooks.py
```

## Notes

A note is Claude Code's memory format plus a few fields:

```yaml
---
name: move-billing-to-stripe
description: Decided 2026-09-02 to move billing to Stripe; tracked in ENG-15
kind: decision
scope: project:acme
tags: [billing, payments]
sources:
  - linear:ENG-15
  - file:acme-api/app/billing/webhooks.py
verified: 2026-10-27
---

Billing moves to Stripe. Webhooks first, see [[billing-webhooks]].
```

| Field | Meaning |
| --- | --- |
| `kind` | One of `preference`, `project`, `person`, `client`, `decision` or `reference`. |
| `scope` | `global`, which lives in `me/`, or `project:<id>`. |
| `tags` | A few tags. wong refuses a new spelling of a tag already in use. |
| `sources` | What the claims rely on: `linear:<ID>`, `file:<repo>/<path>`, `commit:<repo>@<sha>`, `note:<name>` or a URL. |
| `verified` | When the note was last checked against them, set by `wong note verify`. A source that changes after this date sends the note back to review. |
| `pending` | `release` or `main` when the note leaves work for the next `v*` tag or push to the main branch. Whoever deploys, the user is reminded. |

Notes point to their sources instead of copying their state. Write "tracked in ENG-15",
not "ENG-15 is in progress".

## The server

Each machine has its own token, which it gets when you let it in. `wong serve --tokens`
lists them, and `--revoke-token <name>` revokes a lost machine's. With Docker, run them
as `docker exec wong /wong serve --tokens`. A token piped to `wong setup <url>` or set in
`WONG_TOKEN` connects a machine without the dashboard, for scripts.

One server can keep a brain for each person or team: `wong serve --accounts <dir>` keeps
each account's brain in a SQLite file of its own, and `--account <id> --new-token browser`
creates an account. Its tokens open that brain alone, in the API, the dashboard and MCP.

A note changed on two machines at once is never overwritten. The server's version goes
to `~/.wong/.wong/conflicts`, and `wong resolve <path> --keep local|server` settles it.

To reach the server from outside your network, put it behind a tunnel or a proxy with a
certificate, or pass `--tls-cert` and `--tls-key`. wong only sends a token over plain
http to an address on your own network.

### Dashboard

Open the server's address in a browser and sign in with a token, or run `wong open` on a
connected machine to open it signed in. You can:

- read and edit notes in place, then rename, move or delete them;
- undo any change, and see what every machine and agent changed;
- browse the brain as a graph, coloured by project, kind or tag;
- let machines in, and set up the webhooks.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/note-dark.png">
  <img src=".github/assets/note-light.png" alt="A note in the wong dashboard with its tags, kind and status, and a red callout saying acme-api released v1.2.0, so the work the note leaves for that release is due">
</picture>

### Webhooks

Webhooks turn changes elsewhere into tasks on the notes they affect. Each task closes by
itself once the fixed note reaches the server.

- **GitHub:** push events at `/hooks/github`. A task is left by a push to the default
  branch that removes a file a note cites. A new `v*` tag or a push to the main branch
  also leaves one on the notes marked `pending` for it.
- **Linear:** issue events at `/hooks/linear`. An issue that a note cites leaves a task
  when it is completed or canceled.

The dashboard's **Webhooks** page shows each URL and the events to pick, makes a secret
for GitHub and keeps the one Linear shows. `WONG_GITHUB_SECRET` and `WONG_LINEAR_SECRET`
on the server set them too. With `--accounts`, each account has its own, at
`/hooks/<account>/github` and `/hooks/<account>/linear`. The plugin records which GitHub
repo belongs to which project. Only `/hooks/*` needs to be reachable from the internet.
To check Linear issues locally with `wong check`, set `LINEAR_API_KEY` to a personal API
key.

### MCP

The server speaks MCP over Streamable HTTP at `/mcp`, with these tools:

`get_context` · `search_notes` · `read_note` · `write_note` · `edit_note` · `verify_note` · `move_note` · `delete_note`

They follow the same rules as the skill, and every change is signed with the token that
made it.

```sh
codex mcp add wong -- wong mcp     # stdio, through this machine's token
```

Clients that speak HTTP can use `<server>/mcp` directly with a token of their own.

## Obsidian

`~/.wong` opens as an Obsidian vault. Links resolve the same way for Obsidian and for
agents, and Obsidian's own settings stay on each machine.

```sh
wong obsidian export ~/vaults/brain                       # copy the brain into a new vault
wong obsidian import ~/vaults/notes --to acme --dry-run   # bring a vault's notes in
```

## Commands

<details>
<summary>Every command, in one table</summary>

| Command | What it does |
| --- | --- |
| `wong setup` | Make the brain this machine's memory, with a server or on its own, and install the plugin |
| `wong import` | Bring Claude Code's memory folders into the brain |
| `wong link` | Make Claude Code keep its memory in the brain |
| `wong check` | Check every note against what it cites |
| `wong note show/search/write/verify/move/remove` | Read and write notes the way agents should |
| `wong tags` | List the tags in use |
| `wong tidy` | List duplicate preferences, notes in the wrong folder and notes that grow by additions |
| `wong skills save` | Record where your skills come from, and keep the ones that have no source |
| `wong serve` | Run the server |
| `wong remote`, `pull`, `push`, `sync`, `resolve` | Sync with the server |
| `wong open` | Open the dashboard in a browser, signed in |
| `wong tasks` | List the tasks that webhooks left |
| `wong mcp` | Serve the brain over MCP on stdio |
| `wong obsidian export/import` | Move notes between the brain and an Obsidian vault |

</details>

## Privacy

Your brain stays on your machines and your server. wong talks to your server and, when
you give it a key, to Linear, and to nothing else. It never asks for your Claude
credentials or stores them.

Notes about people and clients are personal data. Keep the server private, or behind
HTTPS with tokens.

## Status

Early. The author uses it every day on two machines. Next: a hosted version for those who
would rather not run a server, and teams.

## Contributing

`go test ./...`, `go vet ./...` and `gofmt` before sending a change. Commits read
`type(scope): description`, in lowercase and the imperative, with the reason in the body.

## License

[AGPL-3.0](LICENSE). Running wong for yourself or your team is free; a modified wong
offered as a service must publish its source.
