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
	// Kept out of the template, with "<" escaped as \x3c: raw tag text in a
	// component (even inside a string) trips the Svelte/TS tooling.
	const verifyPageHtml = "\x3c!-- verify.html — the page Eurobase's verification email links to.\n     Deploy at whichever URL you configured as email_verification_url. -->\n\x3c!DOCTYPE html>\n\x3chtml lang=\"en\">\n\x3chead>\n  \x3cmeta charset=\"utf-8\">\n  \x3ctitle>Verifying your email…\x3c/title>\n\x3c/head>\n\x3cbody>\n  \x3ch1 id=\"status\">Verifying…\x3c/h1>\n  \x3cscript type=\"module\">\n    import { createClient } from 'https://cdn.jsdelivr.net/npm/@eurobase/sdk/+esm'\n    const eb = createClient({\n      url: 'https://your-project.eurobase.app',\n      apiKey: 'eb_pk_YOUR_PUBLIC_KEY',\n    })\n    const token = new URL(location.href).searchParams.get('token')\n    const status = document.getElementById('status')\n    if (!token) {\n      status.textContent = 'Missing verification token in the URL.'\n    } else {\n      const { error } = await eb.auth.verifyEmail(token)\n      if (error) {\n        status.textContent = 'Verification failed: ' + error\n      } else {\n        status.textContent = 'Verified! You can now sign in.'\n        setTimeout(() => location.href = '/login', 2000)\n      }\n    }\n  \x3c/script>\n\x3c/body>\n\x3c/html>";
</script>

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
					<li><strong>Phone (SMS OTP)</strong> &mdash; sign in with phone number via a 6-digit SMS code (EU-sovereign SMS via GatewayAPI). <span class="text-amber-700 font-medium">Paid plans only</span> &mdash; every send has a per-message cost.</li>
					<li><strong>Passkeys</strong> &mdash; coming soon (WebAuthn / FaceID / fingerprint)</li>
					<li><strong>Social Login</strong> &mdash; Google, GitHub, LinkedIn, Apple (configure in Auth settings)</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Configuration options</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Password rules</strong> &mdash; set minimum length (8&ndash;128 characters)</li>
					<li><strong>Email confirmation</strong> &mdash; require users to verify their email before signing in</li>
					<li><strong>Session duration</strong> &mdash; how long access tokens remain valid (1h to 30 days)</li>
					<li><strong>Redirect URLs</strong> &mdash; whitelist URLs your app can redirect to after auth callbacks</li>
					<li><strong>CORS origins</strong> &mdash; browser origins (scheme://host[:port]) allowed to call this project's API. Add your dev and production app origins. Eurobase platform origins (<code class="bg-gray-100 border border-gray-200 rounded px-1">*.eurobase.app</code>) are always allowed. <strong>Loopback port wildcard supported</strong>: add <code class="bg-gray-100 border border-gray-200 rounded px-1">http://localhost:*</code> (or the <code class="bg-gray-100 border border-gray-200 rounded px-1">127.0.0.1</code> / <code class="bg-gray-100 border border-gray-200 rounded px-1">[::1]</code> spellings) to match any port &mdash; the standard carve-out for local dev tools that bind a fresh random port each run. Wildcards on non-loopback hosts are rejected at save time.</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Email confirmation &mdash; end-to-end</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					When you enable <strong>Email confirmation</strong>, signups aren't complete until the user clicks a link in the verification email. Same shape for password reset and magic-link sign-in. All three flows follow the same three-step pattern:
				</p>
				<ol class="mt-2 text-sm text-gray-700 space-y-1.5 ml-4 list-decimal">
					<li>User signs up / requests a reset / requests a magic link via the SDK.</li>
					<li>Eurobase sends an email with a link to <strong>a page on your app</strong> (not Eurobase's console) with a <code class="bg-gray-100 border border-gray-200 rounded px-1">?token=...</code> query parameter.</li>
					<li>Your page reads the token from the query string and calls the corresponding SDK method to complete the flow.</li>
				</ol>

				<h4 class="text-sm font-semibold text-gray-900 mt-4">Configure the URLs</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					In <strong>Auth &rarr; Settings &rarr; Email-flow redirect URLs</strong>, set the three URLs your app will host:
				</p>
				<ul class="mt-1 text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Email verification URL</strong> &mdash; e.g. <code class="bg-gray-100 border border-gray-200 rounded px-1">https://yourapp.example/verify</code>. Required when "Require email confirmation" is on.</li>
					<li><strong>Password reset URL</strong> &mdash; e.g. <code class="bg-gray-100 border border-gray-200 rounded px-1">https://yourapp.example/reset-password</code>.</li>
					<li><strong>Magic link URL</strong> &mdash; e.g. <code class="bg-gray-100 border border-gray-200 rounded px-1">https://yourapp.example/magic-link</code>.</li>
				</ul>
				<p class="mt-2 text-sm text-gray-700 leading-relaxed">
					Each URL <strong>must appear</strong> in the <strong>Allowed redirect URLs</strong> list above (same allowlist). The console blocks Save inline if a URL isn't allowed; the backend rejects the update for the same reason. This is the standard <em>open-redirect defence</em> &mdash; without it, an attacker could put your project's URL in a phishing email and land users on their own page. Add both your dev and prod URLs to the redirect list before configuring the three fields.
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-4">The verify-email page (copy-paste starter)</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					This is the minimum &mdash; a static HTML file works. Adapt to your framework (Next.js page, SvelteKit route, Vue component &mdash; same three lines of logic).
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-3">
					<button
						onclick={() => copyCode(verifyPageHtml, 'verify-page')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'verify-page' ? 'Copied!' : 'Copy'}
					</button>
					<pre>&lt;!-- verify.html --&gt;
&lt;!DOCTYPE html&gt;
&lt;html lang="en"&gt;
&lt;head&gt;&lt;title&gt;Verifying your email…&lt;/title&gt;&lt;/head&gt;
&lt;body&gt;
  &lt;h1 id="status"&gt;Verifying…&lt;/h1&gt;
  &lt;script type="module"&gt;
    import {'{'} createClient {'}'} from 'https://cdn.jsdelivr.net/npm/@eurobase/sdk/+esm'
    const eb = createClient({'{'}
      url: 'https://your-project.eurobase.app',
      apiKey: 'eb_pk_YOUR_PUBLIC_KEY',
    {'}'})
    const token = new URL(location.href).searchParams.get('token')
    const status = document.getElementById('status')
    if (!token) {'{'}
      status.textContent = 'Missing verification token in the URL.'
    {'}'} else {'{'}
      const {'{'} error {'}'} = await eb.auth.verifyEmail(token)
      status.textContent = error ? 'Verification failed: ' + error : 'Verified!'
    {'}'}
  &lt;/script&gt;
&lt;/body&gt;
&lt;/html&gt;</pre>
				</div>

				<h4 class="text-sm font-semibold text-gray-900 mt-4">Password reset page</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Same shape, but you collect a new password from the user first, then call <code class="bg-gray-100 border border-gray-200 rounded px-1">eb.auth.resetPassword(token, newPassword)</code>. Trigger the email with <code class="bg-gray-100 border border-gray-200 rounded px-1">eb.auth.forgotPassword(email)</code>.
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-4">Magic link page</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Same shape. Trigger with <code class="bg-gray-100 border border-gray-200 rounded px-1">eb.auth.requestMagicLink(email)</code>; complete with <code class="bg-gray-100 border border-gray-200 rounded px-1">eb.auth.signInWithMagicLink(token)</code>. On success the user is signed in and you can redirect to your app's home page.
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-4">Per-request override (multi-tenant apps)</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					If you serve multiple environments or subdomains from one project, pass <code class="bg-gray-100 border border-gray-200 rounded px-1">emailRedirectTo</code> on the SDK call to override the default for that user:
				</p>
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<button
						onclick={() => copyCode("await eb.auth.signUp({\n  email: 'user@example.com',\n  password: 'SecurePass123!',\n  emailRedirectTo: 'https://staging.yourapp.example/verify',\n})", 'sdk-redirect-override')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-redirect-override' ? 'Copied!' : 'Copy'}
					</button>
					<pre>await eb.auth.signUp({'{'}
  email: 'user@example.com',
  password: 'SecurePass123!',
  emailRedirectTo: 'https://staging.yourapp.example/verify',
{'}'})</pre>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					The per-request URL must still be in your <strong>Allowed redirect URLs</strong> list, or the signup returns 400 (same open-redirect defence).
				</p>

				<h4 class="text-sm font-semibold text-gray-900 mt-4">Migrating from the broken default</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					If your project had <strong>Require email confirmation</strong> enabled before July 2026, signups were silently broken &mdash; the email linked to a Eurobase-hosted URL that never existed. If any users were created with an unconfirmed email in that window, run this SQL from the SQL Runner to check who's affected:
				</p>
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<button
						onclick={() => copyCode("SELECT id, email, created_at FROM users\nWHERE email_confirmed_at IS NULL\nORDER BY created_at ASC;", 'sql-unconfirmed')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sql-unconfirmed' ? 'Copied!' : 'Copy'}
					</button>
					<pre>SELECT id, email, created_at FROM users
WHERE email_confirmed_at IS NULL
ORDER BY created_at ASC;</pre>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					After you configure the three redirect URLs, those users can request a fresh verification email with <code class="bg-gray-100 border border-gray-200 rounded px-1">eb.auth.resendVerification(email)</code> from a "verify your email" prompt in your app.
				</p>

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

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Phone Auth (SMS OTP)</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Phone auth lets users sign in with their phone number instead of an email. They receive a 6-digit verification code via SMS that expires after 10 minutes. Phone numbers must be in E.164 format (e.g., <code class="bg-gray-100 border border-gray-200 rounded px-1">+33612345678</code>).
				</p>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Enable phone auth in <strong>Auth &rarr; Settings &rarr; Phone (SMS OTP)</strong> toggle. The gateway must have <code class="bg-gray-100 border border-gray-200 rounded px-1">GATEWAYAPI_TOKEN</code> configured. SMS is sent via GatewayAPI, an EU-based provider (Denmark) &mdash; no data leaves EU infrastructure.
				</p>
				<div class="mt-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3">
					<p class="text-xs text-amber-800 leading-relaxed">
						<strong>Paid plan required.</strong> SMS auth is available on <strong>Pro and above</strong> because every OTP send has a per-message cost. Free projects see the toggle grayed out; a direct API call to <code class="bg-white border border-amber-200 rounded px-1">/v1/auth/phone/send-otp</code> from a Free project returns <code class="bg-white border border-amber-200 rounded px-1">402 paid_plan_required</code>.
					</p>
				</div>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-3">
					<pre><span class="text-gray-500">// 1. Send OTP to phone</span>
<span class="text-amber-400">POST</span> /v1/auth/phone/send-otp
Body: {'{'}"phone": "+33612345678"{'}'}

<span class="text-gray-500">// 2. Verify code and get session</span>
<span class="text-amber-400">POST</span> /v1/auth/phone/verify
Body: {'{'}"phone": "+33612345678", "code": "123456"{'}'}
<span class="text-gray-500">// Returns: access_token, refresh_token, user</span></pre>
				</div>

				<div class="mt-3 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">How it works</p>
					<ol class="text-xs text-gray-600 space-y-1 ml-4 list-decimal">
						<li>Your app sends the phone number to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/phone/send-otp</code></li>
						<li>Eurobase creates a user (if new) and sends a 6-digit code via SMS</li>
						<li>The user enters the code in your app</li>
						<li>Your app sends the phone + code to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/phone/verify</code></li>
						<li>Eurobase verifies the code, confirms the phone number, and returns JWT tokens</li>
					</ol>
				</div>

				<div class="mt-3 rounded-lg border border-blue-200 bg-blue-50 px-4 py-3">
					<p class="text-xs text-blue-700"><strong>Phone-only users:</strong> Users who sign in with only a phone number are created without an email. They can later add an email via account linking. Phone auth can coexist with email/password and social login.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Social Login (OAuth)</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase supports social login with <strong>Google</strong>, <strong>GitHub</strong>, <strong>LinkedIn</strong>, and <strong>Apple</strong>. Users authenticate with their existing account at the provider &mdash; Eurobase only receives their verified email, name, and profile picture. No application data is shared with the provider, and all user records remain in EU infrastructure.
				</p>

				<h4 class="text-base font-semibold text-gray-900 mt-4">Setting up a provider</h4>
				<ol class="text-sm text-gray-700 space-y-1.5 ml-4 list-decimal">
					<li>Go to <strong>Auth &rarr; Settings</strong> and toggle on the provider you want</li>
					<li>Create an OAuth app on the provider's developer console (links are shown in the setup instructions)</li>
					<li>Set the <strong>redirect/callback URL</strong> to your Eurobase API URL + <code class="bg-gray-100 border border-gray-200 rounded px-1">/v1/auth/oauth/{'{'}provider{'}'}/callback</code></li>
					<li>Copy the <strong>Client ID</strong> and <strong>Client Secret</strong> into the Eurobase console</li>
					<li>Add your app's URL to the <strong>Allowed redirect URLs</strong> list in Session Settings</li>
				</ol>

				<h4 class="text-base font-semibold text-gray-900 mt-4">Provider-specific notes</h4>
				<div class="space-y-2 mt-2">
					<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
						<p class="text-xs font-semibold text-gray-700">Google &amp; GitHub</p>
						<p class="text-xs text-gray-600 mt-1">Standard OAuth 2.0 setup. You need a Client ID and Client Secret from their developer consoles. GitHub fetches the primary verified email if the user's email is private.</p>
					</div>
					<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
						<p class="text-xs font-semibold text-gray-700">LinkedIn</p>
						<p class="text-xs text-gray-600 mt-1">Uses OpenID Connect. When creating your LinkedIn app, you must request the <strong>"Sign In with LinkedIn using OpenID Connect"</strong> product under the Products tab. Standard Client ID + Client Secret setup.</p>
					</div>
					<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
						<p class="text-xs font-semibold text-gray-700">Apple</p>
						<p class="text-xs text-gray-600 mt-1">Requires additional configuration: a <strong>Service ID</strong> (used as Client ID), <strong>Team ID</strong>, <strong>Key ID</strong>, and a <strong>.p8 private key</strong> file from the Apple Developer Portal. Apple only sends the user's name on the first authorization &mdash; subsequent logins won't include it. Users may also receive a private relay email address if they choose to hide their real email.</p>
					</div>
				</div>

				<h4 class="text-base font-semibold text-gray-900 mt-4">SDK usage</h4>
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<button
						onclick={() => copyCode("// Redirect to provider's login page\neb.auth.signInWithOAuth('google', {\n  redirectTo: 'https://myapp.com/auth/callback'\n})\n// Supported providers: 'google', 'github', 'linkedin', 'apple', 'microsoft'\n\n// On your callback page — extract tokens from URL fragment\nconst { data, error } = await eb.auth.handleOAuthCallback()\n// data.access_token, data.user — user is now signed in", 'sdk-oauth')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-oauth' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// Redirect to provider's login page
eb.auth.signInWithOAuth('google', {'{'}
  redirectTo: 'https://myapp.com/auth/callback'
{'}'})
// Supported providers: 'google', 'github', 'linkedin', 'apple', 'microsoft'

// On your callback page — extract tokens from URL fragment
const {'{'} data, error {'}'} = await eb.auth.handleOAuthCallback()
// data.access_token, data.user — user is now signed in</pre>
				</div>

				<h4 class="text-base font-semibold text-gray-900 mt-4">REST API</h4>
				<div class="rounded-lg bg-gray-900 p-4 font-mono text-[11px] text-green-400 leading-relaxed overflow-x-auto mt-2">
					<div class="text-gray-500">// 1. Initiate OAuth — redirects browser to provider</div>
					<div><span class="text-amber-400">GET</span> /v1/auth/oauth/{'{'}provider{'}'}?redirect_url=https://myapp.com/callback</div>
					<div class="mt-2 text-gray-500">// 2. Provider redirects back with tokens in the URL fragment</div>
					<div class="text-gray-400">https://myapp.com/callback#access_token=eyJ...&amp;refresh_token=...&amp;token_type=bearer&amp;expires_in=604800</div>
				</div>

				<div class="mt-3 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
					<p class="text-xs font-semibold text-gray-700 mb-1.5">How it works under the hood</p>
					<ol class="text-xs text-gray-600 space-y-1 ml-4 list-decimal">
						<li>Your app redirects the user to <code class="bg-white border border-gray-200 rounded px-1">/v1/auth/oauth/{'{'}provider{'}'}</code> with a <code class="bg-white border border-gray-200 rounded px-1">redirect_url</code></li>
						<li>Eurobase generates a CSRF state token, encodes the redirect URL in it, and redirects the browser to the provider's consent screen</li>
						<li>The user authenticates at the provider (Google, GitHub, LinkedIn, or Apple)</li>
						<li>The provider redirects back to Eurobase's callback endpoint with an authorization code</li>
						<li>Eurobase exchanges the code for user info (email, name, avatar), finds or creates the user, and links the OAuth identity</li>
						<li>The user is redirected to your app with JWT access and refresh tokens in the URL fragment</li>
					</ol>
				</div>

				<div class="mt-3 rounded-lg border border-blue-200 bg-blue-50 px-4 py-3">
					<p class="text-xs text-blue-700"><strong>Account linking:</strong> If a user signs up with email/password and later signs in with an OAuth provider using the same email, the accounts are automatically linked &mdash; same user ID, no duplicates. OAuth sign-in also auto-verifies the user's email.</p>
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
				<a href="/docs/rate-limits" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Rate Limits &rarr;
				</a>
			</div>
		</section>
