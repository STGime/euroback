		<section id="connect" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">22. Connecting Your IDE &amp; Lovable</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants their AI coding assistant &mdash; and Lovable &mdash; to understand the LexVault schema.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Connect page generates ready-to-use configuration for popular AI coding tools and app builders. It gives your AI assistant context about your project's schema, API endpoints and connection details, and connects it to the <a href="/docs/mcp" class="text-eurobase-600 hover:text-eurobase-700 font-medium">Eurobase MCP server</a>.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Supported tools</h3>

				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h4 class="text-sm font-semibold text-gray-900">One tab per tool</h4>
					</div>
					<div class="p-5 space-y-3">
						{#each [
							{ name: 'Claude Code', text: 'a CLAUDE.md with your schema and API reference, plus the MCP server command' },
							{ name: 'Lovable', text: 'a step-by-step setup with the Eurobase skill, the MCP connector and your app\'s allowed URLs (see below)' },
							{ name: 'Codex', text: 'an AGENTS.md, plus the MCP server entry for ~/.codex/config.toml' },
							{ name: 'Cursor', text: 'a .cursorrules file, plus the MCP server entry for .cursor/mcp.json' },
							{ name: 'Windsurf', text: 'a .windsurfrules file, plus the MCP server entry' },
							{ name: 'Generic', text: 'SDK install, a .env template, the MCP server URL and cURL examples' }
						] as tool}
							<div class="flex items-start gap-2">
								<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
								<p class="text-sm text-gray-700"><strong>{tool.name}</strong> &mdash; {tool.text}</p>
							</div>
						{/each}
					</div>
				</div>

				<p class="text-sm text-gray-700 leading-relaxed">
					Each tab shows a preview of the generated files with copy and download buttons. For the IDE tabs, drop the file into your project root and your AI assistant has full context about your Eurobase project. Every tab except Generic has a button that creates a <a href="/docs/account" class="text-eurobase-600 hover:text-eurobase-700 font-medium">project token</a> for that tool and fills it into the config.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Building a Lovable app on Eurobase</h3>

				<p class="text-sm text-gray-700 leading-relaxed">
					Alex's colleague prototypes the LexVault client portal in <strong>Lovable</strong>. By default Lovable puts the backend on Lovable Cloud (Supabase); with the Eurobase integration the app talks to Eurobase directly from the browser with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code>, so the data stays in the EU. Open <strong>Connect &rarr; Lovable</strong> and follow the six steps &mdash; the values are already filled in for your project:
				</p>

				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="p-5 space-y-4">
						<div>
							<p class="text-sm font-semibold text-gray-900">1. Stop Lovable from creating its own backend</p>
							<p class="text-sm text-gray-700 mt-1">In Lovable: <strong>Connectors &rarr; Cloud &rarr; Manage my agent's permissions</strong> &rarr; set <strong>Enable Cloud</strong> to <strong>Ask each time</strong>, and decline if it asks. Otherwise the first "add a login" prompt puts your users on Lovable Cloud.</p>
						</div>
						<div>
							<p class="text-sm font-semibold text-gray-900">2. Environment</p>
							<p class="text-sm text-gray-700 mt-1">Copy the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.env</code> lines into the Lovable project:</p>
							<pre class="mt-2 rounded-lg bg-gray-900 px-4 py-3 text-xs font-mono text-gray-100 overflow-x-auto">VITE_EUROBASE_URL=https://lexvault.eurobase.app
VITE_EUROBASE_PROJECT_ID=&lt;project id&gt;
VITE_EUROBASE_PUBLIC_KEY=eb_pk_...</pre>
						</div>
						<div>
							<p class="text-sm font-semibold text-gray-900">3. Add the Eurobase skill</p>
							<p class="text-sm text-gray-700 mt-1">The skill teaches Lovable the Eurobase SDK (it's not supabase-js), row-level security, file privacy and where secrets belong. Copy <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SKILL.md</code> into <strong>Project settings &rarr; Knowledge</strong>, or add it to your workspace's skills (workspace owner or admin) so every project can use it. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">sdk-reference.md</code> is the full SDK reference that goes with it.</p>
						</div>
						<div>
							<p class="text-sm font-semibold text-gray-900">4. Connect the Eurobase MCP server</p>
							<p class="text-sm text-gray-700 mt-1">In Lovable: <strong>Connectors &rarr; + &rarr; MCP server</strong>, server URL <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">https://mcp.eurobase.app/mcp</code>, authentication <strong>Bearer token</strong>. Use a project token with <strong>Developer</strong> access, so Lovable can see and create tables in this project and nothing else. Lovable can't edit a connection &mdash; to change the token, delete the connector and add it again.</p>
						</div>
						<div>
							<p class="text-sm font-semibold text-gray-900">5. Allow your app's URLs</p>
							<p class="text-sm text-gray-700 mt-1">Paste the Lovable editor URL (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">lovable.dev/projects/&hellip;</code>), the published URL and any custom domain, then click <strong>Add to allowed URLs</strong>. The console adds every origin &mdash; both preview addresses (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">&lt;id&gt;.lovableproject.com</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">id-preview--&lt;id&gt;.lovable.app</code>), the published app and its <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">preview--</code> address, and your custom domain &mdash; to <strong>Allowed CORS origins</strong>, and each origin plus its sign-in pages (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/verify</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/reset-password</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/magic</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/auth/callback</code>) to <strong>Allowed redirect URLs</strong>. Wildcards like <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">*.lovable.app</code> aren't accepted, since anyone can publish there. Needs the admin role.</p>
						</div>
						<div>
							<p class="text-sm font-semibold text-gray-900">6. Start building</p>
							<p class="text-sm text-gray-700 mt-1">Copy the first prompt, which tells Lovable to use the skill and Eurobase instead of Lovable Cloud, add your public key if it isn't filled in, and add what to build.</p>
						</div>
					</div>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						Remove old entries in <strong>Auth</strong> after renaming or unpublishing a Lovable app &mdash; someone else could later take the old address.
					</p>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						The generated configs include your API URL and public key but <strong>never your secret key</strong>. Never paste a secret key (<code class="rounded bg-eurobase-100 px-1.5 py-0.5 text-xs font-mono text-eurobase-700">eb_sk_…</code>) into Lovable or any other app builder &mdash; anything that needs it belongs in an <a href="/docs/edge-functions" class="font-medium underline">edge function</a>. For local projects, add secret keys to your <code class="rounded bg-eurobase-100 px-1.5 py-0.5 text-xs font-mono text-eurobase-700">.env</code> file manually and make sure it's in your <code class="rounded bg-eurobase-100 px-1.5 py-0.5 text-xs font-mono text-eurobase-700">.gitignore</code>.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/mcp" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: MCP Server &rarr;
				</a>
			</div>
		</section>
