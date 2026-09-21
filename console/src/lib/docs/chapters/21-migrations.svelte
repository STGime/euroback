<script lang="ts">
</script>

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
