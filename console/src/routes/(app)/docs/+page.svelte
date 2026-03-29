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
		{ id: 'users', label: '7. Managing End Users' },
		{ id: 'api', label: '8. Exploring the API' },
		{ id: 'webhooks', label: '9. Webhooks' },
		{ id: 'rls', label: '10. Row-Level Security' },
		{ id: 'vault', label: '11. Vault (Secrets)' },
		{ id: 'cron', label: '12. Scheduled Jobs' },
		{ id: 'logs', label: '13. Monitoring with Logs' },
		{ id: 'settings', label: '14. Project Settings' },
		{ id: 'connect', label: '15. Connecting Your IDE' },
		{ id: 'account', label: '16. Your Account' },
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
					Eurobase uses a simple email-and-password system. There are no third-party OAuth providers &mdash; your credentials stay within EU infrastructure.
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

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Using the SDK</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("// Insert a client\nconst { data, error } = await eb.db\n  .from('clients')\n  .insert({ name: 'Acme Legal', email: 'info@acmelegal.eu', firm_name: 'Acme Legal GmbH' })\n\n// Read all clients\nconst { data: clients } = await eb.db\n  .from('clients')\n  .select('*')\n\n// Update a client\nawait eb.db\n  .from('clients')\n  .update({ plan: 'pro' })\n  .eq('email', 'info@acmelegal.eu')\n\n// Delete a client\nawait eb.db\n  .from('clients')\n  .delete()\n  .eq('id', 'some-uuid')", 'sdk-crud')}
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

// Update a client
await eb.db
  .from('clients')
  .update({'{'} plan: 'pro' {'}'})
  .eq('email', 'info@acmelegal.eu')

// Delete a client
await eb.db
  .from('clients')
  .delete()
  .eq('id', 'some-uuid')</pre>
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
						onclick={() => copyCode("// Upload a file\nconst file = document.getElementById('fileInput').files[0]\nconst { data, error } = await eb.storage\n  .upload('contracts/nda-acme.pdf', file)\n\n// Get a signed URL (1 hour expiry)\nconst { url } = await eb.storage\n  .getSignedUrl('contracts/nda-acme.pdf', { expiresIn: 3600 })\n\n// List files in a folder\nconst { data: files } = await eb.storage\n  .list('contracts/')", 'sdk-storage')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-storage' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// Upload a file
const file = document.getElementById('fileInput').files[0]
const {'{'} data, error {'}'} = await eb.storage
  .upload('contracts/nda-acme.pdf', file)

// Get a signed URL (1 hour expiry)
const {'{'} url {'}'} = await eb.storage
  .getSignedUrl('contracts/nda-acme.pdf', {'{'} expiresIn: 3600 {'}'})

// List files in a folder
const {'{'} data: files {'}'} = await eb.storage
  .list('contracts/')</pre>
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
					<li><strong>Passkeys</strong> &mdash; coming soon (WebAuthn / FaceID / fingerprint)</li>
					<li><strong>Social Login</strong> &mdash; coming soon (Google, GitHub)</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Configuration options</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Password rules</strong> &mdash; set minimum length (8&ndash;128 characters)</li>
					<li><strong>Email confirmation</strong> &mdash; require users to verify their email before signing in</li>
					<li><strong>Session duration</strong> &mdash; how long access tokens remain valid (1h to 30 days)</li>
					<li><strong>Redirect URLs</strong> &mdash; whitelist URLs your app can redirect to after auth callbacks</li>
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
				<button onclick={() => scrollTo('users')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Managing End Users &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 7. MANAGING END USERS ======================= -->
		<section id="users" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">7. Managing End Users</h2>
			<p class="text-sm italic text-gray-500 mb-4">A law firm onboards new employees. Alex needs to manage their accounts.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Users page shows every end-user registered in your project. From here you can create, edit, suspend, and delete accounts.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">User management features</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>User list</strong> &mdash; searchable table with email, status, and creation date</li>
					<li><strong>Create user</strong> &mdash; manually add a user with email and password</li>
					<li><strong>Edit user</strong> &mdash; update email, metadata, or password</li>
					<li><strong>Suspend / reactivate</strong> &mdash; temporarily block a user from signing in</li>
					<li><strong>Delete user</strong> &mdash; permanently remove a user account</li>
					<li><strong>Reset password</strong> &mdash; set a new password for a user directly</li>
					<li><strong>Metadata JSON</strong> &mdash; attach arbitrary JSON metadata to any user (e.g., role, department, permissions)</li>
				</ul>

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
			<h2 class="text-2xl font-bold text-gray-900 mb-1">8. Exploring the API</h2>
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
			<h2 class="text-2xl font-bold text-gray-900 mb-1">9. Webhooks</h2>
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
			<h2 class="text-2xl font-bold text-gray-900 mb-1">10. Row-Level Security (RLS)</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs each law firm employee to only see their own cases.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Row-Level Security lets you control which rows each user can read, insert, update, or delete. Policies are written in SQL and enforced by PostgreSQL itself &mdash; no application code needed.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Auth helper functions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase provides built-in functions you can use in RLS policies to access the current user's identity:
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
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_uid()</td><td class="px-3 py-1.5 text-gray-500">uuid</td><td class="px-3 py-1.5 text-gray-500">Current user's ID</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_email()</td><td class="px-3 py-1.5 text-gray-500">text</td><td class="px-3 py-1.5 text-gray-500">Current user's email</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_role()</td><td class="px-3 py-1.5 text-gray-500">text</td><td class="px-3 py-1.5 text-gray-500">'authenticated' or 'anon'</td></tr>
						</tbody>
					</table>
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

				<h3 class="text-lg font-semibold text-gray-900 mt-6">How to enable RLS on a custom table</h3>
				<ol class="text-sm text-gray-700 space-y-1.5 ml-4 list-decimal">
					<li>Create the table with a <code class="bg-gray-100 rounded px-1">user_id</code> UUID column (FK to users.id)</li>
					<li>Enable RLS: <code class="bg-gray-100 rounded px-1">ALTER TABLE my_table ENABLE ROW LEVEL SECURITY;</code></li>
					<li>Add policies using the auth helper functions</li>
					<li>Test: sign in as a user, query the table &mdash; only matching rows are returned</li>
				</ol>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Supabase compatibility:</strong> Eurobase's <code class="bg-white/50 rounded px-1">auth_uid()</code>, <code class="bg-white/50 rounded px-1">auth_role()</code>, and <code class="bg-white/50 rounded px-1">auth_email()</code> follow the same pattern as Supabase's GoTrue. RLS policies written for Supabase work in Eurobase with minimal changes.</p>
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
			<h2 class="text-2xl font-bold text-gray-900 mb-1">11. Vault (Encrypted Secrets)</h2>
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
			<h2 class="text-2xl font-bold text-gray-900 mb-1">12. Scheduled Jobs</h2>
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
				<button onclick={() => scrollTo('logs')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Monitoring with Logs &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 11. MONITORING WITH LOGS ======================= -->
		<section id="logs" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">13. Monitoring with Logs</h2>
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
				<button onclick={() => scrollTo('settings')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Project Settings &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 11. PROJECT SETTINGS ======================= -->
		<section id="settings" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">14. Project Settings</h2>
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
				<button onclick={() => scrollTo('connect')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Connecting Your IDE &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 12. CONNECTING YOUR IDE ======================= -->
		<section id="connect" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">15. Connecting Your IDE</h2>
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
				<button onclick={() => scrollTo('account')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Your Account &rarr;
				</button>
			</div>
		</section>

		<!-- ======================= 13. YOUR ACCOUNT ======================= -->
		<section id="account" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">16. Your Account</h2>
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
