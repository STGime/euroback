		<section id="migrating-from-supabase" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">30. Migrating from Supabase (FAQ)</h2>
			<p class="text-sm italic text-gray-500 mb-4">Common questions from teams moving an existing Supabase project to Eurobase — what ports cleanly, what needs a rewrite, and where we're honest about the differences.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase is close to Supabase in shape — Postgres, Row-Level Security, an SDK with a familiar API, EU storage with signed URLs — but it is not a drop-in replacement. A migration CLI (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase import supabase</code>) automates most of a move, starting with an <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">assess</code> step that reports what transfers cleanly and what needs changes. It's rolling out now — <a href="mailto:contact@eurobase.app" class="cursor-pointer text-eurobase-700 hover:underline">get in touch</a> and we'll run an assessment against your project. The answers below are deliberately straight about the gaps.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-2">Migrating in</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I import my existing users without password resets?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. We use bcrypt in the same format as Supabase, so password hashes import as-is and your users keep their passwords. The importer preserves user UUIDs (so RLS keeps working), email-confirmed status, metadata, and OAuth identities. Without the CLI, the users API's batch import takes the same bcrypt hashes and ids (see below).</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I keep using <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">supabase-js</code>?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">No — you switch to <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code>. It is not wire-compatible, but the API shape is deliberately familiar (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">createClient</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.signUp</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.from()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.rpc()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.storage</code>), so for a typical app it is a contained change.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I run a trial alongside my current setup?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. Your account can have up to 10 projects on Pro, so you can stand up a test project next to your live backend and trial the migration before switching anything.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Database &amp; auth differences</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Is Row-Level Security supported?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes — RLS is the core authorization model, with the same helpers you know (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.uid()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.role()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.email()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">is_service_role()</code>). The importer auto-translates Supabase policies, and the service (secret) key bypasses RLS like Supabase's service role.</p>
				</div>

				<div class="rounded-xl border border-amber-200 bg-amber-50 p-5">
					<p class="text-sm font-semibold text-gray-900">Are <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">SECURITY DEFINER</code> functions supported?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Not on self-serve surfaces — this is the biggest difference to plan for. Re-express them as plain (invoker) functions plus RLS, as server-side code using the service key, or as edge functions. Plain functions and trigger functions port fine.</p>
				</div>

				<div class="rounded-xl border border-amber-200 bg-amber-50 p-5">
					<p class="text-sm font-semibold text-gray-900">Does the <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">trigger on auth.users</code> (profile-on-signup) pattern work?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">No — the users table is platform-managed, so you can't attach a trigger to it. You can still reference it (<code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">REFERENCES users(id)</code>) and read it server-side. Create the profile row in your signup code or an edge function; list and delete users through the admin API.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I see the source of a database function?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Click it under Cron &amp; RPC &rarr; Functions, or run <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase functions get &lt;name&gt;</code>. You get the full <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">CREATE OR REPLACE FUNCTION</code> statement; edit and run it in the SQL editor to change the function. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">pg_get_functiondef()</code> and other system-catalog queries are refused in your own SQL. See <a href="/docs/cron" class="cursor-pointer text-eurobase-700 hover:underline">Cron &amp; RPC</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I keep a test project apart from production in the CLI?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Log in with a personal access token scoped to the test project, under its own <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">HOME</code>: the CLI keeps its login and active project only in <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">$HOME/.eurobase/config.json</code>, and a scoped token is refused for every other project. See <a href="/docs/cli" class="cursor-pointer text-eurobase-700 hover:underline">CLI</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I copy my schema to a test project without the data?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. Apply your migration files to a fresh, empty project (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase migrations up</code>) &mdash; you get tables, foreign keys, CHECK constraints, RLS policies and functions, with no rows copied. There&rsquo;s no standalone schema-only export yet; on Team you can also <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">pg_dump --schema-only</code> over the direct connection. See <a href="/docs/migrations" class="cursor-pointer text-eurobase-700 hover:underline">Migrations</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I see my full RLS policies for a table?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Use the schema endpoints rather than catalog SQL (which is refused): <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /platform/projects/&lt;id&gt;/schema/tables/&lt;table&gt;/policies</code> returns each policy&rsquo;s USING and WITH CHECK (developer role); <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET .../schema</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.../schema/rls-audit</code> give foreign keys and RLS status at viewer level. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET .../schema/functions/&lt;name&gt;</code> returns a function&rsquo;s definition, including trigger functions and the platform helpers <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_uid()</code> / <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_role()</code> / <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_email()</code>. See <a href="/docs/rls" class="cursor-pointer text-eurobase-700 hover:underline">Row-Level Security</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I make several writes atomic without client transactions?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Every API request runs in its own transaction. Put the related writes in a trigger (it commits or rolls back with the write that fired it) or in an RPC function (the whole function is one transaction; an exception undoes everything). Both run with the caller&rsquo;s rights, so RLS applies; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SECURITY DEFINER</code> isn&rsquo;t accepted. See <a href="/docs/rls" class="cursor-pointer text-eurobase-700 hover:underline">Row-Level Security</a> for a change-history example.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I send auth emails (reset, magic link) to my own site, with my own templates and sender?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Redirect URLs to your own app are fully configurable (allow-listed, same idea as Supabase's site URL). Custom email templates are available on Pro. A custom SMTP sender is a Pro feature we're finalising — validate your provider in a test project before relying on it.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Private storage with signed URLs?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes — a private per-project EU bucket with signed download URLs. Access differs from Supabase's <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage.foldername()</code> policies: files are owned per end-user automatically, with a separate mechanism for developer-shared folders (authenticated or public). SDK signed-URL expiry caps at 1 hour, so long playback sessions re-issue the URL.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Auth from your server</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I create or import users from my server, e.g. with their email already confirmed?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes, with the users API and a <strong>project token with the Admin role</strong> (not the secret key &mdash; there's no <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/v1/auth/admin</code>). <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST /platform/projects/&lt;id&gt;/users</code> creates one user; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST …/users/batch</code> imports up to 500 per request. The password is optional (users then sign in by magic link or set one with a password reset), <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed</code> defaults to true, and a batch can carry bcrypt <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">password_hash</code>es and keep existing user ids. Re-running an import is safe. See <a href="/docs/users" class="cursor-pointer text-eurobase-700 hover:underline">Managing End Users</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I fetch, list, change and delete users from my server?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes, the same API: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET …/users?email=</code> (exact) or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">?search=</code>, a list with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">last_sign_in_at</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed_at</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">PATCH …/users/&lt;id&gt;</code> to change the email, name, metadata or confirmation, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">DELETE</code>, suspend and reset password.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I verify access tokens on my server without a network call?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. Access tokens are ES256-signed with your project's own key pair; the public key is at <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">https://your-project.eurobase.app/v1/auth/.well-known/jwks.json</code>. Verify with any JWT library that supports JWKS (issuer <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase</code>, audience = your project ID); see <a href="/docs/auth" class="text-eurobase-600 hover:underline cursor-pointer">chapter 6</a> for an example. A locally verified token stays valid until it expires, so use <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /v1/auth/user</code> where you need to confirm the user still exists and read their current profile.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">With email confirmation on, users who haven't confirmed get "invalid email or password". Why?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">That changed: signing in with the right password before confirming now answers <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">403 email_not_confirmed</code> (SDK: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">EMAIL_NOT_CONFIRMED</code>), so you can offer <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">resendVerification(email)</code>. Sign-up returns no session until the user confirms. Whether a user has confirmed is in <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed_at</code>. Use <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code> 0.10.0 or later with this setting.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can a signed-in user change their password?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST /v1/auth/user/password</code> with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">current_password</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">new_password</code> and the user's access token. Their other sessions end once their current access token expires, and the response is a fresh session. An SDK helper (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">updatePassword</code>) needs <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code> 0.11.0 or later.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What rate limits should I set for a launch or migration day?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">When many users sign up or sign in at once, raise sign-ups / sign-ins to 30 and token verifications (which include magic-link sign-ins) to 150 per 5 minutes under Auth &rarr; Rate Limits beforehand. See <a href="/docs/rate-limits" class="cursor-pointer text-eurobase-700 hover:underline">Rate Limits</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Is there an hourly limit on auth emails with the built-in sender?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">No hourly cap on the number of emails sent. Per address: 3 magic links and 3 password-reset emails per 15 minutes, and one verification resend per 5 minutes. Per client IP: 60 password-reset and 60 verification emails an hour, and 300 magic-link requests an hour. For a migration day, magic links (300/h) go further than mass password resets (60/h).</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Reliability, backups &amp; leaving</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What backups and uptime do I get?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">The managed database is backed up automatically about every 24 hours at the infrastructure level on every tier (worst-case data loss ≈ 24 hours). Customer-controlled backups and self-serve restore are a Team-tier feature (dedicated EU database). Pro carries a 99.5% monthly availability target — see the <a href="/legal/sla" class="cursor-pointer text-eurobase-700 hover:underline">SLA</a> for the full detail.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Do you provide a DPA and a sub-processor list?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes — an auto-generated DPA report per project, and a published, EU-only sub-processor list (Scaleway for compute/database/storage/email in France, Mollie for payments, GatewayAPI for SMS). See the <a href="/docs/compliance" class="cursor-pointer text-eurobase-700 hover:underline">Compliance</a> chapter.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What happens to my data if Eurobase shuts down?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">If we cease operations, we keep the Service and your data online and exportable for at least 90 days before any deletion, with email notice to project owners. Your data is portable at any time regardless: export your schema, all table data, and a storage manifest from the console or API.</p>
				</div>

				<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/50 p-5">
					<div class="flex gap-3">
						<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 0 1 3 12c0-1.605.42-3.113 1.157-4.418" />
						</svg>
						<p class="text-sm text-eurobase-800">
							Everything runs in the EU on Scaleway (France) — database, storage, and custom Go auth services — end to end, under EU jurisdiction. That data-sovereignty guarantee is the main reason most teams make this move.
						</p>
					</div>
				</div>
			</div>
		</section>
