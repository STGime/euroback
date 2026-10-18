<script lang="ts">
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
</script>

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

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3">
					<p class="text-sm font-semibold text-amber-900">Today the limits count per project</p>
					<p class="mt-1 text-sm text-amber-800 leading-relaxed">
						The limits below are counted per client IP, but the platform doesn't see each client's own IP yet. Until it does, every request to your project counts against one shared budget, so "per IP" currently means <strong>a total across all your users</strong>. For a launch, a migration day or anything else where many users sign in at once, set <strong>sign-up / sign-in to 30</strong> and <strong>token verification to 150</strong> per 5 minutes (the maximums). Per-IP counting will follow, and this page will say so.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900">Where to find it</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					In the console: <strong>Auth &rarr; Rate Limits</strong> tab. The page shows the knobs below, plus an "IP Address Forwarding" toggle.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">The knobs</h3>
				<ul class="text-sm text-gray-700 space-y-2 ml-4 list-disc">
					<li>
						<strong>Signup / sign-in rate</strong> &mdash; how many <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/signup</code> + <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/signin</code> requests per 5-minute window per IP. <span class="text-gray-500">Default: 8 per 5 min.</span>
					</li>
					<li>
						<strong>Token refresh rate</strong> &mdash; how many <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/refresh</code> calls per 5 min per IP. Higher than the others because legitimate SDK clients refresh proactively. <span class="text-gray-500">Default: 150 per 5 min (~ 1800/hour).</span>
					</li>
					<li>
						<strong>Token verification rate</strong> &mdash; how many phone-OTP, magic-link sign-in, password-reset and email-verification calls per 5 min per IP. <span class="text-gray-500">Default: 30 per 5 min.</span>
					</li>
					<li>
						<strong>Auth emails</strong> &mdash; there's no hourly cap per project on the platform sender today (the "emails per hour" setting isn't enforced, so the console doesn't show it). Per address: 3 magic links and 3 password-reset emails per 15 minutes, one verification resend per 5 minutes. Per client IP (so per project for now): 60 password-reset and 60 verification emails an hour, 300 magic-link requests an hour.
					</li>
					<li>
						<strong>SMS per hour</strong> &mdash; cap on outbound auth SMS (phone OTP) per rolling hour. <span class="text-gray-500">Default: 30/hour; an override can be at most 10.</span>
					</li>
					<li>
						<strong>IP Address Forwarding (Trust Proxy)</strong> &mdash; when on, the rate limiter takes the client IP from <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">X-Forwarded-For</code> instead of the connection. <strong>Leave it off for now:</strong> it doesn't change how the limits count yet (see the note above). <span class="text-gray-500">Default: off.</span>
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
					<li>For a launch or migration day, set sign-up / sign-in to 30 and token verification to 150 (see the note above).</li>
					<li>Hit <strong>Save changes</strong>. The new limits apply within a few seconds &mdash; no redeploy needed.</li>
				</ol>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">What an over-quota response looks like</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					When a request goes over a limit, the SDK receives <strong>HTTP 429 Too Many Requests</strong> with a <code class="bg-gray-100 border border-gray-200 rounded px-1">Retry-After</code> header naming the number of seconds the client should wait. Your app can surface a friendly message; the limit window slides, so retrying after the indicated delay succeeds.
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
					<a href="/docs/custom-smtp" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
						Next: Custom SMTP &rarr;
					</a>
				</div>
			</div>
		</section>
