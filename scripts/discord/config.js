// Eurobase Discord server blueprint.
// Edit this file to change the structure, then re-run `node setup.js`.
// The setup script is idempotent: existing roles/categories/channels are
// matched by name and skipped, so you can tweak and re-run safely.

// Discord role colors (hex). null = default grey.
const COLORS = {
  team: 0x2ecc71, // green
  maintainer: 0x3498db, // blue
  contributor: 0x9b59b6, // purple
  verifiedDev: 0x1abc9c, // teal
  member: null,
  bot: 0x95a5a6, // grey
};

// Roles are created top-to-bottom; Discord stacks the FIRST as highest.
// `hoist` shows the role as a separate group in the member sidebar.
const roles = [
  { name: "Team", color: COLORS.team, hoist: true, mentionable: true },
  { name: "Maintainer", color: COLORS.maintainer, hoist: true, mentionable: true },
  { name: "Contributor", color: COLORS.contributor, hoist: true, mentionable: true },
  { name: "Verified Dev", color: COLORS.verifiedDev, hoist: true, mentionable: false },
  { name: "Bot", color: COLORS.bot, hoist: false, mentionable: false },
  { name: "Member", color: COLORS.member, hoist: false, mentionable: false },
];

// Each category has channels. `private: true` hides the channel/category
// from @everyone and grants view access to the listed roles only.
// `topic` sets the channel description.
const categories = [
  {
    name: "👋 Welcome",
    channels: [
      { name: "welcome", topic: "Start here. What Eurobase is and how this server works." },
      { name: "rules", topic: "Community guidelines. Read before posting." },
      { name: "announcements", topic: "Official Eurobase announcements." },
      { name: "changelog", topic: "Release notes — can be auto-posted from CI." },
    ],
  },
  {
    name: "💬 Community",
    channels: [
      { name: "general", topic: "General chat about Eurobase and EU-sovereign infra." },
      { name: "introductions", topic: "Say hi — who you are and what you're building." },
      { name: "showcase", topic: "Show off what you've built on Eurobase." },
      { name: "off-topic", topic: "Everything else." },
    ],
  },
  {
    name: "🛠️ Support",
    channels: [
      { name: "help", topic: "Ask for help. Search before posting." },
      { name: "self-hosting", topic: "Running Eurobase on your own EU infrastructure." },
      { name: "database-rls", topic: "Postgres, RLS, set_tenant_id, migrations." },
      { name: "edge-functions", topic: "Edge functions runner, deployment, debugging." },
      { name: "auth", topic: "Email/password, magic links, OAuth, phone OTP." },
      { name: "storage", topic: "Storage objects, signed URLs, uploads." },
    ],
  },
  {
    name: "🇪🇺 Sovereignty",
    channels: [
      { name: "gdpr-compliance", topic: "GDPR, DPAs, sub-processors, audit logging." },
      { name: "data-residency", topic: "EU data residency, Scaleway, no CLOUD Act exposure." },
    ],
  },
  {
    name: "🧑‍💻 Dev",
    channels: [
      { name: "feature-requests", topic: "Request and discuss new features." },
      { name: "bug-reports", topic: "Report bugs. Include repro steps." },
      { name: "api-feedback", topic: "Feedback on the SDK and REST API surface." },
      { name: "roadmap", topic: "What's coming next." },
    ],
  },
  {
    name: "🔒 Internal",
    private: true,
    privateRoles: ["Team"],
    channels: [
      { name: "team", topic: "Team-only chat." },
      { name: "alerts", topic: "Ops alerts — can be wired to monitoring." },
      { name: "deploys", topic: "Deploy notifications from CI/CD." },
    ],
  },
];

module.exports = { roles, categories };
