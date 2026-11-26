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

	const c = 'rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700';

	const setupCode = `// server.js — Node 18+, the secret key from the environment
import { createClient } from '@eurobase/sdk'

const eb = createClient({
  url: 'https://your-project.eurobase.app',
  apiKey: process.env.EUROBASE_SECRET_KEY, // eb_sk_… — never in a browser or an app
})

const { data, error, status, code } = await eb.auth.admin.listUsers({ perPage: 20 })`;

	const methodsCode = `eb.auth.admin.listUsers({ page, perPage, search })      // { users, total }
eb.auth.admin.createUser({ email, password, emailConfirmed, displayName, metadata, locale })
eb.auth.admin.getUserById(id)
eb.auth.admin.updateUserById(id, { email, displayName, metadata, emailConfirmed, locale })
eb.auth.admin.deleteUser(id)                             // data: null
eb.auth.admin.suspendUser(id)
eb.auth.admin.unsuspendUser(id)
eb.auth.admin.setPassword(id, password)                  // { status: 'password_reset' }
eb.auth.admin.signOutUser(id)                            // { status: 'signed_out', refresh_tokens_revoked }

// Every method resolves to { data, error, status?, code? } and never throws.`;

	const userJson = `{
  "id": "6f486f53-2841-4a86-961b-9673dfb2b58d",
  "email": "ana@partner.example",
  "display_name": "Ana Novak",
  "providers": ["email"],
  "metadata": { "company": "Partner GmbH", "plan": "b2b" },
  "banned_at": null,
  "last_sign_in_at": null,
  "email_confirmed_at": "2026-11-20T09:12:44Z",
  "created_at": "2026-11-20T09:12:44Z",
  "locale": "de"
}`;

	const curlCode = `export EB=https://your-project.eurobase.app/v1/auth/admin/users
export KEY=eb_sk_...   # the secret key

# List (page is 1-based; per_page 1-200, default 50; search matches email, name or id)
curl -s "$EB?page=1&per_page=50&search=partner" -H "apikey: $KEY"

# Create (password optional; email_confirmed defaults to true)
curl -s -X POST "$EB" -H "apikey: $KEY" -H "Content-Type: application/json" \\
  -d '{"email":"ana@partner.example","password":"a-long-password","email_confirmed":true,
       "display_name":"Ana Novak","metadata":{"company":"Partner GmbH"}}'

# Get / update / delete
curl -s "$EB/$USER_ID" -H "apikey: $KEY"
curl -s -X PATCH "$EB/$USER_ID" -H "apikey: $KEY" -H "Content-Type: application/json" \\
  -d '{"display_name":"Ana N.","metadata":{"company":"Partner GmbH","plan":"gold"}}'
curl -s -X DELETE "$EB/$USER_ID" -H "apikey: $KEY"            # 204

# Suspend / lift the suspension
curl -s -X POST   "$EB/$USER_ID/suspend" -H "apikey: $KEY"
curl -s -X DELETE "$EB/$USER_ID/suspend" -H "apikey: $KEY"

# Set a new password
curl -s -X POST "$EB/$USER_ID/password" -H "apikey: $KEY" -H "Content-Type: application/json" \\
  -d '{"password":"another-long-password"}'

# Sign out everywhere (revoke every refresh token)
curl -s -X POST "$EB/$USER_ID/signout" -H "apikey: $KEY"`;

	const partnerSql = `-- Which company each account belongs to. Your backend writes it; users only read their own row.
CREATE TABLE company_members (
  user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  company_id uuid NOT NULL REFERENCES companies(id),
  role       text NOT NULL DEFAULT 'member'
);
ALTER TABLE company_members ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_membership ON company_members
  FOR SELECT USING (user_id = auth_uid());
-- Your server (secret key) reads and writes every row.
CREATE POLICY server_manages ON company_members
  FOR ALL USING (is_service_role()) WITH CHECK (is_service_role());

-- Example: everyone in a company sees the company's invoices.
ALTER TABLE invoices ENABLE ROW LEVEL SECURITY;
CREATE POLICY company_invoices ON invoices
  FOR SELECT USING (company_id IN (
    SELECT company_id FROM company_members WHERE user_id = auth_uid()
  ));`;

	const partnerCode = `// Your backend creates accounts for a company's employees, records the
// membership, and sends each person a link to choose their password.
export async function onboardCompany(company, employees) {
  const results = []
  for (const person of employees) {
    const { data, error, code } = await eb.auth.admin.createUser({
      email: person.email,
      emailConfirmed: true,                  // you vouch for the address
      displayName: person.name,
      metadata: { company: company.name },  // a label for your UI, not for access rules
    })
    if (code === 'email_exists') {
      results.push({ email: person.email, status: 'exists' })
      continue
    }
    if (error) {
      results.push({ email: person.email, status: 'failed', error })
      continue
    }

    // Access comes from this table (see the policies above), written with the secret key.
    await eb.db.from('company_members').insert({ user_id: data.id, company_id: company.id, role: person.role })

    // Creating an account sends no email. Send the "set your password" link:
    const sent = await eb.auth.forgotPassword(person.email, {
      emailRedirectTo: 'https://app.example.com/set-password', // one of your allowed redirect URLs
    })
    results.push({ email: person.email, status: 'created', id: data.id, invited: !sent.error })

    await new Promise((r) => setTimeout(r, 250)) // pace large batches
  }
  return results
}`;

	const deleteCode = `// DELETE /account — the signed-in user deletes their own account.
import express from 'express'
import { createRemoteJWKSet, jwtVerify } from 'jose'
import { createClient } from '@eurobase/sdk'

const eb = createClient({ url: process.env.EUROBASE_URL, apiKey: process.env.EUROBASE_SECRET_KEY })
const JWKS = createRemoteJWKSet(new URL(process.env.EUROBASE_URL + '/v1/auth/.well-known/jwks.json'))

const app = express()

app.delete('/account', async (req, res) => {
  // 1. Who is calling? Verify their access token; never trust an id from the body.
  const token = (req.headers.authorization || '').replace(/^Bearer /, '')
  let userId
  try {
    const { payload } = await jwtVerify(token, JWKS, {
      issuer: 'eurobase',
      audience: process.env.EUROBASE_PROJECT_ID,
      algorithms: ['ES256'],
    })
    userId = payload.sub
  } catch {
    return res.status(401).json({ error: 'not signed in' })
  }

  // 2. Delete what your tables keep about them that the database won't cascade
  //    (here: their uploaded files, which reference the user).
  //    ... await eb.storage.remove(...) for each of their files ...

  // 3. Delete the account. Their refresh tokens go with it.
  const { error, status, code } = await eb.auth.admin.deleteUser(userId)
  if (status === 404) return res.status(204).end() // already gone
  if (code === 'user_referenced') return res.status(409).json({ error })
  if (error) return res.status(502).json({ error: 'could not delete the account' })
  res.status(204).end()
})

app.listen(3000)`;

	const dashboardCode = `// GET /admin/users?page=1&q=ana — your own admin dashboard's API.
// Protect this route with your staff login; the secret key stays on the server.
app.get('/admin/users', requireStaff, async (req, res) => {
  const page = Math.max(1, Number(req.query.page) || 1)
  const { data, error } = await eb.auth.admin.listUsers({
    page,
    perPage: 25,
    search: typeof req.query.q === 'string' ? req.query.q : undefined,
  })
  if (error) return res.status(502).json({ error })
  res.json({
    page,
    pages: Math.ceil(data.total / 25),
    users: data.users.map((u) => ({
      id: u.id,
      email: u.email,
      name: u.display_name,
      suspended: u.banned_at !== null,
      confirmed: u.email_confirmed_at !== null,
      lastSignIn: u.last_sign_in_at,
    })),
  })
})`;

	const abuseCode = `// A moderator bans an abusive user: suspend (no new sign-in, refresh tokens
// revoked) and record why in their metadata.
export async function banUser(userId, reason, moderator) {
  const { data: user, error } = await eb.auth.admin.getUserById(userId)
  if (error) return { error }

  const suspended = await eb.auth.admin.suspendUser(userId)
  if (suspended.error && suspended.status !== 404) return { error: suspended.error } // 404 = already suspended

  await eb.auth.admin.updateUserById(userId, {
    metadata: { ...user.metadata, bannedReason: reason, bannedBy: moderator, bannedAt: new Date().toISOString() },
  })
  return { error: null }
}

// Lifting the ban later:
//   await eb.auth.admin.unsuspendUser(userId)
// Signing someone out everywhere without suspending them (e.g. a lost phone):
//   await eb.auth.admin.signOutUser(userId)`;

	const csvCode = `// import-users.mjs — node import-users.mjs users.csv
// CSV columns: email,name,company   (one user per line, header first)
import { readFile } from 'node:fs/promises'
import { createClient } from '@eurobase/sdk'

const eb = createClient({ url: process.env.EUROBASE_URL, apiKey: process.env.EUROBASE_SECRET_KEY })

const [header, ...lines] = (await readFile(process.argv[2], 'utf8')).trim().split(/\\r?\\n/)
const cols = header.split(',').map((s) => s.trim())

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

let created = 0, existing = 0, failed = 0
for (const line of lines) {
  const row = Object.fromEntries(line.split(',').map((v, i) => [cols[i], v.trim()]))
  let res
  for (let attempt = 0; attempt < 5; attempt++) {
    res = await eb.auth.admin.createUser({
      email: row.email,
      displayName: row.name,
      metadata: { company: row.company, importedAt: new Date().toISOString() },
    })
    if (res.status !== 429) break
    await sleep((res.retryAfter ?? 5) * 1000) // over the rate limit: wait, then retry
  }
  const { error, code } = res
  if (!error) created++
  else if (code === 'email_exists') existing++ // safe to re-run
  else { failed++; console.error(row.email, error) }
}
console.log({ created, existing, failed })`;
</script>

<section id="user-admin" class="scroll-mt-20">
	<h2 class="text-2xl font-bold text-gray-900 mb-1">33. User admin API (server-side)</h2>
	<p class="text-sm italic text-gray-500 mb-4">Create, list, change, suspend and delete your app's users from your own server, with the project's secret key.</p>

	<div class="space-y-4">
		<p class="text-sm text-gray-700 leading-relaxed">
			Everything the console's <a href="/docs/users" class="text-eurobase-700 hover:underline">Users</a> page does is also an API for your backend: <code class={c}>/v1/auth/admin/users</code>, and in the SDK <code class={c}>eb.auth.admin</code>. It is the counterpart of Supabase's <code class={c}>auth.admin</code>: the same names, so code moving from Supabase changes little. The same rules as the console apply: email checks, passwords of 8–72 bytes, bcrypt with cost 12.
		</p>
		<p class="text-sm text-amber-800 bg-amber-50 border-l-4 border-amber-400 px-3 py-2 rounded leading-relaxed">
			<strong>Secret key only.</strong> The admin API reads and changes every account of the project, so it answers only to the secret key (<code class={c}>eb_sk_…</code>, console → Settings → API keys). Use it on a server or in a script — never in a browser, a mobile app or anything you ship to users. Edge functions can't call it (they can't reach your project's API URL); inside an edge function use <code class={c}>ctx.db.sql</code>, or <code class={c}>ctx.db.asService()</code> where the function must act as the service. Send the key in the <code class={c}>apikey</code> header (the SDK does): a key in the URL (<code class={c}>?apikey=</code>) is refused, because URLs end up in logs. A public key gets <code class={c}>403</code> (<code class={c}>secret_key_required</code>); the SDK returns that error without sending a request. An end user's access token on the request plays no part: the key alone authorises.
		</p>

		<h3 id="setup" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Setup</h3>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(setupCode, 'setup')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'setup' ? 'Copied!' : 'Copy'}</button>
			<pre>{setupCode}</pre>
		</div>
		<p class="text-sm text-gray-700 leading-relaxed">Requires <code class={c}>@eurobase/sdk</code> 0.13.0 or later. Admin requests send only the API key, even if the same client has a signed-in user.</p>

		<h3 id="methods" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Methods</h3>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(methodsCode, 'methods')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'methods' ? 'Copied!' : 'Copy'}</button>
			<pre>{methodsCode}</pre>
		</div>
		<div class="overflow-x-auto">
			<table class="w-full text-sm text-left text-gray-700 border border-gray-200 rounded-lg">
				<thead class="bg-gray-50 text-xs uppercase text-gray-500">
					<tr><th class="px-3 py-2">SDK</th><th class="px-3 py-2">HTTP</th><th class="px-3 py-2">Request</th><th class="px-3 py-2">Success</th></tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					<tr><td class="px-3 py-2"><code class={c}>listUsers</code></td><td class="px-3 py-2"><code class={c}>GET /</code></td><td class="px-3 py-2">query <code class={c}>page</code> (from 1), <code class={c}>per_page</code> (1–200, default 50), <code class={c}>search</code> (part of the email, display name or id), <code class={c}>email</code> (exact match)</td><td class="px-3 py-2">200 <code class={c}>{'{'} users, total {'}'}</code>, newest first</td></tr>
					<tr><td class="px-3 py-2"><code class={c}>createUser</code></td><td class="px-3 py-2"><code class={c}>POST /</code></td><td class="px-3 py-2"><code class={c}>email</code>; optional <code class={c}>password</code>, <code class={c}>email_confirmed</code> (default <code class={c}>true</code>), <code class={c}>display_name</code>, <code class={c}>metadata</code>, <code class={c}>locale</code> (the user's email language, e.g. <code class={c}>"de"</code>)</td><td class="px-3 py-2">201 user</td></tr>
					<tr><td class="px-3 py-2"><code class={c}>getUserById</code></td><td class="px-3 py-2"><code class={c}>GET /{'{'}id{'}'}</code></td><td class="px-3 py-2">—</td><td class="px-3 py-2">200 user</td></tr>
					<tr><td class="px-3 py-2"><code class={c}>updateUserById</code></td><td class="px-3 py-2"><code class={c}>PATCH /{'{'}id{'}'}</code></td><td class="px-3 py-2">any of <code class={c}>email</code>, <code class={c}>display_name</code>, <code class={c}>metadata</code> (replaces the whole object), <code class={c}>email_confirmed</code>, <code class={c}>locale</code> (<code class={c}>""</code> clears it)</td><td class="px-3 py-2">200 user</td></tr>
					<tr><td class="px-3 py-2"><code class={c}>deleteUser</code></td><td class="px-3 py-2"><code class={c}>DELETE /{'{'}id{'}'}</code></td><td class="px-3 py-2">—</td><td class="px-3 py-2">204</td></tr>
					<tr><td class="px-3 py-2"><code class={c}>suspendUser</code></td><td class="px-3 py-2"><code class={c}>POST /{'{'}id{'}'}/suspend</code></td><td class="px-3 py-2">—</td><td class="px-3 py-2">200 user (<code class={c}>banned_at</code> set)</td></tr>
					<tr><td class="px-3 py-2"><code class={c}>unsuspendUser</code></td><td class="px-3 py-2"><code class={c}>DELETE /{'{'}id{'}'}/suspend</code></td><td class="px-3 py-2">—</td><td class="px-3 py-2">200 user</td></tr>
					<tr><td class="px-3 py-2"><code class={c}>setPassword</code></td><td class="px-3 py-2"><code class={c}>POST /{'{'}id{'}'}/password</code></td><td class="px-3 py-2"><code class={c}>password</code> (8–72 bytes)</td><td class="px-3 py-2">200 <code class={c}>{'{'} status: "password_reset" {'}'}</code></td></tr>
					<tr><td class="px-3 py-2"><code class={c}>signOutUser</code></td><td class="px-3 py-2"><code class={c}>POST /{'{'}id{'}'}/signout</code></td><td class="px-3 py-2">—</td><td class="px-3 py-2">200 <code class={c}>{'{'} status: "signed_out", refresh_tokens_revoked {'}'}</code></td></tr>
				</tbody>
			</table>
		</div>
		<p class="text-sm text-gray-700 leading-relaxed">Paths are relative to <code class={c}>https://your-project.eurobase.app/v1/auth/admin/users</code>. A user looks like this (no password hash or token data, ever):</p>
		<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto"><pre>{userJson}</pre></div>

		<h3 id="curl" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">The same with curl</h3>
		<p class="text-sm text-gray-700 leading-relaxed">The key goes in the <code class={c}>apikey</code> header; the API refuses it in the URL.</p>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(curlCode, 'curl')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'curl' ? 'Copied!' : 'Copy'}</button>
			<pre>{curlCode}</pre>
		</div>

		<h3 id="errors" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Errors</h3>
		<p class="text-sm text-gray-700 leading-relaxed">Error bodies are <code class={c}>{'{'} "error": "…", "code": "…" {'}'}</code> (<code class={c}>code</code> where listed). The SDK returns them as <code class={c}>{'{'} data: null, error, status, code {'}'}</code>.</p>
		<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
			<li><strong>400</strong> — invalid email, a password outside 8–72 bytes, a display name over 256 characters, metadata over 16 KiB (as JSON), an unreadable body, or an update with no fields.</li>
			<li><strong>401</strong> — no API key, or an unknown one.</li>
			<li><strong>403</strong> <code class={c}>secret_key_required</code> — any key other than the secret key. <code class={c}>apikey_header_required</code> — the secret key was sent in the URL instead of the header.</li>
			<li><strong>404</strong> — no user with that id (also for an id that isn't a UUID); suspending a user who is already suspended, or lifting a suspension that isn't there.</li>
			<li><strong>409</strong> <code class={c}>email_exists</code> — another user has that email (create, or changing the email). <code class={c}>user_referenced</code> — deleting a user that other rows still point to without <code class={c}>ON DELETE CASCADE</code>, for example files they uploaded or your own tables; remove or reassign those first. <code class={c}>retention_hold_active</code> — the user is under a retention hold (Legal Team).</li>
			<li><strong>413</strong> <code class={c}>body_too_large</code> — a request body over 64 KiB.</li>
			<li><strong>429</strong> — too many requests, with a <code class={c}>Retry-After</code> header (the SDK's <code class={c}>retryAfter</code>, in seconds). Two limits apply: your plan's requests per second, like the data API; and, per project, at most 10 calls in 5 seconds that hash a password (<code class={c}>createUser</code> with a password, <code class={c}>setPassword</code>) — hashing is deliberately slow. Wait and retry.</li>
			<li><strong>503</strong> — maintenance, a Team project's database is not reachable (never served from anywhere else), or a delete whose retention-hold check couldn't run; retry.</li>
		</ul>

		<h3 id="suspend-delete" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">What suspend, sign-out and delete do — and don't</h3>
		<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
			<li><strong>Suspend</strong> stops every new sign-in and revokes all the user's refresh tokens. Their data stays.</li>
			<li><strong>Sign out</strong> revokes all refresh tokens (every device) without suspending: the user can sign in again.</li>
			<li><strong>Set password</strong> also revokes all refresh tokens.</li>
			<li><strong>Delete</strong> removes the user, their sign-in methods and tokens, and rows of your tables that reference them with <code class={c}>ON DELETE CASCADE</code>. Files in storage are not deleted for you.</li>
			<li><strong>Access tokens already issued stay valid until they expire</strong>, after any of these. Keep <strong>Session duration</strong> short (Auth settings). Where it matters, ask the server about the user, not only the token: <code class={c}>GET /v1/auth/user</code> with their token (the SDK's <code class={c}>getUser</code>) fails for a deleted or suspended user, while verifying the token locally (JWKS, or the SDK's <code class={c}>verifyAccessToken</code>) checks only the token's signature and expiry. With the secret key, <code class={c}>admin.getUserById(id)</code> shows <code class={c}>banned_at</code> for a suspended user and 404 for a deleted one.</li>
		</ul>

		<h3 id="audit" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Audit log</h3>
		<p class="text-sm text-gray-700 leading-relaxed">Every change — create, update, delete, suspend, lift, set password, sign out — is written to the project's audit log (console → Compliance), from the console and from the API alike: <code class={c}>end_user.created</code>, <code class={c}>end_user.updated</code> (the names of the changed fields, not their values), <code class={c}>end_user.deleted</code>, <code class={c}>end_user.suspended</code>, <code class={c}>end_user.unsuspended</code>, <code class={c}>end_user.password_set</code>, <code class={c}>end_user.signed_out</code>. Calls with the secret key show the actor <em>secret API key</em> and <code class={c}>"via": "secret_key"</code>. Reads (list, get) aren't logged. Passwords are never logged.</p>

		<h3 id="use-cases" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Use cases</h3>

		<h4 class="text-base font-semibold text-gray-900 mt-4">1. Accounts for a company's employees, or a partner's customers (B2B)</h4>
		<p class="text-sm text-gray-700 leading-relaxed">Two common shapes: <strong>B2B seats</strong> — a company buys accounts for its employees and you create them, instead of each person signing up; and <strong>partner integrations</strong> — another platform creates accounts for its customers through your API, and your server creates the Eurobase users behind it.</p>
		<p class="text-sm text-gray-700 leading-relaxed">Decide access with a table your backend fills, not with <code class={c}>metadata</code>: keep the company there only as a label. App users can only read their own row of <code class={c}>company_members</code>, so they can't move themselves into another company; your server writes it with the secret key, which the <code class={c}>is_service_role()</code> policy admits.</p>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(partnerSql, 'partner-sql')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'partner-sql' ? 'Copied!' : 'Copy'}</button>
			<pre>{partnerSql}</pre>
		</div>
		<p class="text-sm text-gray-700 leading-relaxed">Creating an account sends no email. Without a password, each person sets one through the reset link that <code class={c}>auth.forgotPassword(email, {'{'} emailRedirectTo {'}'})</code> sends (or signs in with a magic link). Those emails count towards your project's email sending (see <a href="/docs/rate-limits" class="text-eurobase-700 hover:underline">Rate Limits</a>): 3 per address per 15 minutes, and 60 reset emails an hour per client IP — and since the client IP isn't always told apart today, count that 60 as shared with the reset requests of the rest of your project. A batch of 20 fits; for more than about 50 an hour, spread the invitations out, or use a magic link or your own email with a link to your sign-in page.</p>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(partnerCode, 'partner')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'partner' ? 'Copied!' : 'Copy'}</button>
			<pre>{partnerCode}</pre>
		</div>

		<h4 class="text-base font-semibold text-gray-900 mt-4">2. "Delete my account" in an Express server</h4>
		<p class="text-sm text-gray-700 leading-relaxed">Verify who is calling from their access token (see <a href="/docs/auth" class="text-eurobase-700 hover:underline">Verifying tokens on your server</a>), then delete that user — never an id the client sends.</p>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(deleteCode, 'delete')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'delete' ? 'Copied!' : 'Copy'}</button>
			<pre>{deleteCode}</pre>
		</div>

		<h4 class="text-base font-semibold text-gray-900 mt-4">3. An admin dashboard that lists and searches users</h4>
		<p class="text-sm text-gray-700 leading-relaxed">Your dashboard's frontend calls your server; your server calls the admin API. <code class={c}>search</code> matches part of the email, display name or id.</p>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(dashboardCode, 'dashboard')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'dashboard' ? 'Copied!' : 'Copy'}</button>
			<pre>{dashboardCode}</pre>
		</div>

		<h4 class="text-base font-semibold text-gray-900 mt-4">4. Suspending an abusive user and signing them out everywhere</h4>
		<p class="text-sm text-gray-700 leading-relaxed">Suspending already revokes their refresh tokens; their current access token works until it expires.</p>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(abuseCode, 'abuse')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'abuse' ? 'Copied!' : 'Copy'}</button>
			<pre>{abuseCode}</pre>
		</div>

		<h4 class="text-base font-semibold text-gray-900 mt-4">5. A one-off script that imports users from a CSV file</h4>
		<p class="text-sm text-gray-700 leading-relaxed">Re-running it is safe: existing emails answer <code class={c}>email_exists</code> and are left unchanged. It goes one user at a time and waits when it gets a 429; without passwords the per-project hashing limit doesn't apply, only your plan's request rate. For large imports with existing password hashes or ids, use the console's batch import (<a href="/docs/users" class="text-eurobase-700 hover:underline">Managing End Users</a>) or the CLI's Supabase import instead.</p>
		<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
			<button onclick={() => copyCode(csvCode, 'csv')} class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer">{copiedId === 'csv' ? 'Copied!' : 'Copy'}</button>
			<pre>{csvCode}</pre>
		</div>
		<p class="text-sm text-gray-700 leading-relaxed">The simple <code class={c}>split(',')</code> doesn't handle quoted fields; use a CSV parser for real files.</p>
	</div>
</section>
