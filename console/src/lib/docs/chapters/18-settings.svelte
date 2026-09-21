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
				<a href="/docs/cli" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Team Collaboration &rarr;
				</a>
			</div>
		</section>
