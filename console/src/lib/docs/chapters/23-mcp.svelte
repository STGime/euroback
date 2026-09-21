<script lang="ts">
	let copiedId = $state('');
	async function copyCode(code: string, id: string) {
		try {
			await navigator.clipboard.writeText(code);
			copiedId = id;
			setTimeout(() => { if (copiedId === id) copiedId = ''; }, 1500);
		} catch {
			// clipboard unavailable — silently ignore
		}
	}
</script>

		<section id="mcp" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">23. MCP Server</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants their AI assistant to actually <em>do things</em> in LexVault &mdash; list users, run a SELECT, rotate a Vault secret &mdash; not just read schema docs.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The configs in <a href="/docs/connect" class="text-eurobase-600 hover:underline cursor-pointer">section 22</a> teach an AI assistant <em>about</em> your project. The MCP server lets it <em>operate</em> on your project. Eurobase ships a hosted <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener" class="text-eurobase-600 hover:underline">Model Context Protocol</a> server at <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">https://mcp.eurobase.app/mcp</code> that exposes the platform API as tool calls.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">What it can do</h3>
				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="p-5 grid grid-cols-1 sm:grid-cols-2 gap-3 text-sm text-gray-700">
						<div><strong>Projects</strong> &mdash; list and inspect your Eurobase projects</div>
						<div><strong>Database</strong> &mdash; list tables, describe schema, run SQL, create tables</div>
						<div><strong>Auth</strong> &mdash; list end-users registered in a project</div>
						<div><strong>Storage</strong> &mdash; list files, generate signed download URLs</div>
						<div><strong>Vault</strong> &mdash; list, get, and set encrypted secrets</div>
						<div><strong>Functions</strong> &mdash; list and invoke edge functions</div>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Authentication: Personal Access Tokens</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The MCP server authenticates Bearer-style with a <strong>Personal Access Token (PAT)</strong>. Mint one in <a href="/account" class="text-eurobase-700 hover:underline">Account &rarr; Personal Access Tokens</a>: name it ("my laptop", "ci-prod"), optionally set an expiry, and copy the plaintext token (shown once on creation).
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					PATs are deliberately scoped down from a full console login:
				</p>
				<ul class="text-sm text-gray-700 space-y-1 ml-4 list-disc">
					<li><strong>Authenticate as you</strong> across every project you own or are a member of &mdash; full SDK + platform-API surface.</li>
					<li><strong>Never carry superadmin rights</strong>, even if the underlying account has them. The platform admin endpoints (allowlist, cross-tenant project list) are unreachable via PAT.</li>
					<li><strong>Cannot mint other tokens</strong> &mdash; sign in to the console for that. Limits the blast radius of a leaked PAT.</li>
					<li><strong>Cannot change passwords or delete the account.</strong></li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">
					Revoke a PAT any time from the same screen. Tokens are stored as SHA-256 hashes &mdash; the plaintext exists only on your machine after creation.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Setup</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The Connect page generates the right snippet for each IDE (Claude Code, Codex, Cursor, Windsurf). For Claude Code the one-liner is:
				</p>
				<div class="relative">
					<pre class="rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">export EUROBASE_PAT=eb_pat_...   # from Account &rarr; Personal Access Tokens
claude mcp add --transport http eurobase https://mcp.eurobase.app/mcp \
  --header "Authorization: Bearer $EUROBASE_PAT"</pre>
					<button
						onclick={() => copyCode('export EUROBASE_PAT=eb_pat_...\nclaude mcp add --transport http eurobase https://mcp.eurobase.app/mcp \\\n  --header "Authorization: Bearer $EUROBASE_PAT"', 'mcp-claude-cli')}
						class="absolute top-2 right-2 rounded-md bg-gray-800 hover:bg-gray-700 text-gray-300 px-2 py-1 text-xs cursor-pointer"
					>{copiedId === 'mcp-claude-cli' ? 'Copied!' : 'Copy'}</button>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">
					After this, Alex can ask Claude Code things like <em>"how many active LexVault users signed up this week?"</em> and it will run the SELECT itself.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Sovereignty</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The MCP server runs in the same Scaleway Paris cluster as the rest of the platform. Tool calls never leave EU infrastructure &mdash; the only data that traverses your AI vendor is whatever the model itself sees in the conversation.
				</p>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/account" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Your Account &rarr;
				</a>
			</div>
		</section>
