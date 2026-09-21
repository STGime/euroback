<script lang="ts">
	import { goto } from '$app/navigation';
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
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

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
