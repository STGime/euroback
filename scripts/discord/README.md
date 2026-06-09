# Eurobase Discord server setup

Idempotent script that builds the Eurobase community server — roles,
categories, channels, and private-channel permissions — from `config.js`.

Safe to re-run: existing items are matched by name and skipped, so you can
edit `config.js` and run again to add only what's new.

## 1. Create the server

In Discord, create a new (empty) server you own. Then enable
**Server Settings → Enable Community** so you get the rules-screening and
announcement-channel features referenced below.

## 2. Create a bot and invite it

1. Go to <https://discord.com/developers/applications> → **New Application**.
2. **Bot** tab → **Reset Token** → copy the token (this is `DISCORD_TOKEN`).
3. **OAuth2 → URL Generator**: scope `bot`, permissions
   **Manage Roles** + **Manage Channels**. Open the generated URL and add
   the bot to your server.
4. In **Server Settings → Roles**, drag the bot's role to the **top** of the
   list. A bot cannot create or reorder roles above its own highest role.

## 3. Get the server (guild) ID

Enable **Discord Settings → Advanced → Developer Mode**, then right-click the
server icon → **Copy Server ID** (this is `DISCORD_GUILD_ID`).

## 4. Run

```bash
cd scripts/discord
npm install
DISCORD_TOKEN=your_bot_token DISCORD_GUILD_ID=your_guild_id node setup.js
```

You should see `+ created ...` lines for each role/category/channel, and
`= ... already exists` for anything skipped on a re-run.

## Customizing

Everything lives in `config.js`:

- **roles** — name, color, `hoist` (separate sidebar group), `mentionable`.
  Listed top-to-bottom = highest-to-lowest in Discord.
- **categories** — each has `channels`. Set `private: true` +
  `privateRoles: ["Team"]` to lock a category and its channels to specific
  roles (everyone else can't see them).

Edit, save, re-run. Only new items are created.

## Integrations

Three automations post into the server. Each needs a **channel webhook URL**,
created in Discord once, then stored as a secret.

### Create the webhooks

For each of `#changelog`, `#deploys`, and `#alerts`:
**Edit channel (gear) → Integrations → Webhooks → New Webhook → Copy
Webhook URL.** Name them e.g. `Changelog Bot`, `Deploy Bot`, `Health Bot`.

### Wire them up

| Integration | What fires it | Secret name | Where the secret lives |
|---|---|---|---|
| Release notes → `#changelog` | A published GitHub Release (`.github/workflows/discord-changelog.yml`) | `DISCORD_CHANGELOG_WEBHOOK` | GitHub repo secret |
| Deploy result → `#deploys` | Every `main` deploy, pass or fail (`ci.yml` deploy job) | `DISCORD_DEPLOY_WEBHOOK` | GitHub repo secret |
| Health check → `#alerts` | `health-monitor` CronJob every 5 min (`deploy/k8s/health-monitor-cronjob.yaml`) | `DISCORD_ALERTS_WEBHOOK` | `eurobase-secrets` k8s Secret |

**GitHub secrets** (changelog + deploys): repo **Settings → Secrets and
variables → Actions → New repository secret**. Both workflows no-op safely if
their secret is missing, so you can add one without the other.

**k8s secret** (alerts) — add the webhook to the existing Secret:

```bash
kubectl -n eurobase patch secret eurobase-secrets --type merge \
  -p '{"stringData":{"DISCORD_ALERTS_WEBHOOK":"https://discord.com/api/webhooks/..."}}'
```

(or set `DISCORD_ALERTS_WEBHOOK` before running `deploy/create-secrets.sh` on a
fresh cluster). The CronJob is applied automatically on the next `main` deploy;
to apply it now: `kubectl apply -f deploy/k8s/health-monitor-cronjob.yaml`.

The monitor probes `https://api.eurobase.app/health` 3× before alerting (so a
single blip doesn't page). Change `HEALTH_URL` or the `schedule` in the
manifest to adjust. Without the secret key, it still logs failures — it just
doesn't post to Discord.

## Notes

- The token is a secret — never commit it. Pass it via env var as shown.
- This script does not set up Community-only channel types (rules screening,
  announcement/news channels) — those are toggled in the Discord UI after
  enabling Community mode.
- Ideas to wire up later: pipe GitHub releases into `#changelog`, post deploy
  notifications into `#deploys`, and a health-check feed into `#alerts`.
