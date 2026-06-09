#!/usr/bin/env node
/**
 * Idempotent Eurobase Discord server setup.
 *
 * Creates the roles, categories, and channels defined in config.js.
 * Matches existing objects by name and skips them, so it is safe to
 * re-run after editing config.js — only new items are created.
 *
 * Usage:
 *   DISCORD_TOKEN=... DISCORD_GUILD_ID=... node setup.js
 *
 * The bot must already be invited to the target server with the
 * "Manage Roles" and "Manage Channels" permissions, and its highest
 * role must sit ABOVE any role it needs to create/reorder.
 */

const {
  Client,
  GatewayIntentBits,
  ChannelType,
  PermissionFlagsBits,
} = require("discord.js");
const { roles, categories } = require("./config");

const TOKEN = process.env.DISCORD_TOKEN;
const GUILD_ID = process.env.DISCORD_GUILD_ID;

if (!TOKEN || !GUILD_ID) {
  console.error(
    "Missing env vars. Run with:\n" +
      "  DISCORD_TOKEN=<bot token> DISCORD_GUILD_ID=<server id> node setup.js"
  );
  process.exit(1);
}

const client = new Client({ intents: [GatewayIntentBits.Guilds] });

// Find an existing object in a manager's cache by case-insensitive name.
const byName = (cache, name) =>
  cache.find((o) => o.name.toLowerCase() === name.toLowerCase());

async function ensureRoles(guild) {
  const created = {};
  // Create in reverse so the first config entry ends up highest in the list.
  for (const def of [...roles].reverse()) {
    let role = byName(guild.roles.cache, def.name);
    if (role) {
      console.log(`  = role "${def.name}" already exists`);
    } else {
      role = await guild.roles.create({
        name: def.name,
        color: def.color ?? undefined,
        hoist: !!def.hoist,
        mentionable: !!def.mentionable,
        reason: "Eurobase server setup",
      });
      console.log(`  + created role "${def.name}"`);
    }
    created[def.name] = role;
  }
  return created;
}

function privateOverwrites(guild, allowedRoles, roleMap) {
  const overwrites = [
    { id: guild.roles.everyone.id, deny: [PermissionFlagsBits.ViewChannel] },
  ];
  for (const name of allowedRoles || []) {
    const role = roleMap[name];
    if (role) {
      overwrites.push({
        id: role.id,
        allow: [PermissionFlagsBits.ViewChannel],
      });
    }
  }
  return overwrites;
}

async function ensureCategories(guild, roleMap) {
  for (const cat of categories) {
    let category = guild.channels.cache.find(
      (c) => c.type === ChannelType.GuildCategory && c.name === cat.name
    );

    const overwrites = cat.private
      ? privateOverwrites(guild, cat.privateRoles, roleMap)
      : undefined;

    if (category) {
      console.log(`= category "${cat.name}" already exists`);
    } else {
      category = await guild.channels.create({
        name: cat.name,
        type: ChannelType.GuildCategory,
        permissionOverwrites: overwrites,
        reason: "Eurobase server setup",
      });
      console.log(`+ created category "${cat.name}"`);
    }

    for (const ch of cat.channels) {
      const existing = guild.channels.cache.find(
        (c) =>
          c.type === ChannelType.GuildText &&
          c.name === ch.name &&
          c.parentId === category.id
      );
      if (existing) {
        console.log(`  = channel #${ch.name} already exists`);
        continue;
      }
      // Don't set overwrites on the child explicitly — a newly created
      // channel automatically syncs to its parent category's permissions.
      // Setting them at create time can trigger a 50013 "Missing Permissions"
      // error, while the category-level overwrite (above) works fine. The
      // child inherits the private category's lock via sync.
      await guild.channels.create({
        name: ch.name,
        type: ChannelType.GuildText,
        parent: category.id,
        topic: ch.topic,
        reason: "Eurobase server setup",
      });
      console.log(`  + created channel #${ch.name}`);
    }
  }
}

client.once("clientReady", async () => {
  try {
    const guild = await client.guilds.fetch(GUILD_ID);
    console.log(`Setting up "${guild.name}"\n`);

    console.log("Roles:");
    const roleMap = await ensureRoles(guild);

    console.log("\nChannels:");
    await ensureCategories(guild, roleMap);

    console.log("\nDone. Server structure is in place.");
  } catch (err) {
    console.error("\nSetup failed:", err.message);
    process.exitCode = 1;
  } finally {
    client.destroy();
  }
});

client.login(TOKEN);
