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
					<li><strong>Mark confirmed</strong> &mdash; confirm a user's email address by hand (for users who existed before you turned on email confirmation, or can't receive the email)</li>
					<li><strong>Suspend / reactivate</strong> &mdash; temporarily block a user from signing in (revokes all refresh tokens)</li>
					<li><strong>Delete user</strong> &mdash; permanently remove a user account</li>
					<li><strong>Reset password</strong> &mdash; set a new password for a user directly (revokes all refresh tokens)</li>
					<li><strong>Metadata JSON</strong> &mdash; attach arbitrary JSON metadata to any user (e.g., role, department, permissions)</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Account linking</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Accounts are <strong>not</strong> linked automatically. If someone signs in with a social provider (Google, GitHub, &hellip;) using an email that already has an account, the sign-in is refused with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">account_exists_link_required</code>: they sign in the way they signed up. Linking automatically would let whoever controls the provider account take over the existing one. Users who sign in with several providers show all of them in the Provider column.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Managing users from your server</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Everything on the Users page is also an API, for importing users or managing them from your own backend. These are <em>platform</em> endpoints on <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">https://api.eurobase.app/platform/projects/$PROJECT_ID/users</code>, authenticated with a <strong>project token with the Admin role</strong> (Account &rarr; Personal Access Tokens &rarr; pick the project and <em>Admin</em>). Your project's public and secret keys (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb_pk_</code> / <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb_sk_</code>) can't call them, and there's no <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/v1/auth/admin</code> API. Keep the token on your server, like the secret key.
				</p>
				<div class="overflow-x-auto mt-2">
					<table class="min-w-full text-sm text-gray-700">
						<thead><tr class="text-left text-xs uppercase tracking-wider text-gray-500"><th class="py-1.5 pr-4">Request</th><th class="py-1.5">Does</th></tr></thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /users?search=&amp;limit=&amp;offset=</code></td><td class="py-1.5">List users, newest first: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">{'{'}"users": [...], "total": n{'}'}</code>. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">limit</code> up to 200 (default 50). <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">search</code> matches part of the email, display name or id.</td></tr>
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /users/{'{'}id{'}'}</code></td><td class="py-1.5">One user</td></tr>
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST /users</code></td><td class="py-1.5">Create a user with a confirmed email: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">{'{'}"email", "password", "metadata"{'}'}</code> (password at least 8 characters)</td></tr>
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">PATCH /users/{'{'}id{'}'}</code></td><td class="py-1.5">Change <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">display_name</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">metadata</code> (replaces the whole object), or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed</code> (true / false)</td></tr>
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">DELETE /users/{'{'}id{'}'}</code></td><td class="py-1.5">Delete the user (refused while a retention hold covers them)</td></tr>
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST</code> / <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">DELETE /users/{'{'}id{'}'}/suspend</code></td><td class="py-1.5">Suspend (revokes their refresh tokens) / reactivate</td></tr>
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST /users/{'{'}id{'}'}/reset-password</code></td><td class="py-1.5">Set a new password: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">{'{'}"password"{'}'}</code> (revokes their refresh tokens)</td></tr>
							<tr><td class="py-1.5 pr-4"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /users/{'{'}id{'}'}/export</code></td><td class="py-1.5">The user's data (GDPR Art. 15)</td></tr>
						</tbody>
					</table>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Each user has <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">id</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">phone</code> (when set), <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">display_name</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">providers</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">metadata</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">banned_at</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">last_sign_in_at</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed_at</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">created_at</code>.
				</p>
				<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>API=https://api.eurobase.app/platform/projects/$PROJECT_ID/users
AUTH="Authorization: Bearer $EUROBASE_PROJECT_TOKEN"   # eb_ptk_&hellip;, Admin role

# find a user by email
curl -G "$API" -H "$AUTH" --data-urlencode "search=ann@example.com"

# create one (email confirmed)
curl -X POST "$API" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{'{'}"email":"ann@example.com","password":"&lt;random, 16–72 chars&gt;","metadata":{'{'}"role":"member"{'}'}{'}'}'

# change their email
curl -X PATCH "$API/$USER_ID" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{'{'}"email":"ann@new.example.com"{'}'}'

# delete
curl -X DELETE "$API/$USER_ID" -H "$AUTH"</pre>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					<strong>Importing existing members:</strong> create each user with a random password you don't keep (16–72 characters), then let them in with a magic link (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.requestMagicLink(email)</code>) or a password reset. Coming from Supabase, the migration CLI's <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">import supabase</code> (in the next CLI release) imports users with their password hashes instead. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">search</code> matches parts of addresses (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">ann@x.com</code> also finds <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">joann@x.com</code>), so compare the email in the results.
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
				<a href="/docs/api" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Exploring the API &rarr;
				</a>
			</div>
		</section>
