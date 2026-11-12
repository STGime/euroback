		<section id="settings" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">18. Project Settings</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to rotate an API key after an intern accidentally committed it.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Settings page manages your project's API keys and provides administrative actions.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">API key management</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>View keys</strong> &mdash; the public key is shown in full (it ships in your app anyway; any project member, viewers included, can copy it). The secret key is shown only once, when it is created &mdash; afterwards just its prefix. A public key created before this was available appears in full after its first use.</li>
					<li><strong>Rotate the secret key</strong> (<strong>Rotate</strong> on the secret key) &mdash; replaces only the secret key, e.g. after it was exposed. The old secret key stops working with the next request; the public key stays the same, so apps that use only the public key keep working. The new secret key is shown once. Admins only; recorded in the audit log as <em>Secret Key Rotated</em>. CLI: <code class="rounded bg-gray-100 px-1 py-0.5 text-xs">eurobase keys rotate-secret --confirm</code>.</li>
					<li><strong>Regenerate keys</strong> &mdash; generate a new public and secret key pair instantly. The old keys stop working immediately &mdash; also the way to cut off a former member who copied the public key.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">
					Edge functions don't need the secret key: <code class="rounded bg-gray-100 px-1 py-0.5 text-xs">ctx.db.sql</code>, <code class="rounded bg-gray-100 px-1 py-0.5 text-xs">ctx.storage</code> and <code class="rounded bg-gray-100 px-1 py-0.5 text-xs">ctx.vault</code> already have full access to your project. Keep the secret key only in your own servers, and other credentials in the Vault.
				</p>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>Rotating or regenerating a key is irreversible.</strong> All applications using the old key will lose access. Have the new key ready to deploy (environment variables, servers) before you rotate.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Billing &amp; subscription</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					A project's plan and subscription are managed on the <strong>Billing</strong> page (Project &rarr; Billing). To stop paying for a Pro project without deleting it, use <strong>Cancel subscription</strong> there &mdash; by default it cancels at the end of the current billing period, so you keep Pro until the period you've already paid for runs out, then the project drops to Free. No new charges are made after you cancel.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					<strong>Payment method.</strong> The Billing page shows the card, PayPal account or bank account your subscription is charged to, and warns you when a card expires before the next charge. <strong>Change</strong> takes you to a Mollie checkout where you add a new card or PayPal account; nothing is charged, and from then on your payments go to the new one. Only the person paying for the subscription sees and changes it. While a payment is overdue, use <strong>Pay now</strong> instead (below).
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					<strong>If a payment fails</strong>, the project's owner gets an email and the Billing page shows <em>Payment failed</em> with the date the project would drop to Free. Depending on the reason, Mollie, our payment provider, retries the charge automatically, up to five times, once a day; a retry that goes through clears the warning. If the payment still hasn't come through 7 days after the first failure, the project moves to the Free plan. Nothing is deleted, and you can subscribe to Pro again at any time. The same applies if Mollie stops the subscription on its own, for example because the card or payment authorisation is no longer valid. To settle it before then, choose <strong>Pay now</strong> on the Billing page: you pay one month with the card or payment method you pick there, and future charges use it too. The new month starts on the day you pay. If a retry of the failed charge still goes through afterwards, it's refunded automatically.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Transferring ownership</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					The owner sees a <strong>Transfer ownership</strong> card above the danger zone. <strong>Transfer this project&hellip;</strong> hands the project to another person's account (not available for Team / Legal Team projects yet); they accept by email while signed in, and you stay on as an admin. A pending transfer is shown in the same card with <strong>Cancel transfer</strong>. How it works, including what happens to a paid subscription, is described in chapter 19, <em>Team Collaboration</em> &rarr; Transferring ownership.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Danger zone &mdash; deleting a project</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					At the bottom of the Settings page is the <strong>danger zone</strong>, where the project owner (or an org admin) can <strong>permanently delete</strong> the project. You confirm by typing the project's slug. This action cannot be undone.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					Deleting a project removes <strong>everything</strong>: the database and all its tables and rows, all stored files, end users, API keys, webhooks, and configuration &mdash; and, for a Pro project, it <strong>cancels the subscription so no further charges are made</strong>. You don't need to cancel billing separately first; deleting the project does it for you. There is no recovery and no export after deletion, so export anything you need (see Compliance &rarr; Export) beforehand.
				</p>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/cli" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Team Collaboration &rarr;
				</a>
			</div>
		</section>
