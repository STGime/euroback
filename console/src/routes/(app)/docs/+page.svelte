<script lang="ts">
	import { onMount } from 'svelte';
	import { browser } from '$app/environment';

	// ---- TOC & Scrollspy ----

	const chapters = [
		{ id: 'welcome', label: 'Welcome' },
		{ id: 'signup', label: '1. Signing Up' },
		{ id: 'create-project', label: '2. Creating Your First Project' },
		{ id: 'dashboard', label: '3. The Project Dashboard' },
		{ id: 'database', label: '4. Building the Database' },
		{ id: 'storage', label: '5. File Storage' },
		{ id: 'auth', label: '6. Authentication Setup' },
		{ id: 'rate-limits', label: '7. Rate Limits' },
		{ id: 'custom-smtp', label: '8. Custom SMTP' },
		{ id: 'users', label: '9. Managing End Users' },
		{ id: 'api', label: '10. Exploring the API' },
		{ id: 'webhooks', label: '11. Webhooks' },
		{ id: 'rls', label: '12. Row-Level Security' },
		{ id: 'vault', label: '13. Vault (Secrets)' },
		{ id: 'cron', label: '14. Scheduled Jobs' },
		{ id: 'edge-functions', label: '15. Edge Functions' },
		{ id: 'logs', label: '16. Monitoring with Logs' },
		{ id: 'compliance', label: '17. Compliance & Audit Log' },
		{ id: 'settings', label: '18. Project Settings' },
		{ id: 'team', label: '19. Team Collaboration' },
		{ id: 'cli', label: '20. CLI Tool' },
		{ id: 'migrations', label: '21. Schema Migrations' },
		{ id: 'connect', label: '22. Connecting Your IDE' },
		{ id: 'mcp', label: '23. MCP Server' },
		{ id: 'account', label: '24. Your Account' },
		{ id: 'next', label: "What's Next" }
	];

	let activeId = $state('welcome');
	let tocOpen = $state(false);

	onMount(() => {
		const observer = new IntersectionObserver(
			(entries) => {
				for (const entry of entries) {
					if (entry.isIntersecting) {
						activeId = entry.target.id;
					}
				}
			},
			{ rootMargin: '-80px 0px -60% 0px', threshold: 0 }
		);

		for (const ch of chapters) {
			const el = document.getElementById(ch.id);
			if (el) observer.observe(el);
		}

		return () => observer.disconnect();
	});

	function scrollTo(id: string) {
		if (!browser) return;
		const el = document.getElementById(id);
		if (el) {
			el.scrollIntoView({ behavior: 'smooth', block: 'start' });
			tocOpen = false;
		}
	}

	// ---- Copy button ----

	let copiedId = $state('');

	async function copyCode(code: string, id: string) {
		try {
			await navigator.clipboard.writeText(code);
			copiedId = id;
			setTimeout(() => { if (copiedId === id) copiedId = ''; }, 1500);
		} catch {
			// silently fail
		}
	}
</script>

<svelte:head>
	<title>Documentation - Eurobase Console</title>
</svelte:head>

<div class="flex gap-8 max-w-7xl mx-auto">
	<!-- Desktop TOC -->
	<nav class="hidden lg:block w-56 shrink-0">
		<div class="sticky top-8">
			<h3 class="text-xs font-semibold uppercase tracking-wider text-gray-400 mb-3">Contents</h3>
			<ul class="space-y-0.5">
				{#each chapters as ch}
					<li>
						<button
							onclick={() => scrollTo(ch.id)}
							class="w-full text-left text-sm py-1 pl-3 border-l-2 transition-colors cursor-pointer {activeId === ch.id ? 'text-eurobase-700 font-medium border-eurobase-600' : 'text-gray-500 border-transparent hover:text-gray-700 hover:border-gray-300'}"
						>
							{ch.label}
						</button>
					</li>
				{/each}
			</ul>
		</div>
	</nav>

	<!-- Mobile TOC dropdown -->
	<div class="lg:hidden fixed top-20 right-4 z-30">
		<button
			onclick={() => tocOpen = !tocOpen}
			class="flex items-center gap-1.5 rounded-lg bg-white border border-gray-200 px-3 py-2 text-xs font-medium text-gray-700 shadow-sm cursor-pointer"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6.75h16.5M3.75 12h16.5m-16.5 5.25h16.5" />
			</svg>
			Contents
		</button>
		{#if tocOpen}
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="fixed inset-0 z-40" onclick={() => tocOpen = false} onkeydown={() => {}}></div>
			<div class="absolute right-0 mt-1 w-64 rounded-xl bg-white border border-gray-200 shadow-lg z-50 py-2 max-h-[70vh] overflow-y-auto">
				{#each chapters as ch}
					<button
						onclick={() => scrollTo(ch.id)}
						class="w-full text-left px-4 py-1.5 text-sm cursor-pointer {activeId === ch.id ? 'text-eurobase-700 font-medium bg-eurobase-50' : 'text-gray-600 hover:bg-gray-50'}"
					>
						{ch.label}
					</button>
				{/each}
			</div>
		{/if}
	</div>

	<!-- Content -->
	<div class="flex-1 min-w-0 max-w-3xl space-y-16 pb-24">

		<!-- ======================= WELCOME ======================= -->
		<section id="welcome" class="scroll-mt-20">
			<h1 class="text-3xl font-bold text-gray-900 mb-2">Documentation</h1>
			<p class="text-base text-gray-600 mb-6">A guided tour of Eurobase through the eyes of a real project.</p>

			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<p class="text-sm text-gray-700 leading-relaxed mb-4">
					Meet <strong>Alex</strong>, a full-stack developer building <strong>LexVault</strong> &mdash; a document
					management SaaS for European law firms. Alex's clients handle sensitive legal documents and need
					rock-solid GDPR compliance with zero US cloud exposure.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mb-4">
					This guide follows Alex from first sign-up to a fully connected application. Each chapter matches a
					section of the Eurobase console, so you can follow along with your own project.
				</p>
				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						Estimated read time: <strong>~15 minutes.</strong> Every feature is covered &mdash; skip ahead with the table of contents if you're looking for something specific.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('signup')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Start with Chapter 1: Signing Up &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 1. SIGNING UP ======================= -->
		<section id="signup" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">1. Signing Up</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex has heard about Eurobase and navigates to the login page.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase uses email-and-password authentication by default. You can also enable social login (Google, GitHub, LinkedIn, Apple) &mdash; providers are used only to verify identity; all user records stay within EU infrastructure.
				</p>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">1</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Navigate to the login page</p>
						<p class="text-sm text-gray-600">Open your browser and go to the Eurobase console URL. You'll see the sign-in form.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">2</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Toggle to "Sign Up"</p>
						<p class="text-sm text-gray-600">Below the form, click the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Don't have an account? Sign up</code> link to switch to registration mode.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">3</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Enter your email and password</p>
						<p class="text-sm text-gray-600">Choose a strong password (minimum 8 characters). Hit <strong>Sign Up</strong> and you're in.</p>
					</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						Your account is a <strong>platform account</strong> &mdash; it's separate from end-user accounts in your projects. One platform account can manage many projects.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('create-project')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Creating Your First Project &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 2. CREATING YOUR FIRST PROJECT ======================= -->
		<section id="create-project" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">2. Creating Your First Project</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex is signed in and ready to set up LexVault's backend.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					After signing in for the first time, you'll land on the onboarding wizard. Returning users can create additional projects from the <strong>Projects</strong> page.
				</p>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">1</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Name your project</p>
						<p class="text-sm text-gray-600">Alex types <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">LexVault</code>. The slug <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">lexvault</code> is generated automatically.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">2</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Choose a region</p>
						<p class="text-sm text-gray-600">All regions are within the EU. Alex selects <strong>Paris (fr-par)</strong>. Your database, object storage, and compute are provisioned there.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">3</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Configure authentication</p>
						<p class="text-sm text-gray-600">Toggle email/password auth on (default). Set password requirements and session duration.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">4</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Get your API keys</p>
						<p class="text-sm text-gray-600">On completion, you receive a <strong>public key</strong> (safe for client-side) and a <strong>secret key</strong> (server-side only).</p>
					</div>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>Save your secret key now.</strong> It is shown only once. If you lose it, you'll need to regenerate it from Project Settings.
					</p>
				</div>

				<!-- SDK init snippet -->
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("import { createClient } from '@eurobase/sdk'\n\nconst eb = createClient({\n  url: 'https://lexvault.eurobase.app',\n  apiKey: process.env.EUROBASE_PUBLIC_KEY\n})", 'sdk-init')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-init' ? 'Copied!' : 'Copy'}
					</button>
					<pre>import {'{'} createClient {'}'} from '@eurobase/sdk'

const eb = createClient({'{'}
  url: 'https://lexvault.eurobase.app',
  apiKey: process.env.EUROBASE_PUBLIC_KEY
{'}'})</pre>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('dashboard')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: The Project Dashboard &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 3. THE PROJECT DASHBOARD ======================= -->
		<section id="dashboard" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">3. The Project Dashboard</h2>
			<p class="text-sm italic text-gray-500 mb-4">LexVault is created. Alex lands on the project overview.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The dashboard is your project's home screen. It shows key stats at a glance and provides quick actions to jump into common tasks.
				</p>

				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h3 class="text-sm font-semibold text-gray-900">What you'll see</h3>
					</div>
					<div class="p-5 space-y-3">
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Stats cards</strong> &mdash; table count, storage used, and API request count</p>
						</div>
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Quick actions</strong> &mdash; buttons to open the Database, Storage, and Settings pages</p>
						</div>
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Get started guide</strong> &mdash; copy-paste code snippets for SDK install, init, first query, and auth</p>
						</div>
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Project info</strong> &mdash; name, slug, region, and API URL</p>
						</div>
					</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						The project sidebar (visible on the left) gives you quick access to every section covered in this guide: Database, Storage, Auth, Users, API, Webhooks, Logs, Settings, and Connect.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('database')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Building the Database &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 4. BUILDING THE DATABASE ======================= -->
		<section id="database" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">4. Building the Database</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">clients</code> table for LexVault's law firm customers.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Database section is where you design your schema and manage data. Each project gets its own
					isolated PostgreSQL database with full SQL access.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Creating a table</h3>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">1</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Click "New Table"</p>
						<p class="text-sm text-gray-600">In the Database view, hit the button in the top-right corner. Enter a table name &mdash; Alex types <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">clients</code>.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">2</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Add columns</p>
						<p class="text-sm text-gray-600">Use the column editor to define your schema. Supported types include <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">text</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">integer</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">boolean</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">timestamp</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">uuid</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">jsonb</code>, and more.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">3</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Set constraints</p>
						<p class="text-sm text-gray-600">Mark columns as primary key, not-null, unique, or add default values. Foreign keys link tables together.</p>
					</div>
				</div>

				<!-- SQL example -->
				<p class="text-sm text-gray-700 mt-2">Or use the <strong>SQL Runner</strong> (Database &rarr; SQL tab) to create tables with raw SQL:</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("CREATE TABLE clients (\n  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),\n  name       text NOT NULL,\n  email      text UNIQUE NOT NULL,\n  firm_name  text,\n  plan       text DEFAULT 'free',\n  created_at timestamptz DEFAULT now()\n);", 'create-table')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'create-table' ? 'Copied!' : 'Copy'}
					</button>
					<pre>CREATE TABLE clients (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  email      text UNIQUE NOT NULL,
  firm_name  text,
  plan       text DEFAULT 'free',
  created_at timestamptz DEFAULT now()
);</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Working with data</h3>

				<p class="text-sm text-gray-700 leading-relaxed">
					Once your table exists, you can browse, create, edit, and delete rows directly in the console:
				</p>

				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Add rows</strong> &mdash; click "Insert Row" to open the inline editor</li>
					<li><strong>Edit cells</strong> &mdash; click any cell to edit it in place</li>
					<li><strong>Filter &amp; sort</strong> &mdash; use the toolbar to filter by column values and sort ascending/descending</li>
					<li><strong>Pagination</strong> &mdash; navigate large tables with page controls</li>
					<li><strong>Bulk delete</strong> &mdash; select multiple rows with checkboxes and delete them at once</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Migration history</h3>

				<p class="text-sm text-gray-700 leading-relaxed">
					Database &rarr; <strong>Migration History</strong> shows a timeline of schema changes &mdash; tables created, columns added, indexes dropped, and so on. Each entry records the action, target table/column, and timestamp.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					The platform tracks DDL from any path that goes through the gateway: the console (Table Editor, SQL Runner), the SDK (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.db.sql()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.schema.createTable()</code>), and MCP tools (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">runSQL</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">createTable</code>).
				</p>
				<div class="mt-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3">
					<p class="text-sm font-medium text-amber-900">Limitation: direct database access is not audited</p>
					<p class="mt-1 text-xs text-amber-800 leading-relaxed">
						Migration History tracks DDL that reaches the platform via the gateway. Schema changes made through a <strong>direct database connection</strong> &mdash; for example, <code class="rounded bg-amber-100 px-1 py-0.5 text-[11px] font-mono">psql</code> connected to your <code class="rounded bg-amber-100 px-1 py-0.5 text-[11px] font-mono">DATABASE_URL</code>, an ORM/migration tool running on your own infrastructure, or the Scaleway database console &mdash; <strong>do not appear</strong> in the history. They are real changes against your schema and your code/queries should still work, but the audit trail won't show them.
					</p>
					<p class="mt-2 text-xs text-amber-800 leading-relaxed">
						This is a deliberate trade-off: capturing every change uniformly would require Postgres superuser privileges, which managed-Postgres providers (Scaleway, RDS, etc.) reserve for themselves. If you need a complete audit trail, prefer the gateway-mediated paths (SQL Runner / SDK / MCP) over direct DB access for any schema-changing operation.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Using the SDK</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("// Insert a client\nconst { data, error } = await eb.db\n  .from('clients')\n  .insert({ name: 'Acme Legal', email: 'info@acmelegal.eu', firm_name: 'Acme Legal GmbH' })\n\n// Read all clients\nconst { data: clients } = await eb.db\n  .from('clients')\n  .select('*')\n\n// Update a client by ID\nawait eb.db\n  .from('clients')\n  .update('some-uuid', { plan: 'pro' })\n\n// Delete a client by ID\nawait eb.db\n  .from('clients')\n  .delete('some-uuid')", 'sdk-crud')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-crud' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// Insert a client
const {'{'} data, error {'}'} = await eb.db
  .from('clients')
  .insert({'{'} name: 'Acme Legal', email: 'info@acmelegal.eu', firm_name: 'Acme Legal GmbH' {'}'})

// Read all clients
const {'{'} data: clients {'}'} = await eb.db
  .from('clients')
  .select('*')

// Update a client by ID
await eb.db
  .from('clients')
  .update('some-uuid', {'{'} plan: 'pro' {'}'})

// Delete a client by ID
await eb.db
  .from('clients')
  .delete('some-uuid')</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Schema management</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The <strong>Schema</strong> tab gives you a visual overview of all tables, their columns, types, and relationships. Use it to add indexes, manage foreign keys, and review your data model at a glance.
				</p>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('storage')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: File Storage &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 5. FILE STORAGE ======================= -->
		<section id="storage" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">5. File Storage</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to store legal documents that clients upload.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Each project gets a dedicated Scaleway Object Storage bucket in your chosen EU region. The Storage page in the console gives you a file manager to upload, organize, and preview files.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Console features</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Drag-and-drop upload</strong> &mdash; drop files or click to browse</li>
					<li><strong>Folders</strong> &mdash; organize files with a folder structure and navigate with breadcrumbs</li>
					<li><strong>Preview</strong> &mdash; images, PDFs, and text files render inline</li>
					<li><strong>Signed URLs</strong> &mdash; generate time-limited download links for any file</li>
					<li><strong>List &amp; grid view</strong> &mdash; toggle between compact list and visual grid</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Upload via SDK</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("// Upload a file\nconst file = document.getElementById('fileInput').files[0]\nconst { key, error } = await eb.storage\n  .upload('contracts/nda-acme.pdf', file)\n\n// Get a signed download URL (1 hour expiry)\nconst { url } = await eb.storage\n  .createSignedUrl('contracts/nda-acme.pdf', 'download', { expiresIn: 3600 })\n\n// List files in a folder\nconst { objects } = await eb.storage\n  .list({ prefix: 'contracts/' })\n\n// Download a file\nconst blob = await eb.storage.download('contracts/nda-acme.pdf')\n\n// Delete a file\nawait eb.storage.remove('contracts/nda-acme.pdf')", 'sdk-storage')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-storage' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// Upload a file
const file = document.getElementById('fileInput').files[0]
const {'{'} key, error {'}'} = await eb.storage
  .upload('contracts/nda-acme.pdf', file)

// Get a signed download URL (1 hour expiry)
const {'{'} url {'}'} = await eb.storage
  .createSignedUrl('contracts/nda-acme.pdf', 'download', {'{'} expiresIn: 3600 {'}'})

// List files in a folder
const {'{'} objects {'}'} = await eb.storage
  .list({'{'} prefix: 'contracts/' {'}'})

// Download a file
const blob = await eb.storage.download('contracts/nda-acme.pdf')

// Delete a file
await eb.storage.remove('contracts/nda-acme.pdf')</pre>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						All files are stored in Scaleway Object Storage in France. No data ever touches US-based cloud providers.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('auth')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Authentication Setup &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 6. AUTHENTICATION SETUP ======================= -->
		<section id="auth" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">6. Authentication Setup</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to let law firm employees sign into LexVault securely.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Auth settings page lets you configure how end-users authenticate in your application. Eurobase provides a built-in email/password auth system &mdash; no external providers required.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Auth methods</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Email + Password</strong> &mdash; traditional sign-up and sign-in with email and password</li>
					<li><strong>Magic Links</strong> &mdash; passwordless sign-in via a one-time email link (no password needed)</li>
					<li><strong>Phone (SMS OTP)</strong> &mdash; sign in with phone number via a 6-digit SMS code (EU-sovereign SMS via GatewayAPI)</li>
					<li><strong>Passkeys</strong> &mdash; coming soon (WebAuthn / FaceID / fingerprint)</li>
					<li><strong>Social Login</strong> &mdash; Google, GitHub, LinkedIn, Apple (configure in Auth settings)</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Configuration options</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Password rules</strong> &mdash; set minimum length (8&ndash;128 characters)</li>
					<li><strong>Email confirmation</strong> &mdash; require users to verify their email before signing in</li>
					<li><strong>Session duration</strong> &mdash; how long access tokens remain valid (1h to 30 days)</li>
					<li><strong>Redirect URLs</strong> &mdash; whitelist URLs your app can redirect to after auth callbacks</li>
					<li><strong>CORS origins</strong> &mdash; browser origins (scheme://host[:port]) allowed to call this project's API. Add your dev and production app origins. Eurobase platform origins (<code class="bg-gray-100 border border-gray-200 rounded px-1">*.eurobase.app</code>) are always allowed.</li>
				</ul>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>Email confirmation</strong> requires a transactional email provider (Scaleway TEM). Until TEM is configured for your project, email-dependent features like signup verification, password reset, and email change will not be available.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">SDK auth flow</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("// Sign up a new user\nconst { user, error } = await eb.auth.signUp({\n  email: 'lawyer@acmelegal.eu',\n  password: 'SecurePass123!'\n})\n\n// Sign in\nconst { session, error: signInError } = await eb.auth.signIn({\n  email: 'lawyer@acmelegal.eu',\n  password: 'SecurePass123!'\n})\n\n// Listen for auth state changes\neb.auth.onAuthStateChange((event, session) => {\n  console.log('Auth event:', event)  // 'SIGNED_IN', 'SIGNED_OUT', 'TOKEN_REFRESHED'\n  console.log('Session:', session)\n})\n\n// Sign out\nawait eb.auth.signOut()", 'sdk-auth')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-auth' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// Sign up a new user
const {'{'} user, error {'}'} = await eb.auth.signUp({'{'}
  email: 'lawyer@acmelegal.eu',
  password: 'SecurePass123!'
{'}'})

// Sign in
const {'{'} session, error: signInError {'}'} = await eb.auth.signIn({'{'}
  email: 'lawyer@acmelegal.eu',
  password: 'SecurePass123!'
{'}'})

// Listen for auth state changes
eb.auth.onAuthStateChange((event, session) => {'{'}\
  console.log('Auth event:', event)  // 'SIGNED_IN', 'SIGNED_OUT', 'TOKEN_REFRESHED'
  console.log('Session:', session)
{'}'})

// Sign out
await eb.auth.signOut()</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Magic Links (passwordless)</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Magic links let users sign in without a password. They enter their email, receive a link, and click it to sign in. The link expires after 15 minutes and can only be used once. Email is automatically verified on first magic link sign-in.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Enable magic links in <strong>Auth &rarr; Settings &rarr; Magic Links</strong> toggle. Both email/password and magic links can be active at the same time.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-3">
					<button
						onclick={() => copyCode("// 1. Send magic link to user's email\nawait eb.auth.requestMagicLink('user@example.com')\n\n// 2. User clicks the link in their inbox\n// Your app receives the token via URL: /auth/callback?token=abc123\nconst token = new URL(location.href).searchParams.get('token')\n\n// 3. Exchange the token for a session\nconst { data, error } = await eb.auth.signInWithMagicLink(token)\n// data.access_token, data.user.email — user is now signed in", 'sdk-magic')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-magic' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// 1. Send magic link to user's email
await eb.auth.requestMagicLink('user@example.com')

// 2. User clicks the link in their inbox
// Your app receives the token via URL: /auth/callback?token=abc123
const token = new URL(location.href).searchParams.get('token')

// 3. Exchange the token for a session
const {'{'} data, error {'}'} = await eb.auth.signInWithMagicLink(token)
// data.access_token, data.user.email — user is now signed in</pre>
				</div>

				<div class="mt-3 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">How it works under the hood</p>
					<ol class="text-xs text-gray-600 space-y-1 ml-4 list-decimal">
						<li><code class="bg-white border border-gray-200 rounded px-1">requestMagicLink</code> sends a POST to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/request-magic-link</code> with the email</li>
						<li>The server generates a one-time token (32 random bytes), stores a SHA-256 hash in the database, and emails the raw token in a link</li>
						<li>The user clicks the link, your app extracts the <code class="bg-white border border-gray-200 rounded px-1">token</code> query parameter</li>
						<li><code class="bg-white border border-gray-200 rounded px-1">signInWithMagicLink</code> sends the token to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/signin-magic-link</code></li>
						<li>The server verifies the token (not expired, not used), marks it as consumed, and returns a JWT + refresh token</li>
					</ol>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Phone Auth (SMS OTP)</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Phone auth lets users sign in with their phone number instead of an email. They receive a 6-digit verification code via SMS that expires after 10 minutes. Phone numbers must be in E.164 format (e.g., <code class="bg-gray-100 border border-gray-200 rounded px-1">+33612345678</code>).
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Enable phone auth in <strong>Auth &rarr; Settings &rarr; Phone (SMS OTP)</strong> toggle. The gateway must have <code class="bg-gray-100 border border-gray-200 rounded px-1">GATEWAYAPI_TOKEN</code> configured. SMS is sent via GatewayAPI, an EU-based provider (Denmark) &mdash; no data leaves EU infrastructure.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-3">
					<pre><span class="text-gray-500">// 1. Send OTP to phone</span>
<span class="text-amber-400">POST</span> /v1/auth/phone/send-otp
Body: {'{'}"phone": "+33612345678"{'}'}

<span class="text-gray-500">// 2. Verify code and get session</span>
<span class="text-amber-400">POST</span> /v1/auth/phone/verify
Body: {'{'}"phone": "+33612345678", "code": "123456"{'}'}
<span class="text-gray-500">// Returns: access_token, refresh_token, user</span></pre>
				</div>

				<div class="mt-3 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">How it works</p>
					<ol class="text-xs text-gray-600 space-y-1 ml-4 list-decimal">
						<li>Your app sends the phone number to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/phone/send-otp</code></li>
						<li>Eurobase creates a user (if new) and sends a 6-digit code via SMS</li>
						<li>The user enters the code in your app</li>
						<li>Your app sends the phone + code to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/phone/verify</code></li>
						<li>Eurobase verifies the code, confirms the phone number, and returns JWT tokens</li>
					</ol>
				</div>

				<div class="mt-3 rounded-lg border border-blue-200 bg-blue-50 px-4 py-3">
					<p class="text-xs text-blue-700"><strong>Phone-only users:</strong> Users who sign in with only a phone number are created without an email. They can later add an email via account linking. Phone auth can coexist with email/password and social login.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Social Login (OAuth)</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase supports social login with <strong>Google</strong>, <strong>GitHub</strong>, <strong>LinkedIn</strong>, and <strong>Apple</strong>. Users authenticate with their existing account at the provider &mdash; Eurobase only receives their verified email, name, and profile picture. No application data is shared with the provider, and all user records remain in EU infrastructure.
				</p>

				<h4 class="text-base font-semibold text-gray-900 mt-4">Setting up a provider</h4>
				<ol class="text-sm text-gray-700 space-y-1.5 ml-4 list-decimal">
					<li>Go to <strong>Auth &rarr; Settings</strong> and toggle on the provider you want</li>
					<li>Create an OAuth app on the provider's developer console (links are shown in the setup instructions)</li>
					<li>Set the <strong>redirect/callback URL</strong> to your Eurobase API URL + <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/oauth/{'{'}provider{'}'}/callback</code></li>
					<li>Copy the <strong>Client ID</strong> and <strong>Client Secret</strong> into the Eurobase console</li>
					<li>Add your app's URL to the <strong>Allowed redirect URLs</strong> list in Session Settings</li>
				</ol>

				<h4 class="text-base font-semibold text-gray-900 mt-4">Provider-specific notes</h4>
				<div class="space-y-2 mt-2">
					<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
						<p class="text-xs font-semibold text-gray-700">Google &amp; GitHub</p>
						<p class="text-xs text-gray-600 mt-1">Standard OAuth 2.0 setup. You need a Client ID and Client Secret from their developer consoles. GitHub fetches the primary verified email if the user's email is private.</p>
					</div>
					<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
						<p class="text-xs font-semibold text-gray-700">LinkedIn</p>
						<p class="text-xs text-gray-600 mt-1">Uses OpenID Connect. When creating your LinkedIn app, you must request the <strong>"Sign In with LinkedIn using OpenID Connect"</strong> product under the Products tab. Standard Client ID + Client Secret setup.</p>
					</div>
					<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
						<p class="text-xs font-semibold text-gray-700">Apple</p>
						<p class="text-xs text-gray-600 mt-1">Requires additional configuration: a <strong>Service ID</strong> (used as Client ID), <strong>Team ID</strong>, <strong>Key ID</strong>, and a <strong>.p8 private key</strong> file from the Apple Developer Portal. Apple only sends the user's name on the first authorization &mdash; subsequent logins won't include it. Users may also receive a private relay email address if they choose to hide their real email.</p>
					</div>
				</div>

				<h4 class="text-base font-semibold text-gray-900 mt-4">SDK usage</h4>
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<button
						onclick={() => copyCode("// Redirect to provider's login page\neb.auth.signInWithOAuth('google', {\n  redirectTo: 'https://myapp.com/auth/callback'\n})\n// Supported providers: 'google', 'github', 'linkedin', 'apple', 'microsoft'\n\n// On your callback page — extract tokens from URL fragment\nconst { data, error } = await eb.auth.handleOAuthCallback()\n// data.access_token, data.user — user is now signed in", 'sdk-oauth')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-oauth' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// Redirect to provider's login page
eb.auth.signInWithOAuth('google', {'{'}
  redirectTo: 'https://myapp.com/auth/callback'
{'}'})
// Supported providers: 'google', 'github', 'linkedin', 'apple', 'microsoft'

// On your callback page — extract tokens from URL fragment
const {'{'} data, error {'}'} = await eb.auth.handleOAuthCallback()
// data.access_token, data.user — user is now signed in</pre>
				</div>

				<h4 class="text-base font-semibold text-gray-900 mt-4">REST API</h4>
				<div class="rounded-lg bg-gray-900 p-4 font-mono text-[11px] text-green-400 leading-relaxed overflow-x-auto mt-2">
					<div class="text-gray-500">// 1. Initiate OAuth — redirects browser to provider</div>
					<div><span class="text-amber-400">GET</span> /v1/auth/oauth/{'{'}provider{'}'}?redirect_url=https://myapp.com/callback</div>
					<div class="mt-2 text-gray-500">// 2. Provider redirects back with tokens in the URL fragment</div>
					<div class="text-gray-400">https://myapp.com/callback#access_token=eyJ...&amp;refresh_token=...&amp;token_type=bearer&amp;expires_in=604800</div>
				</div>

				<div class="mt-3 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">How it works under the hood</p>
					<ol class="text-xs text-gray-600 space-y-1 ml-4 list-decimal">
						<li>Your app redirects the user to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/oauth/{'{'}provider{'}'}</code> with a <code class="bg-white border border-gray-200 rounded px-1">redirect_url</code></li>
						<li>Eurobase generates a CSRF state token, encodes the redirect URL in it, and redirects the browser to the provider's consent screen</li>
						<li>The user authenticates at the provider (Google, GitHub, LinkedIn, or Apple)</li>
						<li>The provider redirects back to Eurobase's callback endpoint with an authorization code</li>
						<li>Eurobase exchanges the code for user info (email, name, avatar), finds or creates the user, and links the OAuth identity</li>
						<li>The user is redirected to your app with JWT access and refresh tokens in the URL fragment</li>
					</ol>
				</div>

				<div class="mt-3 rounded-lg border border-blue-200 bg-blue-50 px-4 py-3">
					<p class="text-xs text-blue-700"><strong>Account linking:</strong> If a user signs up with email/password and later signs in with an OAuth provider using the same email, the accounts are automatically linked &mdash; same user ID, no duplicates. OAuth sign-in also auto-verifies the user's email.</p>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						After sign-in, the SDK automatically includes the JWT with every database and storage request. Row-Level Security (RLS) policies on your tables use this token to enforce per-user access.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('rate-limits')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Rate Limits &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 7. RATE LIMITS ======================= -->
		<section id="rate-limits" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">7. Rate Limits</h2>
			<p class="text-sm italic text-gray-500 mb-4">LexVault is about to launch publicly. Alex wants safeguards so a runaway script can't burn through the project's email budget or hammer the auth endpoints.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Rate limits cap the volume of auth-related requests your project will accept per time window. They protect against credential stuffing, runaway scripts, and accidental self-DoS &mdash; without you having to write any middleware.
				</p>

				<p class="text-sm text-gray-700 leading-relaxed">
					Every Eurobase project gets a sane platform-wide default. You only need to touch this page if you want to tighten or loosen a specific knob.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Where to find it</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					In the console: <strong>Auth &rarr; Rate Limits</strong> tab. The page mirrors the same five knobs Supabase users will recognise, plus an "IP Address Forwarding" toggle for projects behind a CDN.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">The six knobs</h3>
				<ul class="text-sm text-gray-700 space-y-2 ml-4 list-disc">
					<li>
						<strong>Signup / sign-in rate</strong> &mdash; how many <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/signup</code> + <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/signin</code> requests per 5-minute window per IP. <span class="text-gray-500">Default: 8 per 5 min.</span>
					</li>
					<li>
						<strong>Token refresh rate</strong> &mdash; how many <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/refresh</code> calls per 5 min per IP. Higher than the others because legitimate SDK clients refresh proactively. <span class="text-gray-500">Default: 150 per 5 min (~ 1800/hour).</span>
					</li>
					<li>
						<strong>Token verification rate</strong> &mdash; how many OTP / magic-link verify calls per 5 min per IP. <span class="text-gray-500">Default: 30 per 5 min.</span>
					</li>
					<li>
						<strong>Emails per hour</strong> &mdash; cap on outbound auth emails (verification, password reset, magic link) the project sends through the platform sender per rolling hour. <span class="text-gray-500">Default: 2/hour.</span> When you bring your own SMTP (next chapter), sends through your provider are not counted against this cap.
					</li>
					<li>
						<strong>SMS per hour</strong> &mdash; cap on outbound auth SMS (phone OTP) per rolling hour. <span class="text-gray-500">Default: 30/hour.</span>
					</li>
					<li>
						<strong>IP Address Forwarding (Trust Proxy)</strong> &mdash; when on, the rate limiter uses the leftmost <code class="bg-gray-100 border border-gray-200 rounded px-1">X-Forwarded-For</code> entry as the client IP instead of the TCP peer. Turn this on if your app sits behind a CDN or reverse proxy that you control; leave it off otherwise. <span class="text-gray-500">Default: off.</span>
					</li>
				</ul>

				<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">Zero means default, not zero</p>
					<p class="text-xs text-gray-600 leading-relaxed">
						Saving an empty input or a literal <code class="bg-white border border-gray-200 rounded px-1">0</code> resets that knob to the platform default. The form is designed so you can't accidentally lock yourself out by typing <code class="bg-white border border-gray-200 rounded px-1">0</code> in "emails per hour" and then waiting a week wondering why signups aren't arriving.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Setting them up</h3>
				<ol class="text-sm text-gray-700 space-y-2 ml-4 list-decimal">
					<li>Open <strong>Auth &rarr; Rate Limits</strong>.</li>
					<li>For each knob, leave blank to keep the platform default, or enter your value. The placeholder text shows what the default is.</li>
					<li>(Optional) Toggle <strong>Trust Proxy</strong> if your app is behind a CDN/proxy. Only do this if you know your edge stack rewrites <code class="bg-gray-100 border border-gray-200 rounded px-1">X-Forwarded-For</code> &mdash; otherwise an attacker can spoof the IP and dodge the limit.</li>
					<li>Hit <strong>Save changes</strong>. The new limits apply within a few seconds &mdash; no redeploy needed.</li>
				</ol>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">What an over-quota response looks like</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					When a project exceeds a per-IP rate, the SDK receives <strong>HTTP 429 Too Many Requests</strong> with a <code class="bg-gray-100 border border-gray-200 rounded px-1">Retry-After</code> header naming the number of seconds the client should wait. Your app can surface a friendly message; the limit window slides, so retrying after the indicated delay succeeds.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-3">
					<button
						onclick={() => copyCode("// SDK example: handle 429 cleanly\ntry {\n  await eb.auth.signIn({ email, password })\n} catch (err) {\n  if (err.status === 429) {\n    const retryAfter = parseInt(err.headers['retry-after'] || '60', 10)\n    showToast(`Too many attempts. Try again in ${retryAfter}s.`)\n    return\n  }\n  throw err\n}", 'sdk-429')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-429' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// SDK example: handle 429 cleanly
try {'{'}
  await eb.auth.signIn({'{'} email, password {'}'})
{'}'} catch (err) {'{'}
  if (err.status === 429) {'{'}
    const retryAfter = parseInt(err.headers['retry-after'] || '60', 10)
    showToast(`Too many attempts. Try again in $&#123;retryAfter&#125;s.`)
    return
  {'}'}
  throw err
{'}'}</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Tuning guidance</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Just launched, small audience.</strong> Leave the defaults &mdash; they're set for a project receiving up to a few thousand signups/day.</li>
					<li><strong>Growing fast, lots of token refreshes.</strong> Raise the token-refresh knob if you see legitimate 429s. The default (~1800/hour/IP) handles most SDK clients, but if your app refreshes more aggressively, double or triple it.</li>
					<li><strong>Phone-first product (SMS OTP).</strong> Raise SMS/hour to your provider quota. The default is conservative so an accidental script doesn't bleed your GatewayAPI budget.</li>
					<li><strong>Outgrowing the platform email cap.</strong> See the next chapter (Custom SMTP) &mdash; bringing your own SMTP provider removes the platform "emails per hour" cap entirely for sends through your provider.</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">What rate limits don't do</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					These knobs only cap <strong>auth-flow</strong> traffic. They do not gate your SDK reads/writes against the database or storage &mdash; those are governed by your plan's request budget (Settings &rarr; Usage). For application-level rate limiting on your own endpoints, build it into your edge functions or your app middleware.
				</p>

				<div class="mt-6 text-right">
					<button onclick={() => scrollTo('custom-smtp')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
						Next: Custom SMTP &rarr;
					</button>
				</div>
			</div>
		</section>

		<!-- ======================= 8. CUSTOM SMTP ======================= -->
		<section id="custom-smtp" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">8. Custom SMTP</h2>
			<p class="text-sm italic text-gray-500 mb-4">LexVault grows past the platform email cap. Alex's signups start hitting the 2-emails-per-hour ceiling. He plugs in the firm's own SMTP provider and the ceiling disappears.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Every Eurobase project starts on the shared platform email sender (Scaleway TEM, EU-sovereign, but capped per project). When you outgrow that cap, or want emails to come from your own domain with your own sender reputation, you can bring your own SMTP provider.
				</p>

				<p class="text-sm text-gray-700 leading-relaxed">
					Once configured and verified, all auth emails for your project (verification, password reset, magic link) route through your SMTP instead of the platform sender. The platform "emails per hour" cap no longer applies to your sends &mdash; you're now bounded by your provider's own limits.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Where to find it</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					In the console: <strong>Auth &rarr; SMTP</strong> tab. Only project admins can configure this &mdash; members with the "developer" or "viewer" role can't see the credentials.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">What you'll need from your provider</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Host</strong> &mdash; the SMTP server hostname (e.g. <code class="bg-gray-100 border border-gray-200 rounded px-1">smtp.brevo.com</code>, <code class="bg-gray-100 border border-gray-200 rounded px-1">smtp-relay.brevo.com</code>, <code class="bg-gray-100 border border-gray-200 rounded px-1">in.mailjet.com</code>).</li>
					<li><strong>Port</strong> &mdash; usually 587 (STARTTLS) or 465 (TLS). Check your provider's docs.</li>
					<li><strong>Username + Password</strong> &mdash; the SMTP credentials from your provider. Often the username is an API key identifier and the "password" is the secret half.</li>
					<li><strong>From address</strong> &mdash; the email address auth messages will come from (e.g. <code class="bg-gray-100 border border-gray-200 rounded px-1">noreply@yourdomain.com</code>). Must be a domain you've verified with your provider.</li>
					<li><strong>From name</strong> &mdash; optional display name (e.g. "LexVault").</li>
					<li><strong>Encryption</strong> &mdash; pick STARTTLS for port 587 (most common), TLS for port 465, or None only for an internal relay on a private network.</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Setting it up</h3>
				<ol class="text-sm text-gray-700 space-y-2 ml-4 list-decimal">
					<li>Open <strong>Auth &rarr; SMTP</strong>.</li>
					<li>Fill in the form with the details above. Hit <strong>Save</strong>. The password is encrypted at rest with your project's per-tenant key &mdash; we never store or transmit it in plaintext after this point, and the API never returns it.</li>
					<li>You'll see an amber <strong>Not verified</strong> badge. Until you verify by sending a test, the project keeps using the platform sender. This is intentional &mdash; we'd rather fail loudly at setup than silently at first signup.</li>
					<li>Type an address you can check (your own works) into <strong>Send test</strong>, hit the button. Within seconds you should receive a small "your custom SMTP is wired up correctly" message.</li>
					<li>The badge flips to a green <strong>Verified</strong>. From this point onward your project's auth emails route through your provider.</li>
				</ol>

				<div class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-emerald-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
					</svg>
					<p class="text-sm text-emerald-800">
						<strong>Edit without retyping.</strong> When you change a non-secret field (host, port, from address, ...) you can leave the password blank to keep the saved one. The placeholder switches to &bullet;&bullet;&bullet;&bullet; so you know there's a stored password being preserved.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">If the test fails</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The console shows the exact error your provider returned &mdash; auth failed, TLS failed, recipient rejected, etc. Fix the config and re-test.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					<strong>If you were already verified and a test starts failing,</strong> the project keeps using the last-known-good config until either a successful retest or a config change. A transient blip from your provider doesn't silently regress your project to the platform sender behind your back.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Sovereignty advisory</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase itself runs entirely on EU-sovereign infrastructure (Scaleway, France). If you configure a US-based SMTP provider (SendGrid, Mailgun, Postmark, Amazon SES, Mandrill, SparkPost), the console shows an amber advisory: your auth email content will leave the EU jurisdiction even though the rest of your project doesn't.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					This is your call &mdash; it's not a block. EU-based alternatives worth knowing: Scaleway TEM, Brevo (FR), Mailjet (FR), Mailtrap EU.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">What stays on the platform sender</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Custom SMTP routes <strong>your project's auth emails</strong> (verification, password reset, magic link). It does not route:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc mt-2">
					<li>Console password resets to <strong>you</strong> (the Eurobase account owner) &mdash; those are platform-level and always come from us.</li>
					<li>Broadcast emails sent via the superadmin announcement tool.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Everything that represents <em>your tenant</em> goes through your sender; everything that represents <em>us</em> stays on the platform sender. The rule is simple and the routing is automatic &mdash; you don't need to do anything special.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Disconnecting</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Hit <strong>Disconnect</strong> at any time to fall back to the platform sender. The sealed password is wiped from our database. You can re-add later with fresh credentials.
				</p>

				<div class="mt-6 text-right">
					<button onclick={() => scrollTo('users')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
						Next: Managing End Users &rarr;
					</button>
				</div>
			</div>
		</section>

		<!-- ======================= 9. MANAGING END USERS ======================= -->
		<section id="users" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">9. Managing End Users</h2>
			<p class="text-sm italic text-gray-500 mb-4">A law firm onboards new employees. Alex needs to manage their accounts.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Users page shows every end-user registered in your project. From here you can create, edit, suspend, and delete accounts.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">User management features</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>User list</strong> &mdash; searchable table with email/phone, provider badges, status, and creation date</li>
					<li><strong>Provider column</strong> &mdash; shows how each user signed up (email, google, github, linkedin, apple, phone). Users with multiple linked providers show all badges.</li>
					<li><strong>Phone-only users</strong> &mdash; users who sign up via SMS OTP appear with their phone number instead of email</li>
					<li><strong>Create user</strong> &mdash; manually add a user with email and password</li>
					<li><strong>Edit user</strong> &mdash; update email, display name, or metadata</li>
					<li><strong>Suspend / reactivate</strong> &mdash; temporarily block a user from signing in (revokes all refresh tokens)</li>
					<li><strong>Delete user</strong> &mdash; permanently remove a user account</li>
					<li><strong>Reset password</strong> &mdash; set a new password for a user directly (revokes all refresh tokens)</li>
					<li><strong>Metadata JSON</strong> &mdash; attach arbitrary JSON metadata to any user (e.g., role, department, permissions)</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Account linking</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					When a user signs in with a social provider (Google, GitHub, etc.) using the same email as an existing account, the accounts are automatically linked. The user can then sign in with either method. All linked providers are shown in the Provider column.
				</p>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						End users are stored in a <code class="rounded bg-eurobase-100 px-1.5 py-0.5 text-xs font-mono text-eurobase-700">users</code> platform table that's managed separately from your application tables. You won't see it in the Database view &mdash; it's accessed through the Users tab.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('api')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Exploring the API &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 8. EXPLORING THE API ======================= -->
		<section id="api" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">10. Exploring the API</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to see every API endpoint available for the LexVault tables.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The API page auto-generates a REST endpoint reference for every table in your project. For each table you get endpoints for listing, creating, reading, updating, and deleting records.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">What you'll find</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Endpoint list</strong> &mdash; every table gets <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">PATCH</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">DELETE</code> endpoints</li>
					<li><strong>Try-it panel</strong> &mdash; test endpoints directly from the console with a built-in request builder</li>
					<li><strong>Query parameters</strong> &mdash; filter with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eq</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">neq</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">gt</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">lt</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">like</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">order</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">limit</code>, and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">offset</code></li>
					<li><strong>Code snippets</strong> &mdash; auto-generated cURL and SDK examples for every endpoint</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">cURL examples</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode('# List all clients\ncurl https://lexvault.eurobase.app/api/v1/db/clients \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"\n\n# Create a client\ncurl -X POST https://lexvault.eurobase.app/api/v1/db/clients \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"name": "Acme Legal", "email": "info@acmelegal.eu"}\'\n\n# Update a client\ncurl -X PATCH https://lexvault.eurobase.app/api/v1/db/clients?eq.email=info@acmelegal.eu \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"plan": "pro"}\'\n\n# Delete a client\ncurl -X DELETE https://lexvault.eurobase.app/api/v1/db/clients?eq.id=some-uuid \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"', 'curl-crud')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'curl-crud' ? 'Copied!' : 'Copy'}
					</button>
					<pre># List all clients
curl https://lexvault.eurobase.app/api/v1/db/clients \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"

# Create a client
curl -X POST https://lexvault.eurobase.app/api/v1/db/clients \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d '{"{"}"name": "Acme Legal", "email": "info@acmelegal.eu"{"}"}'

# Update a client
curl -X PATCH https://lexvault.eurobase.app/api/v1/db/clients?eq.email=info@acmelegal.eu \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d '{"{"}"plan": "pro"{"}"}'

# Delete a client
curl -X DELETE https://lexvault.eurobase.app/api/v1/db/clients?eq.id=some-uuid \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"</pre>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('webhooks')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Webhooks &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 9. WEBHOOKS ======================= -->
		<section id="webhooks" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">11. Webhooks</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants LexVault to be notified whenever a new client record is created.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Webhooks let your application receive real-time HTTP callbacks when events happen in your Eurobase project &mdash; database changes, user signups, file uploads, and more.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Setting up a webhook</h3>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">1</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Create a webhook</p>
						<p class="text-sm text-gray-600">Go to the Webhooks page and click "Create Webhook". Enter a name and your endpoint URL.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">2</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Select events</p>
						<p class="text-sm text-gray-600">Choose which events trigger the webhook: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db.insert</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db.update</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db.delete</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.signup</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.signin</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage.upload</code>, and more. You can also filter by table name.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">3</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Copy the signing secret</p>
						<p class="text-sm text-gray-600">Each webhook gets a signing secret. Use it to verify that incoming requests genuinely come from Eurobase.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">4</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Monitor delivery history</p>
						<p class="text-sm text-gray-600">The webhook detail page shows every delivery attempt with status code, response time, and payload. Failed deliveries are retried automatically.</p>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Verifying signatures (Node.js)</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("import crypto from 'crypto'\n\nfunction verifyWebhook(payload, signature, secret) {\n  const expected = crypto\n    .createHmac('sha256', secret)\n    .update(payload)\n    .digest('hex')\n  return crypto.timingSafeEqual(\n    Buffer.from(signature),\n    Buffer.from(expected)\n  )\n}\n\n// In your Express handler:\napp.post('/webhooks/eurobase', express.raw({ type: 'application/json' }), (req, res) => {\n  const sig = req.headers['x-eurobase-signature']\n  if (!verifyWebhook(req.body, sig, process.env.WEBHOOK_SECRET)) {\n    return res.status(401).send('Invalid signature')\n  }\n  const event = JSON.parse(req.body)\n  console.log('Received:', event.type, event.data)\n  res.sendStatus(200)\n})", 'webhook-verify')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'webhook-verify' ? 'Copied!' : 'Copy'}
					</button>
					<pre>import crypto from 'crypto'

function verifyWebhook(payload, signature, secret) {'{'}\
  const expected = crypto
    .createHmac('sha256', secret)
    .update(payload)
    .digest('hex')
  return crypto.timingSafeEqual(
    Buffer.from(signature),
    Buffer.from(expected)
  )
{'}'}

// In your Express handler:
app.post('/webhooks/eurobase', express.raw({'{'} type: 'application/json' {'}'}), (req, res) => {'{'}\
  const sig = req.headers['x-eurobase-signature']
  if (!verifyWebhook(req.body, sig, process.env.WEBHOOK_SECRET)) {'{'}\
    return res.status(401).send('Invalid signature')
  {'}'}
  const event = JSON.parse(req.body)
  console.log('Received:', event.type, event.data)
  res.sendStatus(200)
{'}'})</pre>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('rls')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Row-Level Security &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 10. ROW-LEVEL SECURITY ======================= -->
		<section id="rls" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">12. Row-Level Security (RLS)</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs each law firm employee to only see their own cases.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Row-Level Security lets you control which rows each user can read, insert, update, or delete. Policies are written in SQL and enforced by PostgreSQL itself &mdash; no application code needed.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Auth helper functions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase provides built-in functions you can use in RLS policies to access the current user's identity. Both the native (no-dot) and Supabase-style (<code class="bg-gray-100 border border-gray-200 rounded px-1">auth.*</code>) forms are equivalent &mdash; pick whichever reads better. <code class="bg-gray-100 border border-gray-200 rounded px-1">is_service_role()</code> is true for service-key calls and lets you write a single policy that admits both end-users and trusted server-side code.
				</p>

				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Function</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Returns</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_uid() / auth.uid()</td><td class="px-3 py-1.5 text-gray-500">uuid</td><td class="px-3 py-1.5 text-gray-500">Current user's ID, NULL when no end-user context</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_email() / auth.email()</td><td class="px-3 py-1.5 text-gray-500">text</td><td class="px-3 py-1.5 text-gray-500">Current user's email</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_role() / auth.role()</td><td class="px-3 py-1.5 text-gray-500">text</td><td class="px-3 py-1.5 text-gray-500">'service_role', 'authenticated', or 'anon'</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">is_service_role()</td><td class="px-3 py-1.5 text-gray-500">boolean</td><td class="px-3 py-1.5 text-gray-500">True for calls made with the service key (bypasses end-user RLS)</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth.jwt()</td><td class="px-3 py-1.5 text-gray-500">jsonb</td><td class="px-3 py-1.5 text-gray-500">{'{'} sub, email, role {'}'} &mdash; for policies that read JWT claims</td></tr>
						</tbody>
					</table>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 mt-3">
					<p class="text-xs text-amber-800">
						<strong>Heads up &mdash; do not redefine <code class="bg-amber-100 border border-amber-200 rounded px-1">auth.uid()</code> in your migrations.</strong>
						The Supabase boilerplate that reads <code class="bg-amber-100 border border-amber-200 rounded px-1">request.jwt.claims</code> won't work here &mdash; Eurobase uses a different session GUC. The built-in <code class="bg-amber-100 border border-amber-200 rounded px-1">auth.uid()</code> already does the right thing.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Common RLS patterns</h3>

				<div class="space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Users can only read their own rows</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "read own" ON todos FOR SELECT USING (user_id = auth_uid());</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Users can insert with their own ID</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "insert own" ON todos FOR INSERT WITH CHECK (user_id = auth_uid());</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Users can update only their own rows</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "update own" ON todos FOR UPDATE USING (user_id = auth_uid());</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Public read, authenticated write</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div>CREATE POLICY "public read" ON posts FOR SELECT USING (true);</div>
							<div>CREATE POLICY "auth insert" ON posts FOR INSERT WITH CHECK (auth_role() = 'authenticated');</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Admin access by email</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "admin all" ON users FOR ALL USING (auth_email() = 'admin@company.eu');</div>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Full example: secure a tasks table</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Follow these steps to create a table where each user can only see and manage their own rows.
				</p>

				<div class="mt-3 space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 1: Create the table with a user_id column</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div>CREATE TABLE tasks (</div>
							<div>&nbsp;&nbsp;id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),</div>
							<div>&nbsp;&nbsp;user_id UUID NOT NULL REFERENCES users(id),</div>
							<div>&nbsp;&nbsp;title TEXT NOT NULL,</div>
							<div>&nbsp;&nbsp;completed BOOLEAN DEFAULT false,</div>
							<div>&nbsp;&nbsp;created_at TIMESTAMPTZ DEFAULT now()</div>
							<div>);</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 2: RLS is enabled automatically</p>
						<p class="mt-1 text-[10px] text-gray-500">Tables created via the Eurobase console or API have RLS enabled by default. You'll see a green RLS badge on protected tables. If a table shows "RLS OFF" in the sidebar, enable it with:</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 3: Add policies for each operation</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-1">
							<div class="text-gray-500">-- Users can read only their own tasks</div>
							<div>CREATE POLICY "select own" ON tasks FOR SELECT</div>
							<div>&nbsp;&nbsp;USING (user_id = auth_uid());</div>
							<div class="mt-1.5 text-gray-500">-- Users can insert tasks with their own user_id</div>
							<div>CREATE POLICY "insert own" ON tasks FOR INSERT</div>
							<div>&nbsp;&nbsp;WITH CHECK (user_id = auth_uid());</div>
							<div class="mt-1.5 text-gray-500">-- Users can update only their own tasks</div>
							<div>CREATE POLICY "update own" ON tasks FOR UPDATE</div>
							<div>&nbsp;&nbsp;USING (user_id = auth_uid());</div>
							<div class="mt-1.5 text-gray-500">-- Users can delete only their own tasks</div>
							<div>CREATE POLICY "delete own" ON tasks FOR DELETE</div>
							<div>&nbsp;&nbsp;USING (user_id = auth_uid());</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 4: Test it from the SDK</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div class="text-gray-500">// Sign in as a user</div>
							<div>await eb.auth.signIn({'{'} email: 'alice@example.com', password: '...' {'}'})</div>
							<div></div>
							<div class="text-gray-500">// Insert a task — user_id is automatically checked by RLS</div>
							<div>await eb.db.from('tasks').insert({'{'} user_id: session.user.id, title: 'Buy milk' {'}'})</div>
							<div></div>
							<div class="text-gray-500">// Query — only Alice's tasks are returned</div>
							<div>const {'{'} data {'}'} = await eb.db.from('tasks').select('*')</div>
							<div class="text-gray-500">// data = [{'{'} title: "Buy milk", ... {'}'}] — Bob's tasks are invisible</div>
						</div>
					</div>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3 mt-4">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<div class="text-sm text-amber-800">
						<p><strong>RLS is on by default</strong> for tables created via the console. But without policies, no rows are visible. Add at least a SELECT policy so users can read data. Tables showing "RLS OFF" in the sidebar need to be secured with <code class="bg-amber-100 rounded px-1">ALTER TABLE ... ENABLE ROW LEVEL SECURITY;</code></p>
					</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Supabase compatibility:</strong> Eurobase's <code class="bg-white/50 rounded px-1">auth_uid()</code>, <code class="bg-white/50 rounded px-1">auth_role()</code>, and <code class="bg-white/50 rounded px-1">auth_email()</code> follow the same pattern as Supabase's GoTrue. RLS policies written for Supabase work in Eurobase with minimal changes.</p>
						<p class="mt-1"><strong>Secret API key</strong> (<code class="bg-white/50 rounded px-1">eb_sk_</code>) bypasses RLS entirely &mdash; use it for server-side admin access, never in client code.</p>
					</div>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('vault')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Vault &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 11. VAULT ======================= -->
		<section id="vault" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">13. Vault (Encrypted Secrets)</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to store API keys for Mollie payments and Twilio SMS securely.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Vault is Eurobase's built-in encrypted secrets storage. Store API keys, credentials, and sensitive configuration securely &mdash; encrypted with AES-256-GCM, accessible via the console, API, and SDK. All secrets stay in EU infrastructure.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Storing secrets</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Go to the <strong>Vault</strong> tab in your project. Click "New Secret", enter a name (e.g. <code class="bg-gray-100 rounded px-1">stripe_api_key</code>), the secret value, and an optional description. The value is encrypted before storage.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Accessing secrets from the SDK</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Secrets are only accessible with the <strong>secret API key</strong> (<code class="bg-gray-100 rounded px-1">eb_sk_</code>). The public key cannot read secrets &mdash; this prevents client-side exposure.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>// Server-side only (Node.js, backend)
const eb = createClient({'{'} url: '...', apiKey: 'eb_sk_...' {'}'})

// Read a secret
const {'{'} data: apiKey {'}'} = await eb.vault.get('stripe_api_key')
console.log(apiKey) // 'sk_live_...'

// Store a secret
await eb.vault.set('twilio_token', 'ACxxxxxxx', 'Twilio auth token')

// List all secret names (values not included)
const {'{'} data: secrets {'}'} = await eb.vault.list()
// [{'{'}name: 'stripe_api_key', description: '...'{'}'}]

// Delete a secret
await eb.vault.delete('old_key')</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Common use cases</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Payment provider keys</strong> &mdash; Mollie, Stripe API keys for processing payments</li>
					<li><strong>Email/SMS credentials</strong> &mdash; SendGrid, Twilio tokens for notifications</li>
					<li><strong>External API keys</strong> &mdash; OpenAI, Google Maps, any third-party service</li>
					<li><strong>Database connection strings</strong> &mdash; credentials for external databases</li>
					<li><strong>Webhook signing secrets</strong> &mdash; verify incoming webhooks from external services</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">REST API</h3>
				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Method</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Endpoint</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">GET</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault</td><td class="px-3 py-1.5 text-gray-500">List secret names (no values)</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">GET</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault/:name</td><td class="px-3 py-1.5 text-gray-500">Get decrypted value</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">POST</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault</td><td class="px-3 py-1.5 text-gray-500">Create secret</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">DELETE</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault/:name</td><td class="px-3 py-1.5 text-gray-500">Delete secret</td></tr>
						</tbody>
					</table>
				</div>
				<p class="text-xs text-gray-400 mt-1">All vault endpoints require the secret API key (<code class="bg-gray-100 rounded px-1">eb_sk_</code>). Public key access returns 403.</p>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>EU sovereignty:</strong> Secrets are encrypted with AES-256-GCM and stored in Scaleway PostgreSQL (France). The encryption key lives in your server environment &mdash; not in a US-based secrets manager. No Google Secret Manager, no AWS KMS, no HashiCorp Vault (US). Your credentials never leave the EU.</p>
					</div>
				</div>

				<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 mt-3">
					<p class="text-xs font-semibold text-gray-700 mb-1">Plan limits</p>
					<p class="text-xs text-gray-600">Free: 5 secrets &middot; Pro: 100 secrets</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('cron')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Scheduled Jobs &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 12. SCHEDULED JOBS ======================= -->
		<section id="cron" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">14. Scheduled Jobs</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to clean up expired sessions and send weekly reports automatically.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Scheduled jobs let you run SQL statements or database functions on a recurring schedule. No server needed &mdash; Eurobase executes them automatically in your project's database.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Creating a scheduled job</h3>
				<ol class="text-sm text-gray-700 space-y-1.5 ml-4 list-decimal">
					<li>Go to the <strong>Cron</strong> tab in your project</li>
					<li>Click <strong>New Job</strong></li>
					<li>Give it a name (e.g. "Clean expired sessions")</li>
					<li>Choose a schedule preset or write a custom cron expression</li>
					<li>Select the action type: <strong>SQL</strong> (run a query) or <strong>RPC</strong> (call a function)</li>
					<li>Write the SQL or function name</li>
					<li>Click <strong>Create</strong></li>
				</ol>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Common examples</h3>

				<div class="space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Clean up expired sessions (every hour)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">0 * * * *</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">DELETE FROM sessions WHERE expires_at &lt; now()</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Send weekly digest (every Monday at 9am)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">0 9 * * 1</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">SELECT send_weekly_digest()</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Archive old records (daily at midnight)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">0 0 * * *</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">INSERT INTO archive SELECT * FROM logs WHERE created_at &lt; now() - interval '30 days'; DELETE FROM logs WHERE created_at &lt; now() - interval '30 days';</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Check pending orders (every 5 minutes)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">*/5 * * * *</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">SELECT process_pending_orders()</div>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Cron schedule reference</h3>
				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Field</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Values</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Special</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700">Minute</td><td class="px-3 py-1.5 text-gray-500">0-59</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Hour</td><td class="px-3 py-1.5 text-gray-500">0-23</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Day of month</td><td class="px-3 py-1.5 text-gray-500">1-31</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Month</td><td class="px-3 py-1.5 text-gray-500">1-12</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Day of week</td><td class="px-3 py-1.5 text-gray-500">0-6 (Sun=0)</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
						</tbody>
					</table>
				</div>

				<div class="rounded-lg border border-gray-200 bg-gray-50 p-3 mt-3">
					<p class="text-xs font-semibold text-gray-700 mb-1">Quick reference</p>
					<div class="grid grid-cols-2 gap-x-6 gap-y-0.5 text-xs text-gray-600">
						<span><code class="bg-white border border-gray-200 rounded px-1">* * * * *</code> &mdash; every minute</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">*/5 * * * *</code> &mdash; every 5 minutes</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 * * * *</code> &mdash; every hour</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 0 * * *</code> &mdash; daily at midnight</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 9 * * 1</code> &mdash; Monday 9am</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 0 1 * *</code> &mdash; 1st of month</span>
					</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Plan limits:</strong> Free plan includes 2 scheduled jobs. Pro plan has unlimited jobs.</p>
						<p class="mt-1">Jobs run SQL in your project's database schema with full access. They execute as the system user, not as an end-user &mdash; RLS policies are bypassed.</p>
					</div>
				</div>
			<h3 class="text-lg font-semibold text-gray-900 mt-6">RPC Functions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					RPC (Remote Procedure Call) functions are reusable PostgreSQL functions stored in your database. Unlike raw SQL cron actions, functions can contain complex logic (loops, conditionals, error handling) and can be called from both cron jobs and your app via the SDK.
				</p>

				<div class="rounded-lg border border-amber-200 bg-amber-50/60 px-4 py-3 mt-2">
					<p class="text-sm text-amber-900"><span class="font-semibold">RPC vs Cron Job vs DB Trigger vs Edge Function:</span> Eurobase has four kinds of "server-side code" and the distinction matters. Quick gist: RPC = callable SQL (this section). Cron Job = scheduled SQL (above). DB Trigger = reactive SQL fired by row events on a table (managed in Database &rarr; Triggers). <button onclick={() => scrollTo('edge-functions')} class="text-amber-900 underline cursor-pointer">Edge Function</button> = TypeScript in a Deno container, for external API calls and JS-ecosystem things. The <button onclick={() => scrollTo('edge-functions')} class="text-amber-900 underline cursor-pointer">full comparison table</button> in the Edge Functions chapter has language, transactional semantics, and use cases side by side.</p>
				</div>

				<h4 class="text-sm font-semibold text-gray-700 mt-4">Creating a function</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					When creating a cron job, select "RPC Function" and click "Create New Function". Choose a name, language, return type, and write the function body.
				</p>

				<div class="mt-3 space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Example: Clean up expired sessions (void — for cron)</p>
						<p class="text-xs text-gray-500 mt-0.5">Language: PL/pgSQL &middot; Returns: void</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">BEGIN<br/>&nbsp;&nbsp;DELETE FROM refresh_tokens WHERE expires_at &lt; now();<br/>&nbsp;&nbsp;DELETE FROM email_tokens WHERE expires_at &lt; now();<br/>END;</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Example: Get active user count (integer — for SDK)</p>
						<p class="text-xs text-gray-500 mt-0.5">Language: SQL &middot; Returns: integer</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">SELECT count(*)::integer FROM users WHERE last_sign_in_at > now() - interval '30 days';</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Example: Generate daily stats (jsonb — for SDK)</p>
						<p class="text-xs text-gray-500 mt-0.5">Language: PL/pgSQL &middot; Returns: jsonb</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">DECLARE result jsonb;<br/>BEGIN<br/>&nbsp;&nbsp;SELECT jsonb_build_object(<br/>&nbsp;&nbsp;&nbsp;&nbsp;'total_users', (SELECT count(*) FROM users),<br/>&nbsp;&nbsp;&nbsp;&nbsp;'active_today', (SELECT count(*) FROM users WHERE last_sign_in_at > now() - interval '1 day')<br/>&nbsp;&nbsp;) INTO result;<br/>&nbsp;&nbsp;RETURN result;<br/>END;</div>
					</div>
				</div>

				<h4 class="text-sm font-semibold text-gray-700 mt-4">Return types explained</h4>
				<div class="rounded-lg border border-gray-200 overflow-hidden mt-2">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Type</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">When to use</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">SDK result</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">void</td><td class="px-3 py-1.5 text-gray-500">Cron jobs, cleanup tasks, side effects only</td><td class="px-3 py-1.5 text-gray-500">null</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">text</td><td class="px-3 py-1.5 text-gray-500">Return a message or formatted string</td><td class="px-3 py-1.5 text-gray-500">"hello world"</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">integer</td><td class="px-3 py-1.5 text-gray-500">Return a count or numeric value</td><td class="px-3 py-1.5 text-gray-500">42</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">boolean</td><td class="px-3 py-1.5 text-gray-500">Return true/false checks</td><td class="px-3 py-1.5 text-gray-500">true</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">jsonb</td><td class="px-3 py-1.5 text-gray-500">Return structured data (objects, arrays)</td><td class="px-3 py-1.5 text-gray-500">{"{'key': 'value'}"}</td></tr>
						</tbody>
					</table>
				</div>

				<h4 class="text-sm font-semibold text-gray-700 mt-4">Calling functions from the SDK</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Functions with a return type (not void) can be called from your app. The return value is sent back as JSON.
				</p>
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>// Call an RPC function from the SDK
const {'{'} data, error {'}'} = await eb.db.rpc('get_active_user_count')
console.log(data) // 42

// Call a function that returns JSON
const {'{'} data: stats {'}'} = await eb.db.rpc('generate_daily_stats')
console.log(stats) // {'{'} total_users: 150, active_today: 23 {'}'}</pre>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Cron + SDK tip:</strong> Create a function that returns <code class="bg-white/50 rounded px-1">void</code> for cron (e.g. cleanup tasks), and separate functions that return data for your SDK calls (e.g. stats, reports). A function can do both &mdash; perform side effects and return a result.</p>
					</div>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('edge-functions')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Edge Functions &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 13. EDGE FUNCTIONS ======================= -->
		<section id="edge-functions" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">15. Edge Functions</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to process a payment webhook and update an order — this requires custom server-side logic beyond SQL.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Edge Functions are serverless TypeScript/JavaScript functions that run on Eurobase's EU-sovereign infrastructure. They let you write custom server-side logic — payment processing, external API integrations, data transformations — without managing any servers.
				</p>

				<div class="rounded-lg bg-blue-50 border border-blue-200 px-4 py-3">
					<p class="text-sm text-blue-800"><span class="font-semibold">EU Sovereign:</span> Edge Functions run on Scaleway infrastructure in France. Unlike other platforms that route through US-hosted runtimes, your code and secrets never leave the EU.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Server-side logic in Eurobase: four kinds, when to pick which</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase has four distinct surfaces for "code that runs on the server." The SDK shapes look similar in places, so it's worth getting the distinction clear before you start building. <strong>RPC functions</strong> and <strong>DB triggers</strong> are both PostgreSQL functions but with different invocation models; <strong>Cron jobs</strong> are scheduled wrappers around either an SQL statement or an RPC; <strong>Edge functions</strong> are a separate Deno runtime entirely.
				</p>
				<div class="overflow-x-auto">
					<table class="w-full text-sm border border-gray-200 rounded-lg">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">&nbsp;</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">Cron Job</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">RPC Function</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">DB Trigger</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">Edge Function</th>
							</tr>
						</thead>
						<tbody class="text-xs">
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">What it is</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">A schedule that runs SQL or an RPC at a cron expression</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">A reusable Postgres function (SQL/PL-pgSQL) you call by name</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">A Postgres function bound to a row event on a table</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Serverless TS/JS in a Deno container</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Where it runs</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Inside Postgres (cron worker fires the SQL/RPC)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Inside Postgres</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Inside Postgres, in the row's transaction</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Deno container, outside the database</td>
							</tr>
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">How it's invoked</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">By the cron schedule (no caller)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top"><code class="text-[11px]">eb.db.rpc('name')</code> from the SDK, or from a cron job</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Automatically, when an INSERT / UPDATE / DELETE / TRUNCATE happens on the attached table</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top"><code class="text-[11px]">eb.functions.invoke('name')</code> or HTTP</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Language</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">SQL (or pick an RPC)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">SQL or PL/pgSQL</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">PL/pgSQL (return type <code class="text-[11px]">trigger</code>)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">TypeScript / JavaScript</td>
							</tr>
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Transactional</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Yes — its own transaction at run time</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Yes — runs in the caller's transaction</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Yes — runs in the row operation's transaction (can roll it back by raising)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">No — separate process</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Where to manage it</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Cron &amp; RPC &rarr; Scheduled Jobs</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Cron &amp; RPC &rarr; Functions</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Function: Cron &amp; RPC &rarr; Functions (Returns: trigger). Attachment: Database &rarr; table &rarr; Triggers panel</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Functions tab</td>
							</tr>
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Use case examples</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Daily cleanup of expired sessions; weekly digest aggregation; archiving old rows</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Computed leaderboard, multi-statement bulk update, an atomic check-and-decrement, complex aggregate the SDK can call by name</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Enforcing a max-N-rows-per-user constraint on INSERT; auto-stamping <code class="text-[11px]">updated_at</code>; mirroring inserts into an audit table</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Stripe / Mollie webhook; sending email via TEM; calling an external image-processing API; OAuth callback handling</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 font-medium text-gray-700 align-top">Pick when</td>
								<td class="px-3 py-2 text-gray-700 align-top">You want something to run on a schedule, regardless of user activity</td>
								<td class="px-3 py-2 text-gray-700 align-top">Your app needs to call a chunk of DB-only logic by name, atomically</td>
								<td class="px-3 py-2 text-gray-700 align-top">A row change must always be accompanied by side-effect SQL — and the side effect must succeed or fail with the row change</td>
								<td class="px-3 py-2 text-gray-700 align-top">You need the JS ecosystem, an external HTTP call, or anything Postgres can't reach from within the database</td>
							</tr>
						</tbody>
					</table>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>Mental model</strong>: Cron jobs are <em>scheduled SQL</em>; RPC is <em>callable SQL</em>; DB triggers are <em>reactive SQL</em>; edge functions are <em>everything else</em>. The first three all run inside Postgres and share its transactional model. Edge functions are a separate runtime that talks to the DB over the SDK like your app does.
				</p>
				<div class="rounded-lg border border-amber-200 bg-amber-50/60 px-4 py-3">
					<p class="text-sm text-amber-900"><strong>One subtle gotcha</strong>: a function that <code class="rounded bg-white border border-amber-200 px-1 text-xs">RETURNS trigger</code> is created in the same place as RPC functions, but it won't appear in the RPC list and can't be called via <code class="rounded bg-white border border-amber-200 px-1 text-xs">eb.db.rpc()</code>. It only exists to be attached to a table by a trigger. The Functions list filters it out so the surfaces stay honest. To attach it: Database tab &rarr; pick the table &rarr; expand the <strong>Triggers</strong> panel.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Creating a Function</h3>
				<p class="text-sm text-gray-700">From the console, navigate to <span class="font-mono text-sm bg-gray-100 px-1 rounded">Functions</span> tab and click <span class="font-mono text-sm bg-gray-100 px-1 rounded">+ New Function</span>. Give it a lowercase name with hyphens (e.g., <code>process-order</code>).</p>
				<p class="text-sm text-gray-700 mt-2">Or via CLI:</p>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto">eurobase edge-functions deploy process-order --file functions/process-order.ts</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Function Structure</h3>
				<p class="text-sm text-gray-700">Write TypeScript or JavaScript and export your handler with <code>export default</code> (or <code>module.exports</code>). The runner passes <code>req</code> (a <code>Request</code>) and <code>ctx</code> (context). Code is compiled on deploy — types are stripped, not checked.</p>
				<p class="text-sm text-amber-700 bg-amber-50 border-l-4 border-amber-400 px-3 py-2 mt-2 rounded"><strong>Heads-up:</strong> third-party imports (<code>import … from "https://…"</code> or npm packages) are not supported yet — inline dependencies into the function file.</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Reading the request (webhooks & APIs)</h3>
				<p class="text-sm text-gray-700">Your function receives the full incoming request, so it can act as a webhook receiver or small HTTP API:</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1 mt-2">
					<li><strong>Query string</strong> — <code>new URL(req.url).searchParams.get('token')</code></li>
					<li><strong>Custom headers</strong> — <code>req.headers.get('X-Signature')</code>, <code>X-Api-Key</code>, <code>X-Webhook-Id</code>, etc. are forwarded as sent.</li>
					<li><strong>Body</strong> — <code>await req.json()</code> / <code>req.text()</code>; <code>Content-Type</code> is preserved.</li>
				</ul>
				<p class="text-sm text-gray-700 mt-2">Withheld from your function so platform credentials don't leak: the auth that authenticates your call to Eurobase — the <code>Authorization</code>, <code>apikey</code>, <code>Cookie</code> headers <em>and</em> the <code>?apikey=</code> query param — plus Eurobase's internal <code>X-Eurobase-*</code> / <code>X-Project-*</code> / <code>X-Function-*</code> / <code>X-User-*</code> headers. For end-user identity on a <code>verify_jwt</code> function, use <code>ctx.user</code>. Put a partner's auth token in any other custom header (e.g. <code>X-Api-Key</code>, <code>X-Signature</code>) or query param (e.g. <code>?token=</code>).</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Example handler</h3>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto whitespace-pre">module.exports = async (req, ctx) =&gt; {"{"}{"\n"}  // Parse the incoming request{"\n"}  const {"{"} orderId {"}"} = await req.json();{"\n"}{"\n"}  // Query the database (scoped to your project){"\n"}  const {"{"} rows {"}"} = await ctx.db.sql({"\n"}    "SELECT * FROM orders WHERE id = $1",{"\n"}    [orderId]{"\n"}  );{"\n"}  const order = rows[0];{"\n"}{"\n"}  // Read a secret from Vault{"\n"}  const apiKey = await ctx.vault.get("PAYMENT_API_KEY");{"\n"}{"\n"}  // Call an external API{"\n"}  const payment = await fetch("https://api.mollie.com/v2/payments", {"{"}{"\n"}    method: "POST",{"\n"}    headers: {"{"} Authorization: `Bearer ${"{"} apiKey {"}"}` {"}"},{"\n"}    body: JSON.stringify({"{"} amount: order.total {"}"}){"\n"}  {"}"});{"\n"}{"\n"}  // Return a response{"\n"}  return new Response(JSON.stringify({"{"} status: "ok" {"}"}), {"{"}{"\n"}    status: 200,{"\n"}    headers: {"{"} "Content-Type": "application/json" {"}"},{"\n"}  {"}"});{"\n"}{"}"};</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Context API</h3>
				<div class="overflow-x-auto">
					<table class="w-full text-sm border border-gray-200 rounded-lg">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Property</th>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.db.sql(query, params)</td><td class="px-4 py-2 text-gray-600">Execute SQL scoped to your project schema</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.vault.get(name)</td><td class="px-4 py-2 text-gray-600">Read an encrypted secret from Vault</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.env</td><td class="px-4 py-2 text-gray-600">Per-function environment variables</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.user.id / ctx.user.email</td><td class="px-4 py-2 text-gray-600">Authenticated user (if JWT required)</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.log.info(msg) / .warn / .error</td><td class="px-4 py-2 text-gray-600">Structured logging (visible in Logs)</td></tr>
						</tbody>
					</table>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Invoking Functions</h3>
				<p class="text-sm text-gray-700">Functions are invoked via HTTP using your API key:</p>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto">POST https://your-project.eurobase.app/v1/functions/process-order{"\n"}Authorization: Bearer &lt;user-jwt&gt;{"\n"}apikey: eb_pk_...{"\n"}{"\n"}{"{"}"orderId": "abc-123"{"}"}</pre>

				<p class="text-sm text-gray-700 mt-2">Or via the SDK:</p>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto">const {"{"} data, error {"}"} = await eurobase.functions.invoke('process-order', {"{"}{"\n"}  body: {"{"} orderId: 'abc-123' {"}"},{"\n"}{"}"});</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">CLI Commands</h3>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto"># List edge functions{"\n"}eurobase edge-functions list{"\n"}{"\n"}# Deploy from local file{"\n"}eurobase edge-functions deploy process-order -f functions/process-order.ts{"\n"}{"\n"}# View execution logs{"\n"}eurobase edge-functions logs process-order{"\n"}{"\n"}# Delete a function{"\n"}eurobase edge-functions delete process-order</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Plan Limits</h3>
				<div class="overflow-x-auto">
					<table class="w-full text-sm border border-gray-200 rounded-lg">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Limit</th>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Free</th>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Pro</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-4 py-2 text-gray-700">Functions per project</td><td class="px-4 py-2">3</td><td class="px-4 py-2">25</td></tr>
							<tr><td class="px-4 py-2 text-gray-700">Execution timeout</td><td class="px-4 py-2">10 seconds</td><td class="px-4 py-2">60 seconds</td></tr>
							<tr><td class="px-4 py-2 text-gray-700">Memory per execution</td><td class="px-4 py-2">64 MB</td><td class="px-4 py-2">256 MB</td></tr>
						</tbody>
					</table>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Use Cases</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li><strong>Payment webhooks</strong> — Process Mollie callbacks, update order status</li>
					<li><strong>External integrations</strong> — Sync data to/from other EU SaaS</li>
					<li><strong>Custom auth logic</strong> — Post-signup hooks, role assignment</li>
					<li><strong>Data transformation</strong> — Parse CSVs, enrich records, generate reports</li>
					<li><strong>Notifications</strong> — Send emails, push notifications on events</li>
				</ul>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('logs')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Monitoring with Logs &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 14. MONITORING WITH LOGS ======================= -->
		<section id="logs" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">16. Monitoring with Logs</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex notices slow responses and wants to investigate API traffic.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Logs page gives you real-time visibility into every API request hitting your project. Use it to debug issues, monitor traffic, and understand usage patterns.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Stats cards</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					At the top of the page, you'll see summary statistics: total requests, average response time, error rate, and requests by status code.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Request log table</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Below the stats, a detailed table lists every request with:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li>Timestamp</li>
					<li>HTTP method and path</li>
					<li>Status code (color-coded: green for 2xx, yellow for 4xx, red for 5xx)</li>
					<li>Response time in milliseconds</li>
					<li>Client IP and user agent</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Filtering</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Use the filter controls to narrow results by HTTP method, status code range, path pattern, or time window. This makes it easy to isolate specific issues &mdash; for example, show only <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">5xx</code> errors on the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/api/v1/db/clients</code> endpoint.
				</p>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('compliance')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Compliance &amp; Audit Log &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 15. COMPLIANCE & AUDIT LOG ======================= -->
		<section id="compliance" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">17. Compliance & Audit Log</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex's client asks for proof that their data stays in the EU and a trail of who changed what.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Compliance page has two tabs: <strong>DPA Report</strong> and <strong>Audit Log</strong>. Together they give you the documentation you need for GDPR compliance reviews, security audits, and customer due diligence.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">DPA Report</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Generates a Data Processing Agreement (Article 30) report showing your sub-processors, data flow, encryption status, and whether any CLOUD Act exposure exists. Download it as JSON for your compliance records.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Audit Log</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Every sensitive action on your project is automatically recorded in the audit log with a timestamp, actor email, IP address, and metadata. Tracked actions include:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Auth config changes</strong> &mdash; updating login providers, OAuth settings, session duration</li>
					<li><strong>API key regeneration</strong> &mdash; who rotated keys and when</li>
					<li><strong>Project deletion</strong> &mdash; logged before the data is removed</li>
					<li><strong>Schema DDL</strong> &mdash; creating, dropping, or renaming tables and columns</li>
					<li><strong>RLS policy changes</strong> &mdash; toggling row-level security, applying presets, creating or dropping policies</li>
					<li><strong>Index changes</strong> &mdash; creating or dropping indexes and constraints</li>
					<li><strong>OAuth secrets</strong> &mdash; setting or rotating provider client secrets (the secret itself is never logged, only the event)</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Filtering</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Use the action filter dropdown to narrow the log to a specific action type &mdash; for example, show only <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">schema.drop_table</code> events to trace who deleted a table and when.
				</p>

				<div class="rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-blue-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-blue-800">
						Audit log entries cannot be edited or deleted. They are append-only and stored in the platform database, separate from your project's tenant schema.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('settings')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Project Settings &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 16. PROJECT SETTINGS ======================= -->
		<section id="settings" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">18. Project Settings</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to rotate an API key after an intern accidentally committed it.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Settings page manages your project's API keys and provides administrative actions.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">API key management</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>View keys</strong> &mdash; see your public and secret API keys (secret is masked by default)</li>
					<li><strong>Regenerate keys</strong> &mdash; generate a new secret key instantly. The old key stops working immediately.</li>
				</ul>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>Regenerating a key is irreversible.</strong> All applications using the old key will lose access. Update your environment variables and redeploy before regenerating.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Danger zone</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					At the bottom of the settings page is the <strong>danger zone</strong> where you can permanently delete the project. This removes all data, files, users, and configuration. This action cannot be undone.
				</p>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('cli')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Team Collaboration &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 17. TEAM COLLABORATION ======================= -->
		<section id="team" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">19. Team Collaboration</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to give a colleague access to the project without sharing API keys.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The <strong>Members</strong> tab on the Settings page lets you invite team members to your project with role-based access control. Each member gets their own login — no shared credentials needed.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Roles</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase has four roles, ordered from least to most permissions:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Viewer</strong> &mdash; read-only access to data, logs, and compliance reports</li>
					<li><strong>Developer</strong> &mdash; viewer + can edit data, manage schema (create/drop tables, columns), and manage edge functions</li>
					<li><strong>Admin</strong> &mdash; developer + can change project settings, regenerate API keys, manage vault secrets, and invite or remove members</li>
					<li><strong>Owner</strong> &mdash; admin + can delete the project and change other members' roles. Every project has exactly one owner (the creator).</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Inviting members</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Go to <strong>Settings &rarr; Members</strong>, enter an email address, select a role, and click <strong>Send Invite</strong>. The recipient receives an email with an invitation link that expires in 7 days. If they don't have a Eurobase account yet, they'll need to sign up first, then click the link again.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Managing members</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Change role</strong> &mdash; only the project owner can change a member's role using the dropdown in the members table</li>
					<li><strong>Remove</strong> &mdash; only the project owner can remove members. Members cannot remove themselves.</li>
					<li><strong>Resend invitation</strong> &mdash; if an invitation hasn't been accepted, click Resend to generate a fresh token and re-send the email</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">How it works</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Once invited, a member sees the project in their project list alongside any projects they own. All member actions are recorded in the <strong>Compliance &rarr; Audit Log</strong>: invitations sent, accepted, roles changed, and members removed.
				</p>

				<div class="rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-blue-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-blue-800">
						Members share the project's plan and API keys. They can access all data in the project's database and storage based on their role. Individual API keys per member are a future feature.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('cli')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: CLI Tool &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 18. CLI TOOL ======================= -->
		<section id="cli" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">20. CLI Tool</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to manage projects, run queries, and test RLS policies from the terminal.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Eurobase CLI lets you manage your projects, database, storage, vault, and more from the command line. Install it via Homebrew or download the Go binary.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Installation</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400">
					<div>brew install stgime/tap/eurobase</div>
					<div class="mt-1 text-gray-500"># or download a binary: github.com/STGime/homebrew-tap/releases</div>
					<div class="mt-1 text-gray-500"># macOS manual download only: xattr -d com.apple.quarantine ./eurobase (not notarized; brew installs unaffected)</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Getting started</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5">
					<div class="text-gray-500"># Log in to your account</div>
					<div>eurobase login</div>
					<div class="mt-2 text-gray-500"># List your projects</div>
					<div>eurobase projects list</div>
					<div class="mt-2 text-gray-500"># Set the active project</div>
					<div>eurobase switch my-project</div>
					<div class="mt-2 text-gray-500"># See project status and usage</div>
					<div>eurobase status</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Command reference</h3>
				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Command</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Auth & Projects</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">login</td><td class="px-3 py-1 text-gray-500">Sign in with email and password</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">logout</td><td class="px-3 py-1 text-gray-500">Clear stored credentials</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">version</td><td class="px-3 py-1 text-gray-500">Print the CLI version</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">projects list</td><td class="px-3 py-1 text-gray-500">List all projects</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">projects create &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Create a new project</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">projects delete &lt;id&gt;</td><td class="px-3 py-1 text-gray-500">Delete a project (requires --confirm)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">switch &lt;slug&gt;</td><td class="px-3 py-1 text-gray-500">Set active project</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">status</td><td class="px-3 py-1 text-gray-500">Show usage and plan info</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Database</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db tables</td><td class="px-3 py-1 text-gray-500">List tables (excludes system tables)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db schema [table]</td><td class="px-3 py-1 text-gray-500">Show columns and types</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db query "SQL"</td><td class="px-3 py-1 text-gray-500">Execute SQL and print results</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db create-table &lt;name&gt; &lt;col:type&gt;...</td><td class="px-3 py-1 text-gray-500">Create a table with RLS preset</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db add-column &lt;table&gt; &lt;col:type&gt;</td><td class="px-3 py-1 text-gray-500">Add a column</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db drop-column &lt;table&gt; &lt;column&gt;</td><td class="px-3 py-1 text-gray-500">Drop a column</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db drop-table &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Drop a table</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db dump</td><td class="px-3 py-1 text-gray-500">Export schema as text</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Schema Migrations</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">migrations new &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Create the next numbered migration file in ./migrations</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">migrations up</td><td class="px-3 py-1 text-gray-500">Apply pending migrations to the active project (idempotent)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">migrations status</td><td class="px-3 py-1 text-gray-500">Local files vs applied versions</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Keys & Config</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">keys show</td><td class="px-3 py-1 text-gray-500">Display API keys</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">keys regenerate</td><td class="px-3 py-1 text-gray-500">Rotate API keys</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">init</td><td class="px-3 py-1 text-gray-500">Generate .env, CLAUDE.md, .cursorrules</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Logs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">logs</td><td class="px-3 py-1 text-gray-500">Show recent request logs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">logs --tail</td><td class="px-3 py-1 text-gray-500">Stream logs in real time</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Vault</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault list</td><td class="px-3 py-1 text-gray-500">List secret names</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault get &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Get decrypted value</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault set &lt;name&gt; &lt;value&gt;</td><td class="px-3 py-1 text-gray-500">Store a secret</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault delete &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Delete a secret</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Edge Functions (serverless TypeScript/JavaScript)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions list</td><td class="px-3 py-1 text-gray-500">List deployed edge functions</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions deploy &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Deploy from functions/&lt;name&gt;.ts (or --file); --no-verify-jwt for public functions</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions get &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Show details and source code</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions invoke &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Invoke with optional --data JSON body</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions logs &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">View invocation logs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions delete &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Delete an edge function</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Cron & RPC Functions</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">cron list</td><td class="px-3 py-1 text-gray-500">List scheduled jobs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">cron logs &lt;id&gt;</td><td class="px-3 py-1 text-gray-500">Show run history</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions list</td><td class="px-3 py-1 text-gray-500">List RPC functions (Postgres)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions create &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Create from file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions delete &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Drop function</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Storage</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage ls</td><td class="px-3 py-1 text-gray-500">List files</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage upload &lt;local&gt; &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Upload a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage download &lt;key&gt; &lt;local&gt;</td><td class="px-3 py-1 text-gray-500">Download a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage delete &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Delete a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage url &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Generate signed URL</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Testing & Compliance</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">test [file-or-dir]</td><td class="px-3 py-1 text-gray-500">Run pgTAP database tests</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">compliance report</td><td class="px-3 py-1 text-gray-500">Generate the DPA / sub-processor report</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">compliance sub-processors</td><td class="px-3 py-1 text-gray-500">List active sub-processors</td></tr>
						</tbody>
					</table>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Testing RLS policies with pgTAP</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Create SQL test files in a <code class="bg-gray-100 rounded px-1">tests/</code> directory. Each file uses pgTAP assertions to verify your RLS policies work correctly.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>-- tests/rls_tasks.sql
BEGIN;
SELECT plan(3);

-- Test as Alice
SET LOCAL app.end_user_id = 'alice-uuid';

SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'alice-uuid') > 0,
    'Alice can see her own tasks'
);

SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'bob-uuid') = 0,
    'Alice cannot see Bob tasks'
);

-- Test as anonymous
SET LOCAL app.end_user_id = '';
SELECT ok(
    (SELECT count(*) FROM tasks) = 0,
    'Anonymous cannot see any tasks'
);

SELECT * FROM finish();
ROLLBACK;</pre>
				</div>

				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 mt-3">
					<div class="text-gray-500"># Run all tests</div>
					<div>eurobase test</div>
					<div class="mt-1 text-gray-500"># Run a specific test file</div>
					<div>eurobase test tests/rls_tasks.sql</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Tip:</strong> Tests run inside a transaction that is rolled back &mdash; no data is modified. Use <code class="bg-white/50 rounded px-1">SET LOCAL app.end_user_id</code> to simulate different users and verify RLS policies enforce correct access.</p>
					</div>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('connect')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Connecting Your IDE &rarr;
				</button>
			</div>
		</section>


		<!-- ======================= 19. SCHEMA MIGRATIONS ======================= -->
		<section id="migrations" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">21. Schema Migrations</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants schema changes that are versioned, reviewable, and repeatable across environments.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Migrations are versioned SQL files in your project's <code class="bg-gray-100 rounded px-1">migrations/</code> directory, applied in order against your project's database schema. The platform records what has been applied, so <code class="bg-gray-100 rounded px-1">migrations up</code> only runs what's pending — re-running it is always safe, locally or in CI. Use migrations for anything the table editor doesn't cover: composite unique constraints, CHECK constraints, partial indexes, triggers, backfills.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">The workflow</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5">
					<div class="text-gray-500"># 1. Create the next numbered migration file</div>
					<div>eurobase migrations new create_listings</div>
					<div class="mt-2 text-gray-500"># 2. Write your SQL in migrations/0001_create_listings.sql, then apply</div>
					<div>eurobase migrations up</div>
					<div class="mt-2 text-gray-500"># 3. See what's applied vs pending</div>
					<div>eurobase migrations status</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">A realistic example</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					<code class="bg-gray-100 rounded px-1">migrations/0001_create_listings.sql</code> — a table with a composite natural key, a CHECK constraint, a partial index, and an RLS policy:
				</p>
				<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<pre>CREATE TABLE listings (
    id          uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
    user_id     uuid NOT NULL,
    source      text NOT NULL,
    source_id   text NOT NULL,
    status      text NOT NULL DEFAULT 'active'
                CHECK (status IN ('active', 'expired', 'flagged')),
    price       numeric(8,2),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, source_id)          -- composite natural key: no dupes per source
);

CREATE INDEX idx_listings_active
    ON listings (user_id, created_at)
    WHERE status = 'active';            -- partial index: hot rows only

ALTER TABLE listings ENABLE ROW LEVEL SECURITY;
CREATE POLICY listings_owner ON listings
    USING (public.is_service_role() OR user_id = public.current_end_user_id())
    WITH CHECK (public.is_service_role() OR user_id = public.current_end_user_id());</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">How it executes</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li>Each migration runs in <strong>one transaction</strong> — if any statement fails, nothing from that file is applied.</li>
					<li>It runs inside <strong>your project schema</strong>: use unqualified table names. Multi-statement files are expected.</li>
					<li>Applied versions are recorded with a checksum. Re-applying an identical file is a no-op; <strong>editing an already-applied file is rejected</strong> — add a new version instead.</li>
					<li>There are no down migrations — to undo something, write a new forward migration.</li>
					<li>Concurrent runs are serialized per project, so two CI jobs can't race each other.</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Running migrations in CI</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Authenticate with a Personal Access Token (Account &rarr; Tokens) stored as a CI secret. Example GitHub Actions step:
				</p>
				<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<pre>- name: Apply database migrations
  run: |
    brew install stgime/tap/eurobase   # or download the binary
    eurobase login --token "$EUROBASE_PAT"
    eurobase switch my-project
    eurobase migrations up
  env:
    EUROBASE_PAT: $&#123;&#123; secrets.EUROBASE_PAT &#125;&#125;</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">What migration SQL can't do (and why)</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Migrations run with your project's developer credentials (PAT or console session) — <strong>never with API keys</strong>: a leaked server key must not be able to alter your schema. Each migration executes under a database role scoped to <em>only your project's schema</em>, so it physically cannot read or write another project or the platform's own tables — that boundary is enforced by Postgres, not just by validation. On top of that, the platform rejects operations that try to reach outside your project before they run:
				</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li>References to other schemas (<code class="bg-gray-100 rounded px-1">public.*</code>, <code class="bg-gray-100 rounded px-1">pg_catalog</code>, other tenants). Exception: the RLS helpers <code class="bg-gray-100 rounded px-1">public.is_service_role()</code>, <code class="bg-gray-100 rounded px-1">public.current_end_user_id()</code>, and <code class="bg-gray-100 rounded px-1">public.uuid_generate_v4()</code>.</li>
					<li><code class="bg-gray-100 rounded px-1">GRANT</code>/<code class="bg-gray-100 rounded px-1">REVOKE</code>, <code class="bg-gray-100 rounded px-1">SET ROLE</code>/<code class="bg-gray-100 rounded px-1">search_path</code> — access control belongs to the platform.</li>
					<li>Transaction control (<code class="bg-gray-100 rounded px-1">COMMIT</code>, <code class="bg-gray-100 rounded px-1">BEGIN</code>) — your file already runs in a transaction.</li>
					<li><code class="bg-gray-100 rounded px-1">SECURITY DEFINER</code> functions, <code class="bg-gray-100 rounded px-1">CREATE EXTENSION</code>, <code class="bg-gray-100 rounded px-1">COPY</code>.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">
					Plain <code class="bg-gray-100 rounded px-1">LANGUAGE plpgsql</code> functions and triggers are fine. Bulk data loads belong in the SDK/REST data path, not migrations.
				</p>
			</div>
		</section>

		<!-- ======================= 16. CONNECTING YOUR IDE ======================= -->
		<section id="connect" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">22. Connecting Your IDE</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants their AI coding assistant to understand the LexVault schema.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Connect page generates ready-to-use configuration files for popular AI-powered IDEs and coding tools. These configs give your AI assistant context about your project's schema, API endpoints, and connection details.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Supported tools</h3>

				<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
					<div class="px-5 py-3 border-b border-gray-100">
						<h4 class="text-sm font-semibold text-gray-900">IDE configurations</h4>
					</div>
					<div class="p-5 space-y-3">
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Claude Code</strong> &mdash; generates a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">CLAUDE.md</code> with your schema and API reference</p>
						</div>
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Cursor</strong> &mdash; generates a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.cursor/rules</code> file</p>
						</div>
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Windsurf</strong> &mdash; generates a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.windsurfrules</code> file</p>
						</div>
						<div class="flex items-start gap-2">
							<span class="text-eurobase-600 mt-0.5"><svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg></span>
							<p class="text-sm text-gray-700"><strong>Generic</strong> &mdash; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.env</code> template and raw connection strings</p>
						</div>
					</div>
				</div>

				<p class="text-sm text-gray-700 leading-relaxed">
					Each tab shows a preview of the generated config, a copy button, and a download button. Just paste the file into your project root and your AI assistant will have full context about your Eurobase project.
				</p>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						The generated configs include your API URL and public key but <strong>never your secret key</strong>. Add secret keys to your <code class="rounded bg-eurobase-100 px-1.5 py-0.5 text-xs font-mono text-eurobase-700">.env</code> file manually and ensure it's in your <code class="rounded bg-eurobase-100 px-1.5 py-0.5 text-xs font-mono text-eurobase-700">.gitignore</code>.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('mcp')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: MCP Server &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 20. MCP SERVER ======================= -->
		<section id="mcp" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">23. MCP Server</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants their AI assistant to actually <em>do things</em> in LexVault &mdash; list users, run a SELECT, rotate a Vault secret &mdash; not just read schema docs.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The configs in <button onclick={() => scrollTo('connect')} class="text-eurobase-600 hover:underline cursor-pointer">section 19</button> teach an AI assistant <em>about</em> your project. The MCP server lets it <em>operate</em> on your project. Eurobase ships a hosted <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener" class="text-eurobase-600 hover:underline">Model Context Protocol</a> server at <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">https://mcp.eurobase.app/mcp</code> that exposes the platform API as tool calls.
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
				<button onclick={() => scrollTo('account')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Your Account &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 21. YOUR ACCOUNT ======================= -->
		<section id="account" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">24. Your Account</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to set a display name and update their password.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Account page (accessible from the main sidebar) manages your <strong>platform account</strong> &mdash; the one you use to sign into the Eurobase console itself.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Profile information</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Email</strong> &mdash; your login email (read-only)</li>
					<li><strong>Display name</strong> &mdash; set a friendly name that appears in the console header</li>
					<li><strong>Member since</strong> &mdash; your account creation date</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Change password</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Enter your current password, then your new password twice. The new password must be at least 8 characters.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Personal Access Tokens</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Long-lived bearer tokens for tooling that needs to act as you outside the browser &mdash; the <button onclick={() => scrollTo('mcp')} class="text-eurobase-600 hover:underline cursor-pointer">MCP server</button>, the CLI, CI pipelines, scripts. Mint and revoke them in the <strong>Personal Access Tokens</strong> card on the Account page.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>Creating a token:</strong> click <em>+ New token</em>, give it a memorable name (e.g. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">my laptop</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">ci-prod</code>), optionally set an expiry date, and click <em>Create</em>. The plaintext token (e.g. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">eb_pat_205d71172180a3ca3ea0e26f07156429</code>) appears in an amber banner once. Copy it immediately into a password manager or your shell rc &mdash; <strong>it is not retrievable afterwards.</strong> The console only stores a SHA-256 hash.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>What a PAT can do</strong>: read and write any project you own or are a member of, via the SDK, the platform API, or the MCP server. It authenticates as you across every project, with the same RLS and role checks the rest of the platform enforces.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>What a PAT can't do</strong>:
				</p>
				<ul class="text-sm text-gray-700 space-y-1 ml-4 list-disc">
					<li>Reach the platform admin surface (allowlist management, cross-tenant project list). PATs never carry the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">is_superadmin</code> claim, even when minted by a superadmin.</li>
					<li>Mint other PATs &mdash; only an authenticated browser session can do that. Limits the blast radius of a leaked token.</li>
					<li>Change your password or delete your account.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>Revoking</strong>: click <em>Revoke</em> next to any token. Revocation is immediate &mdash; the next request using that token returns 401. There's no grace period.
				</p>
				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						Treat PATs like passwords. If a token leaks (commit, screenshot, screen-share), revoke and rotate it. Tokens listed on this page show their last-used timestamp so you can spot inactive ones to clean up.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Delete account</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					At the bottom of the page, you can permanently delete your platform account. You'll need to type your email address to confirm. This also deletes all projects owned by the account.
				</p>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>Account deletion is permanent.</strong> All projects, databases, files, and user data under this account will be irreversibly destroyed.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('next')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: What's Next &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= WHAT'S NEXT ======================= -->
		<section id="next" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">What's Next</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex has a fully configured backend. Time to build LexVault's frontend.</p>

			<div class="space-y-4">
				<div class="rounded-xl border border-gray-200 bg-white p-6">
					<h3 class="text-base font-semibold text-gray-900 mb-3">What Alex built</h3>
					<div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">PostgreSQL database with tables</span>
						</div>
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">File storage for legal docs</span>
						</div>
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">User authentication</span>
						</div>
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">REST API with auto-generated docs</span>
						</div>
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">Webhooks for real-time events</span>
						</div>
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">Request monitoring &amp; logs</span>
						</div>
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">IDE integration configs</span>
						</div>
						<div class="flex items-center gap-2">
							<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" /></svg>
							<span class="text-sm text-gray-700">100% EU-sovereign infrastructure</span>
						</div>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900">Quick links</h3>
				<div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
					<a href="/projects" class="rounded-lg border border-gray-200 bg-white px-4 py-3 text-sm font-medium text-gray-700 hover:border-eurobase-300 hover:text-eurobase-700 transition-colors">
						Your Projects &rarr;
					</a>
					<a href="/account" class="rounded-lg border border-gray-200 bg-white px-4 py-3 text-sm font-medium text-gray-700 hover:border-eurobase-300 hover:text-eurobase-700 transition-colors">
						Account Settings &rarr;
					</a>
				</div>

				<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/50 p-6 mt-4">
					<div class="flex gap-3">
						<svg class="h-6 w-6 text-eurobase-600 shrink-0" viewBox="0 0 24 24" fill="currentColor">
							<circle cx="12" cy="12" r="10" fill="none" stroke="currentColor" stroke-width="1.5"/>
							<circle cx="12" cy="5" r="0.8" />
							<circle cx="15.5" cy="6.3" r="0.8" />
							<circle cx="17.7" cy="9.5" r="0.8" />
							<circle cx="17.7" cy="14.5" r="0.8" />
							<circle cx="15.5" cy="17.7" r="0.8" />
							<circle cx="12" cy="19" r="0.8" />
							<circle cx="8.5" cy="17.7" r="0.8" />
							<circle cx="6.3" cy="14.5" r="0.8" />
							<circle cx="6.3" cy="9.5" r="0.8" />
							<circle cx="8.5" cy="6.3" r="0.8" />
						</svg>
						<div>
							<h3 class="text-sm font-semibold text-eurobase-900 mb-1">EU-Sovereign by design</h3>
							<p class="text-sm text-eurobase-800">
								Everything Alex built runs entirely on EU infrastructure. The database is in Scaleway Paris, files are in Scaleway Object Storage, and authentication is handled by custom Go services &mdash; no AWS, GCP, Azure, or any US CLOUD Act-subject provider touches the data. LexVault's law firm clients can rest easy.
							</p>
						</div>
					</div>
				</div>
			</div>
		</section>

	</div>
</div>
