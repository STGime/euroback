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
				<a href="/docs/api" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Exploring the API &rarr;
				</a>
			</div>
		</section>
