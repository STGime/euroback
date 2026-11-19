# Eurobase skill: migrate from Supabase

An [Agent Skill](https://docs.claude.com/en/docs/claude-code/skills) that lets Claude Code (or another agent that reads `SKILL.md`) move an existing Supabase project to [Eurobase](https://eurobase.app): schema, users, data, files, edge functions, scheduled jobs, and the app's code.

The agent drives the `eurobase import supabase` CLI in the right order, stops for every decision that is yours (access changes, copying real users, deploying functions, public buckets, switching over), checks each step, and then changes your app from `@supabase/supabase-js` to `@eurobase/sdk`. Your Supabase project is only read.

The same steps, for doing it by hand, are in the Eurobase console → Docs → **Migrating from Supabase, step by step**.

## Install in Claude Code

From your app's repository:

```bash
mkdir -p .claude/skills/eurobase-supabase-migration
curl -fsSL https://raw.githubusercontent.com/STGime/euroback/main/integrations/supabase-migration/SKILL.md \
  -o .claude/skills/eurobase-supabase-migration/SKILL.md
```

Or for all your projects: the same into `~/.claude/skills/eurobase-supabase-migration/`.

Then ask Claude: *"Migrate this Supabase project to Eurobase. Start with a test project."*

## What you need

- The Eurobase CLI 0.6.9 or later, signed in (`eurobase login`).
- `pg_dump` matching your Supabase database's major version.
- In your terminal (never in the chat): `SUPABASE_DB_URL` (Supabase → Connect → Session pooler), and for files `SUPABASE_URL` and `SUPABASE_SERVICE_ROLE_KEY`.

Keep secrets out of the conversation: the agent refers to them by name. If you paste one by mistake, rotate it afterwards.

## License

MIT
