# GitHubWatcher

A Telegram bot in Go that watches several GitHub repositories and posts nicely
formatted notifications into an admin chat — a forum topic, a plain group, or
someone's direct messages, in any combination.

## What it watches

| Event | What arrives |
|---|---|
| `commits` | New commits per watched branch (one message per branch) |
| `releases` | Releases and pre-releases, with notes |
| `tags` | New tags |
| `pull_requests` | Opened / merged / closed / reopened |
| `issues` | Opened / closed / reopened |

The set of events is configured per repository.

## What a message looks like

```
📦 🧠 Core · main
─────────────
🔨 3 new commits

▸ a1b2c3d feat: referral system
   ivan
▸ 9f8e7d6 fix: race in cache invalidation
   petya
▸ 4c5b6a7 chore: bump dependencies
   ivan

🕒 16.08.2026 14:32
```

Icons, wording and layout are configurable — see
[Message presentation](#message-presentation).

## Quick start

```bash
cp .env.example .env               # bot token, chat id, topic id
cp repos.example.json repos.json   # repositories and their display names
cp messages.example.json messages.json   # optional: presentation

go run ./cmd/bot -check            # validate config, token and access
go run ./cmd/bot                   # run
```

Or with Docker:

```bash
docker compose up -d --build
docker compose logs -f
```

### Telegram setup

1. Create a bot with [@BotFather](https://t.me/BotFather) and take the token.
2. Add the bot to the chat and let it send messages.
3. For a forum: enable Topics in the supergroup and create a topic for
   notifications.
4. `TELEGRAM_TOPIC_ID` — open the topic in Telegram Web; the id is the last
   number in the URL.
5. `TELEGRAM_CHAT_ID` — the supergroup id, which starts with `-100`. For direct
   messages use the person's numeric id (ask
   [@userinfobot](https://t.me/userinfobot)) — and note that the person has to
   press Start in the bot first, otherwise Telegram refuses the delivery.

### Where notifications go

One bot can post to several places at once. The primary recipient is
`TELEGRAM_CHAT_ID` plus the optional `TELEGRAM_TOPIC_ID`; extra recipients go
into `TELEGRAM_TARGETS` as a comma-separated list, each entry either `chat_id`
or `chat_id:topic_id`:

```bash
TELEGRAM_CHAT_ID=-1001234567890
TELEGRAM_TOPIC_ID=42
TELEGRAM_TARGETS=-1009876543210, 123456789, -1001234567890:7
```

That example delivers to four places: topic 42 of the admin forum, a plain
group with no topics, one person's direct messages, and a second topic of the
same forum. Duplicates are collapsed, so listing a recipient twice is harmless.

If one recipient answers with a permanent error — the bot was blocked, removed
from the chat, or the topic was deleted — that recipient is disabled until
restart and logged once; the others keep receiving messages. A temporary
failure is retried on the next cycle, which can duplicate a message for
recipients that already got it. Delivering twice is the deliberate trade-off
against losing an event.

### The GitHub token

`GITHUB_TOKEN` is optional for public repositories, but without it the whole
bot shares 60 requests per hour, which covers about two repositories. With a
token it is 5000 per hour, and `304 Not Modified` responses (nothing changed)
cost no budget at all.

Permissions: a classic token with the `repo` scope, or a fine-grained one with
`Contents: read` and `Metadata: read`, plus `Pull requests: read` and
`Issues: read` if you watch those.

## Repository configuration

`repos.json` is the only place display names are defined:

```json
{
  "defaults": {
    "branches": ["main"],
    "events": ["commits", "releases", "pull_requests"]
  },
  "repos": [
    {
      "name": "Core",
      "emoji": "🧠",
      "owner": "github",
      "repo": "core",
      "branches": ["main", "develop"]
    },
    {
      "name": "Landing",
      "emoji": "🌐",
      "owner": "github",
      "repo": "landing",
      "events": ["commits"],
      "ignore_authors": ["dependabot[bot]", "github-actions[bot]"]
    }
  ]
}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | The name shown in messages. Must be unique |
| `emoji` | no | Icon in front of the name |
| `owner` / `repo` | yes | The GitHub path; never leaves the server |
| `branches` | no | Branches to watch. Omit for the default branch |
| `events` | no | Event kinds; falls back to `defaults` |
| `ignore_authors` | no | Logins whose events are dropped (bots) |
| `enabled` | no | `false` disables it without deleting the entry |

The file is validated strictly: a typo in a field name, a duplicated
repository or an unknown event kind fails at startup with a clear message
instead of being silently ignored.

### Where `owner` and `repo` come from

No links go into the config — the bot builds the API address itself, and
neither `owner` nor `repo` reaches a message. Only two parts of the URL matter:

```
https://github.com/xsqclown/githubwatcher
                   └── owner ──┘└─ repo ─┘
```

```json
{ "name": "Backend", "emoji": "⚙️", "owner": "xsqclown", "repo": "backend" }
```

Everything after that is irrelevant — `/tree/develop`, `/pull/42`, `.git`,
`?tab=readme`. To watch the `develop` branch, list it in `branches` rather than
in a URL.

Case does not matter to GitHub, but matching the URL keeps things readable.

### Organisation repositories

For an organisation, `owner` is its name exactly as in the URL. Nothing else
changes: the API does not distinguish a personal repository from an
organisation one.

The difference is the token's access, and only for **private** repositories:

- **Classic token** — the `repo` scope. If the organisation enforces SAML SSO,
  the token needs an extra step: *Configure SSO → Authorize* for that
  organisation in the token list. Without it the API replies `404`, as though
  the repository did not exist.
- **Fine-grained token** — set *Resource owner* to the organisation and select
  the repositories. The organisation must permit such tokens
  (*Settings → Third-party Access → Personal access tokens*), and the request
  usually needs an admin to approve it.
- The token's owner must be a member of the organisation with read access to
  those repositories.

### Which token to create

The bot only ever issues `GET` requests, so it never needs write access. The
recommended setup is a **fine-grained, read-only token scoped to specific
repositories**:

- *Resource owner* — the organisation, not a personal account, or its private
  repositories stay invisible
- *Repository access* — *Only select repositories*, listing just the ones you
  watch
- *Permissions* — `Contents: Read-only`, `Metadata: Read-only`,
  `Pull requests: Read-only`, and `Issues: Read-only` if you watch issues
- *Expiration* — a finite one (90 days), not "No expiration"

A classic token with the `repo` scope grants **read and write to every**
repository its owner can reach, across every organisation at once; if it leaks,
the blast radius is not comparable. Use it only if fine-grained tokens are
blocked by organisation policy.

The token's value is visible to nobody on GitHub, organisation owners included
— they only see that access exists and can revoke it. The real leak paths are
`.env`, the server environment and logs. So:

- keep `.env`, `repos.json` and `messages.json` in `.gitignore` (they already
  are) with `600` permissions
- in Docker pass them via `env_file` or secrets, never `ENV` baked into an image
- on any suspicion, *Revoke* and issue a new token

Tokens stay out of the logs: the Telegram client masks its token inside error
text, the GitHub token is never printed, and the startup line shows only
`github_auth=true/false`.

If the token should not depend on one person, there are options beyond a PAT:
a **GitHub App** (installation tokens live an hour and refresh themselves —
requires extending `internal/github`) or a **machine user**, a separate
read-only bot account.

Check everything at once:

```bash
go run ./cmd/bot -check
```

```
Notification recipients:
  • chat -1001234567890, topic 42
  • direct messages, chat_id 123456789

Repository access:
  ✓ Backend — xsqclown/backend (private, default branch main)
      ✓ branch "develop"
  ✗ Landing — xsqclown/landing: not found — check owner/repo; for a private
    organisation repository the token must have access to it (and be
    SSO-authorised)
```

It verifies the bot token, the whole config, access to every repository and the
existence of every branch you named — catching both a `main` vs `master` typo
and a token that was never SSO-authorised.

`repos.schema.json` sits next to it: editors that understand JSON Schema will
autocomplete the file and flag mistakes.

> Enabling `releases` and `tags` together does not produce two messages for one
> release: the bot suppresses a tag it has already announced as a release.

## Message presentation

Everything the bot writes — icons, wording, dividers, truncation limits — lives
in `messages.json`. The file is optional and layered over the built-in
defaults, so it only needs the fields you want to change:

```json
{
  "divider": "",
  "show_author": false,
  "icons": { "commits": "✨" },
  "labels": { "release": "Ship it" }
}
```

Nested objects merge too, so overriding one plural form keeps the others.

| Field | Default | Meaning |
|---|---|---|
| `divider` | `─────────────` | Rule under the header; `""` removes it |
| `time_format` | `02.01.2006 15:04` | Go layout for the footer timestamp |
| `show_footer` | `true` | Render the timestamp |
| `show_author` | `true` | Render event authors |
| `show_labels` | `true` | Render issue and pull request labels |
| `show_body` | `true` | Render release notes |
| `expandable_body` | `true` | Collapse notes into an expandable quote |
| `max_subject_len` | `140` | Commit subject limit |
| `max_title_len` | `180` | Release / issue / PR title limit |
| `max_body_len` | `600` | Release notes limit |
| `icons` | see below | Glyphs; `""` drops one cleanly |
| `labels` | see below | Every word the bot writes |

Two ready-made files ship with the project: `messages.example.json` (the
English defaults, fully spelled out) and `messages.ru.json` (Russian wording).
Point `MESSAGES_CONFIG` at either, or copy one and edit.

Counted nouns use three forms — `one`, `few`, `many`. English sets `few` and
`many` to the same string; Slavic languages use `few` for counts ending in 2-4:

```json
{ "one": "новый коммит", "few": "новых коммита", "many": "новых коммитов" }
```

`messages.schema.json` provides autocompletion and validation in editors.

## Environment variables

The full annotated list is in `.env.example`. The essentials:

| Variable | Default | Purpose |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | Token from @BotFather (required) |
| `TELEGRAM_CHAT_ID` | — | Primary chat id |
| `TELEGRAM_TOPIC_ID` | `0` | Topic id; `0` means General |
| `TELEGRAM_TARGETS` | — | Extra recipients: `chat_id[:topic_id]`, comma-separated |
| `TELEGRAM_SEND_INTERVAL` | `3s` | Gap between messages |
| `TELEGRAM_SILENT` | `false` | Send without a sound |
| `TELEGRAM_NOTIFY_ON_START` | `true` | Announce startup |
| `GITHUB_TOKEN` | — | PAT; without it, 60 req/h |
| `GITHUB_API_URL` | `https://api.github.com` | For GitHub Enterprise |
| `POLL_INTERVAL` | `2m` | Poll interval (minimum `30s`) |
| `POLL_JITTER` | `15s` | Random spread of the interval |
| `POLL_CONCURRENCY` | `4` | Repositories polled in parallel |
| `MAX_COMMITS_PER_MESSAGE` | `10` | Commits shown in one message |
| `MAX_EVENTS_PER_POLL` | `20` | Cap on events per poll |
| `TIMEZONE` | `UTC` | Timestamp timezone, e.g. `Europe/Moscow` |
| `REPOS_CONFIG` | `repos.json` | Path to the repository list |
| `MESSAGES_CONFIG` | `messages.json` | Path to the presentation config |
| `STATE_FILE` | `data/state.json` | State file |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `text` | Logging |
| `HEALTH_ADDR` | — | e.g. `:8080` → `/healthz`, `/readyz` |

At least one recipient is required: either `TELEGRAM_CHAT_ID` or
`TELEGRAM_TARGETS`.

The real environment wins over `.env`, so Docker and Kubernetes can override
values without touching the file.

## Flags

```
-env <path>      path to .env (default ./.env)
-check           validate the configuration and access, then exit
-reset-state     delete the state file before starting
-version         print the version
```

`-check` belongs in CI and in the deploy script: it catches config typos, an
invalid token and unreachable repositories before the bot goes to background.

## How it is put together

```
cmd/bot            entry point: config, logger, signals, healthcheck
internal/config    .env + repos.json, all validation
internal/github    REST client: ETags, retries, rate limits
internal/telegram  Bot API: recipients, throttling, 429, message splitting
internal/model     domain event types
internal/render    HTML messages and their presentation config
internal/state     what has already been sent (atomic writes)
internal/watcher   the polling loop and publication
```

Decisions worth knowing before you change anything:

- **The first poll says nothing.** A new repository is seeded first: the bot
  records the current position and stays quiet. Otherwise adding a repository
  would dump a hundred messages at once.
- **State advances only after a successful send.** If Telegram is down the
  event is not lost — it goes out next cycle. For the same reason the `ETag` is
  stored last: until the message is delivered, the next poll must get the full
  response rather than a `304`.
- **A force push does not break the feed.** If the known SHA vanished from the
  page, the bot treats the whole page as new (bounded by `MAX_EVENTS_PER_POLL`)
  instead of going silent or replaying all of history.
- **Sends are serialised** by a mutex with a `TELEGRAM_SEND_INTERVAL` pause;
  Telegram rate-limits messages into one chat, and parallel sends hit `429`
  reliably.
- **A repository error is reported to chat once**, then every 20th failure, so
  the admin topic does not become a stream of identical complaints.
- **A panic in one repository does not take the bot down** — the worker
  recovers and logs it.
- **State does not grow without bound**: old pull requests, issues and tags are
  evicted.

## Development

```bash
make test    # go test -race ./...
make lint    # go vet + gofmt
make cover   # coverage
make build   # binary into bin/
```

The tests include integration ones: a fake GitHub on `httptest` plus a fake
publisher cover seeding, delivery of new commits, `304`, force pushes, the
author ignore list, retry after a failed send, and fan-out to several
recipients including one that is blocked.

## Deployment notes

The container runs as the distroless `nonroot` user (uid 65532), and `/data`
is created in the image with that ownership so the state volume is writable.
If you replace the named volume with a bind mount, chown the host directory to
`65532:65532` or the bot will log `permission denied` on every flush and lose
state across restarts.

## Possible extensions

- GitHub webhooks instead of polling — instant and limit-free, but they need a
  public address; the current design runs from anywhere, including behind NAT.
- GitLab / Gitea: the collector shape in `internal/watcher` is ready for it —
  add a client and a mapping into `model.Event`.
