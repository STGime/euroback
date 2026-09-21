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
					Eurobase itself runs entirely on EU-sovereign infrastructure (Scaleway, France). If you configure a US-based SMTP provider (SendGrid, Mailgun, Postmark, Amazon SES, Mandrill, SparkPost, SMTP.com), the console shows an amber advisory: your auth email content will leave the EU jurisdiction even though the rest of your project doesn't.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					This is your call &mdash; it's not a block. EU-based alternatives worth knowing: Scaleway TEM, Brevo (FR), Mailjet (EU), Mailtrap EU.
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
					<a href="/docs/users" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
						Next: Managing End Users &rarr;
					</a>
				</div>
			</div>
		</section>
