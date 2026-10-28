# wong

An external brain for coding agents. Memory, preferences and skills live on your own wong
server, with a copy on every machine, and every note is checked against its sources.

The name comes from Wong, the librarian of Kamar-Taj.

## Why

Claude Code keeps its memory in `~/.claude/projects/<path>/memory/`, one folder per path:

- A product made of several repos ends up with several memories. What the agent learned in
  one repo, the next one never sees, and your other machine has nothing.
- Notes copy state that lives somewhere else ("not deployed yet", "ENG-15 is in progress")
  and quietly go stale. A month later they swear an issue is open that closed weeks ago,
  or cite a file that moved.
- Skills get scattered between `~/.claude/skills`, `~/.agents/skills` and each repo.

Syncing `~/.claude` with git solves the first point and none of the others. wong keeps
the memory true: each note says what it relies on, wong checks it, and the agents that
work on the project fix what went stale.

## What it does

- **One brain.** A folder of markdown notes (`~/.wong`): `me/` for what applies
  everywhere, `projects/<id>/` for each project, shared by all of its repos. Claude Code
  reads and writes it directly, and it can always be exported as plain markdown.
- **Checked against its sources.** `wong check` reports notes that cite a deleted file,
  call an issue open that is closed, cite a commit that is on no remote branch, link to
  a note that does not exist, or leave work for a deploy that already happened.
- **Kept by the agents.** wong never calls a model. The Claude Code plugin brings the
  outdated notes into each session, and the agent fixes them with the `maintain-brain`
  skill. Every note an agent saves is checked on the spot: kind, scope, tags in use,
  links that exist, the sources it cites, and no repeat of another note.
- **Notes when they matter.** When an agent opens or edits a file, it sees the notes that
  cite that file or its folder.
- **A server, if you want one.** `wong serve` is a single binary with SQLite:
  - sync between machines, with conflicts reported instead of overwritten;
  - every version of every note kept;
  - GitHub and Linear webhooks that leave tasks on the notes they make outdated;
  - a dashboard to read, edit, undo and browse the brain as a graph;
  - MCP for Codex, Cursor and any other agent.
- **Obsidian.** The brain opens as an Obsidian vault, and wong imports and exports vaults.

## Notes

A note is Claude Code's memory format plus a few fields:

```yaml
---
name: move-billing-to-stripe
description: Decided 2026-10-08 to move billing to Stripe; tracked in ENG-15
kind: decision
scope: project:acme
tags: [billing, payments]
sources:
  - linear:ENG-15
  - file:acme-api/app/billing/stripe.py
  - commit:acme-api@1a2b3c4
verified: 2026-10-27
pending: release
---

Billing moves to Stripe. The old provider stays until the migration in
[[billing-migration]] is done.
```

| Field | Meaning |
| --- | --- |
| `kind` | One of `preference`, `project`, `person`, `client`, `decision` or `reference`. |
| `scope` | `global`, which lives in `me/`, or `project:<id>`. |
| `sources` | What the claims rely on: `linear:<ID>`, `file:<repo>/<path>`, `commit:<repo>@<sha>`, `note:<name>` or a URL. |
| `verified` | When the note was last checked against them, set by `wong note verify`. A source that changes after this date sends the note back to review. |
| `pending` | `release` or `main` when the note leaves work for the next `v*` tag or push to the main branch. When that deploy happens, whoever does it, the user is reminded. |

Notes point to their sources instead of copying their state. Write "tracked in ENG-15",
not "ENG-15 is in progress".

## Install

You need Go 1.27 or later, and git.

```sh
go install github.com/17tayyy/wong/cmd/wong@latest

wong import --dry-run   # see how your Claude Code memory would be grouped into projects
wong import             # bring it into ~/.wong; ~/.claude is left as it is
git -C ~/.wong init     # optional: each session's changes become a commit
wong check              # see what is already outdated
```

`wong import` groups repos that share a parent folder into one project (`~/acme/*`
becomes `acme`). Use `--map PATH=ID` to override that. You can run it again later to
bring in what changed: notes the brain has not touched are updated, and notes changed on
both sides are reported.

Point Claude Code at the brain, and install the plugin:

```sh
wong link --all      # every repo wong knows keeps its memory in its project
wong link --global   # every other folder uses me/

claude plugin marketplace add 17tayyy/wong
claude plugin install wong@wong
```

`wong link` only writes `autoMemoryDirectory` to the repo's
`.claude/settings.local.json` and makes sure git ignores that file. `wong link --remove`
undoes it.

The plugin answers four of Claude Code's hooks:

- **Session start:** the index of `me/`, the tags in use, the format for new notes, and
  the notes of the project that may be outdated.
- **Before an agent reads or edits a file:** the notes that cite it.
- **After it saves a note:** what the note lacks.
- **Session end:** the session's changes, committed and pushed to the server.

## Server

Run it on any machine your others can reach:

```sh
wong serve --db /var/lib/wong/wong.db            # listens on :8787
wong serve --db /var/lib/wong/wong.db --new-token laptop
```

Each machine gets its own token. List tokens with `--tokens` and revoke one with
`--revoke-token <name>`. On each machine:

```sh
wong remote http://wong.lan:8787   # paste the token and press enter
wong sync                          # pull, then push
```

The plugin pulls when a session starts and pushes when it ends. A note changed on two
machines at once is never overwritten: the server's version goes to
`~/.wong/.wong/conflicts`, and `wong resolve <path> --keep local|server` ends the
conflict.

To reach the server from outside your network, put it behind a tunnel or a proxy with a
certificate, or pass `--tls-cert` and `--tls-key`. wong only sends a token over plain
http to an address on your own network.

### Dashboard

Open the server's address in a browser and sign in with a token, for example one made
with `--new-token browser`. You can:

- read and edit notes in place;
- rename, move and delete them, and undo any change;
- see what every machine and agent changed;
- browse the brain as a graph, coloured by project, kind or tag.

### Webhooks

Webhooks turn changes elsewhere into tasks on the notes they affect. Each task closes by
itself once the fixed note reaches the server.

- **GitHub:** `POST /hooks/github` for push events, with `WONG_GITHUB_SECRET` set on the
  server. A push to the default branch that removes a file a note cites leaves a task, and
  so does a new `v*` tag or a push to the main branch that a note marked `pending`.
- **Linear:** `POST /hooks/linear` for issue events, with `WONG_LINEAR_SECRET` set. An
  issue that a note cites leaves a task when it is completed or canceled.

The plugin records which GitHub repo belongs to which project. Only `/hooks/*` needs to be
reachable from the internet. To check Linear issues locally with `wong check`, set
`LINEAR_API_KEY` to a personal API key.

### MCP

The server speaks MCP over Streamable HTTP at `/mcp`. Agents can use these tools:
`get_context`, `search_notes`, `read_note`, `write_note`, `edit_note`, `verify_note`,
`move_note` and `delete_note`. They follow the same rules as the skill, and every change is
signed with the token that made it.

```sh
codex mcp add wong -- wong mcp     # stdio, through this machine's token
```

Clients that speak HTTP can use `<server>/mcp` directly with a token of their own.

## Obsidian

`~/.wong` opens as a vault: links resolve the same way for Obsidian and for agents, and
Obsidian's own settings stay on each machine.

```sh
wong obsidian export ~/vaults/brain                 # copy the brain into a new vault
wong obsidian import ~/vaults/notes --to acme --dry-run
```

## Commands

| Command | What it does |
| --- | --- |
| `wong import` | Bring Claude Code's memory folders into the brain |
| `wong link` | Make Claude Code keep its memory in the brain |
| `wong check` | Check every note against what it cites |
| `wong note show/search/write/verify/move/remove` | Read and write notes the way agents should |
| `wong tags` | List the tags in use |
| `wong tidy` | List duplicate preferences, notes in the wrong folder and notes that grow by additions |
| `wong skills save` | Record where your skills come from, and keep the ones that have no source |
| `wong serve` | Run the server |
| `wong remote`, `pull`, `push`, `sync`, `resolve` | Sync with the server |
| `wong tasks` | List the tasks that webhooks left |
| `wong mcp` | Serve the brain over MCP on stdio |
| `wong obsidian export/import` | Move notes between the brain and an Obsidian vault |

## Privacy

Your brain stays on your machines and your server. wong talks to your server and, when
you give it a key, to Linear, and to nothing else. It never asks for your Claude
credentials or stores them.

Notes about people and clients are personal data. Keep the server private, or behind
HTTPS with tokens.

## Status

Early. The author uses it every day on two machines. Planned next: a hosted version for
those who would rather not run a server, and teams.

## Contributing

`go test ./...`, `go vet ./...` and `gofmt` before sending a change. Commits read
`type(scope): description`, in lowercase and the imperative, with the reason in the body.

## License

[AGPL-3.0](LICENSE). Running wong for yourself or your team is free; a modified wong
offered as a service must publish its source.
