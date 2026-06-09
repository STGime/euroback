// Content for the #welcome and #rules channels, posted by post-messages.js.
// Edit freely and re-run `node post-messages.js` — it edits its previous
// post in each channel rather than adding a duplicate.
//
// Each entry maps a channel name to a Discord embed. Colors are hex ints.

const EUROBASE_GREEN = 0x2ecc71;
const EUROBASE_BLUE = 0x3498db;

const messages = {
  welcome: {
    embed: {
      color: EUROBASE_GREEN,
      title: "👋 Welcome to Eurobase",
      description: [
        "The **EU-sovereign Backend-as-a-Service** — a Firebase/Supabase",
        "alternative hosted entirely in the EU (Scaleway, France). Auth,",
        "Postgres, storage, edge functions and realtime, with GDPR built in",
        "and no CLOUD Act exposure.",
        "",
        "This is the community hub for builders, questions, and feedback.",
      ].join("\n"),
      fields: [
        {
          name: "🧭 Find your way around",
          value: [
            "• **#introductions** — say hi and tell us what you're building",
            "• **#help** — questions and troubleshooting",
            "• **#showcase** — show off what you shipped on Eurobase",
            "• **#feature-requests** / **#bug-reports** — shape the roadmap",
            "• **#gdpr-compliance** / **#data-residency** — sovereignty talk",
          ].join("\n"),
        },
        {
          name: "🚀 Get started",
          value: [
            "• Console → https://console.eurobase.app",
            "• Read **#rules** before posting",
            "• Grab a role / introduce yourself, then dive in",
          ].join("\n"),
        },
      ],
      footer: { text: "Built in the EU 🇪🇺 · Your data stays in the EU" },
    },
  },

  rules: {
    embed: {
      color: EUROBASE_BLUE,
      title: "📋 Community Rules",
      description: "Keep this a useful, friendly place for everyone building on Eurobase.",
      fields: [
        {
          name: "1 · Be respectful",
          value: "No harassment, hate, or personal attacks. Assume good faith.",
        },
        {
          name: "2 · Stay on topic",
          value: "Use the right channel. Keep #general light; technical Qs go in #help.",
        },
        {
          name: "3 · No spam or unsolicited promotion",
          value: "Share your project in **#showcase**, not via DMs or drive-by links.",
        },
        {
          name: "4 · Never share secrets",
          value: "Don't paste API keys, service keys, tokens, `DATABASE_URL`s, or customer data. Redact before asking for help.",
        },
        {
          name: "5 · Search before asking",
          value: "Check pinned messages and recent history first — it's faster for you too.",
        },
        {
          name: "6 · No illegal or abusive use",
          value: "Don't use Eurobase or this server for anything unlawful or that violates Discord's ToS.",
        },
      ],
      footer: { text: "Breaking these may result in removal. Questions? Ask the team." },
    },
  },
};

module.exports = { messages };
