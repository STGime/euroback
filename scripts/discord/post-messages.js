#!/usr/bin/env node
/**
 * Post (or update) the #welcome and #rules messages defined in messages.js.
 *
 * Idempotent: in each target channel it looks for a message it previously
 * authored and EDITS it to the latest content, rather than posting a
 * duplicate. First run sends + pins; later runs edit in place.
 *
 * Usage:
 *   DISCORD_TOKEN=... DISCORD_GUILD_ID=... node post-messages.js
 *
 * The bot needs, in each target channel: View Channel, Send Messages,
 * Read Message History, and (to pin) Manage Messages.
 */

const {
  Client,
  Events,
  GatewayIntentBits,
  ChannelType,
} = require("discord.js");
const { messages } = require("./messages");

const TOKEN = process.env.DISCORD_TOKEN;
const GUILD_ID = process.env.DISCORD_GUILD_ID;

if (!TOKEN || !GUILD_ID) {
  console.error(
    "Missing env vars. Run with:\n" +
      "  DISCORD_TOKEN=<bot token> DISCORD_GUILD_ID=<server id> node post-messages.js"
  );
  process.exit(1);
}

const client = new Client({
  // GuildMessages lets us fetch existing messages to find our prior post.
  intents: [GatewayIntentBits.Guilds, GatewayIntentBits.GuildMessages],
});

async function upsertMessage(channel, embed) {
  // Find a message we authored previously in this channel.
  const recent = await channel.messages.fetch({ limit: 50 });
  const mine = recent.find((m) => m.author.id === client.user.id);

  if (mine) {
    await mine.edit({ embeds: [embed] });
    console.log(`  ~ updated existing message in #${channel.name}`);
    return mine;
  }

  const sent = await channel.send({ embeds: [embed] });
  console.log(`  + posted new message in #${channel.name}`);
  try {
    await sent.pin();
    console.log(`    📌 pinned`);
  } catch {
    console.log(`    (couldn't pin — bot needs Manage Messages; skipping)`);
  }
  return sent;
}

client.once(Events.ClientReady, async () => {
  try {
    const guild = await client.guilds.fetch(GUILD_ID);
    await guild.channels.fetch();
    console.log(`Posting messages in "${guild.name}"\n`);

    for (const [channelName, def] of Object.entries(messages)) {
      const channel = guild.channels.cache.find(
        (c) => c.type === ChannelType.GuildText && c.name === channelName
      );
      if (!channel) {
        console.log(`  ! channel #${channelName} not found — skipping`);
        continue;
      }
      await upsertMessage(channel, def.embed);
    }

    console.log("\nDone.");
  } catch (err) {
    console.error("\nFailed:", err.message);
    process.exitCode = 1;
  } finally {
    client.destroy();
  }
});

client.login(TOKEN);
