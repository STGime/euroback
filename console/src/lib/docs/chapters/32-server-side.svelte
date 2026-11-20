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

	// Code samples are kept out of the template: braces and "<" in markup
	// trip the Svelte tooling. "${" in a sample is written "\${".

	const setupCode = `// lib/eurobase.mjs — create once per process, import everywhere.
import { createClient } from '@eurobase/sdk'

const config = {
  url: process.env.EUROBASE_URL,               // https://your-project.eurobase.app
  projectId: process.env.EUROBASE_PROJECT_ID,  // checked as the token's audience
  auth: { persistSession: false },             // server mode: never keeps a session
}

// Acts as nobody until you call eb.withAccessToken(token). Verifies tokens.
export const eb = createClient({ ...config, apiKey: process.env.EUROBASE_PUBLIC_KEY })

// The service role: RLS is skipped. Only for work your own code has checked.
export const admin = createClient({ ...config, apiKey: process.env.EUROBASE_SECRET_KEY })`;

	const verifyCode = `const { data, error } = await eb.auth.verifyAccessToken(req.headers.authorization ?? '')
if (error) {
  // 'missing token' | 'invalid token' | 'token expired' | 'HS256 tokens are not accepted'
  return res.status(401).json({ error })
}
data.userId      // the user's id (claim sub)
data.email       // their email (the phone number for phone sign-in)
data.role        // 'authenticated'
data.expiresAt   // Unix seconds
data.claims      // every claim of the token
data.verifiedBy  // 'jwks' (checked locally) or 'server' (asked the gateway)

// The user's current state from the gateway — still exists, current profile:
const { data: user } = await eb.auth.getUser(token)`;

	const expressCode = `// server.mjs — npm install express @eurobase/sdk ; node server.mjs
import express from 'express'
import { eb, admin } from './lib/eurobase.mjs'

// Checks the bearer token, then puts the caller (req.user) and a client that
// acts as the caller (req.eb) on the request. Nothing is shared between requests.
async function requireUser(req, res, next) {
  const header = req.get('authorization') ?? ''
  const { data, error } = await eb.auth.verifyAccessToken(header)
  if (error) return res.status(401).json({ error })
  req.user = data
  req.eb = eb.withAccessToken(header)
  next()
}

const app = express()
app.use(express.json())

// The caller's own rows: the table's RLS policies decide what they see.
app.get('/api/todos', requireUser, async (req, res) => {
  const { data, error } = await req.eb.db
    .from('todos')
    .select('id', 'title', 'completed')
    .order('created_at', { ascending: false })
  if (error) return res.status(500).json({ error })
  res.json(data)
})

app.post('/api/todos', requireUser, async (req, res) => {
  const { data, error } = await req.eb.db.from('todos').insert({
    title: String(req.body.title ?? ''),
    user_id: req.user.userId,
  })
  if (error) return res.status(400).json({ error })
  res.status(201).json(data)
})

// Files and functions run as the caller too.
app.get('/api/files', requireUser, async (req, res) => {
  const { objects, error } = await req.eb.storage.list({ prefix: 'uploads/' })
  if (error) return res.status(500).json({ error })
  res.json(objects)
})

app.listen(3000)`;

	const rlsSql = `-- The policies the Express example relies on (SQL editor or a migration).
CREATE TABLE todos (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  title text NOT NULL,
  completed boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE todos ENABLE ROW LEVEL SECURITY;
CREATE POLICY "read own"   ON todos FOR SELECT USING (user_id = auth_uid());
CREATE POLICY "insert own" ON todos FOR INSERT WITH CHECK (user_id = auth_uid());`;

	const creditsSql = `-- Users may read their own grants; only the secret key may write.
CREATE TABLE credit_grants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  kind text NOT NULL,
  day date NOT NULL,
  amount integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, kind, day)            -- one daily bonus per user and day
);
ALTER TABLE credit_grants ENABLE ROW LEVEL SECURITY;
CREATE POLICY "read own" ON credit_grants FOR SELECT USING (user_id = auth_uid());
CREATE POLICY "service writes" ON credit_grants FOR ALL
  USING (is_service_role()) WITH CHECK (is_service_role());`;

	const creditsCode = `// Add to server.mjs, before app.listen: privileged work after checking the user.
app.post('/api/credits/daily', requireUser, async (req, res) => {
  // Your rule, enforced by your code: here, verified email addresses only.
  const { data: user, error: userError } = await eb.auth.getUser(req.get('authorization'))
  if (userError) return res.status(401).json({ error: userError })
  if (!user.email_confirmed_at) return res.status(403).json({ error: 'confirm your email first' })

  // The service role writes what the user can't. The user id comes from the
  // verified token — never from the request body.
  const { data, error, status } = await admin.db.from('credit_grants').insert({
    user_id: req.user.userId,
    kind: 'daily',
    day: new Date().toISOString().slice(0, 10),
    amount: 10,
  })
  // The UNIQUE constraint makes a second claim the same day fail: the
  // gateway answers 409 ("a record with this … already exists").
  if (status === 409) return res.status(409).json({ error: 'already claimed today' })
  if (error) {
    console.error('credit grant failed:', status, error)
    return res.status(500).json({ error: 'could not grant credits' })
  }
  res.status(201).json(data)
})

// Reading as the user shows the grant: their RLS policy allows it.
app.get('/api/credits', requireUser, async (req, res) => {
  const { data, error } = await req.eb.db.from('credit_grants').select('kind', 'day', 'amount')
  if (error) return res.status(500).json({ error })
  res.json({ total: data.reduce((sum, g) => sum + g.amount, 0), grants: data })
})`;

	const nextLibCode = `// lib/eurobase.ts — server-only helpers for a Next.js app (App Router).
import 'server-only'
import { cookies } from 'next/headers'
import { createClient } from '@eurobase/sdk'

const config = {
  url: process.env.EUROBASE_URL!,
  projectId: process.env.EUROBASE_PROJECT_ID!,
  auth: { persistSession: false },
}
export const eb = createClient({ ...config, apiKey: process.env.EUROBASE_PUBLIC_KEY! })
export const admin = createClient({ ...config, apiKey: process.env.EUROBASE_SECRET_KEY! })

export const ACCESS_COOKIE = 'eb_access'
export const REFRESH_COOKIE = 'eb_refresh'

// The caller of this request: from the Authorization header (mobile app,
// another service) or from the cookie the sign-in route sets (your pages).
export async function currentUser(request?: Request) {
  const token = request?.headers.get('authorization')
    ?? (await cookies()).get(ACCESS_COOKIE)?.value
  if (!token) return null
  const { data } = await eb.auth.verifyAccessToken(token)
  return data ? { ...data, eb: eb.withAccessToken(token) } : null
}`;

	const nextSigninCode = `// app/api/auth/signin/route.ts — signs in and keeps the session in httpOnly cookies.
import { NextResponse } from 'next/server'
import { cookies } from 'next/headers'
import { eb, ACCESS_COOKIE, REFRESH_COOKIE } from '@/lib/eurobase'

const cookie = { httpOnly: true, secure: true, sameSite: 'lax' as const, path: '/' }

export async function POST(request: Request) {
  const { email, password } = await request.json()
  const { data, error } = await eb.auth.signIn({ email, password })
  if (error || !data) return NextResponse.json({ error: error ?? 'sign-in failed' }, { status: 401 })
  const jar = await cookies()
  jar.set(ACCESS_COOKIE, data.access_token, { ...cookie, maxAge: data.expires_in })
  jar.set(REFRESH_COOKIE, data.refresh_token, { ...cookie, maxAge: 60 * 60 * 24 * 30 })
  return NextResponse.json({ user: data.user })
}`;

	const nextRefreshCode = `// app/api/auth/refresh/route.ts — your pages call it when a request answers 401.
import { NextResponse } from 'next/server'
import { cookies } from 'next/headers'
import { eb, ACCESS_COOKIE, REFRESH_COOKIE } from '@/lib/eurobase'

const cookie = { httpOnly: true, secure: true, sameSite: 'lax' as const, path: '/' }

export async function POST() {
  const jar = await cookies()
  const refreshToken = jar.get(REFRESH_COOKIE)?.value
  if (!refreshToken) return NextResponse.json({ error: 'signed out' }, { status: 401 })
  const { data, error } = await eb.auth.refreshSession(refreshToken)
  if (error || !data) {
    // Don't delete the cookies here. Refresh tokens are single-use: when two
    // tabs refresh at once, one wins and the other is refused with the old
    // token — deleting the cookies in that response would sign out the
    // winner too. Your sign-out route deletes them.
    return NextResponse.json({ error: error ?? 'signed out' }, { status: 401 })
  }
  jar.set(ACCESS_COOKIE, data.access_token, { ...cookie, maxAge: data.expires_in })
  jar.set(REFRESH_COOKIE, data.refresh_token, { ...cookie, maxAge: 60 * 60 * 24 * 30 }) // it rotates
  return NextResponse.json({ ok: true })
}`;

	const nextRouteCode = `// app/api/todos/route.ts — a route handler.
import { NextResponse } from 'next/server'
import { currentUser } from '@/lib/eurobase'

export async function GET(request: Request) {
  const user = await currentUser(request)
  if (!user) return NextResponse.json({ error: 'sign in first' }, { status: 401 })
  const { data, error } = await user.eb.db.from('todos').select('id', 'title', 'completed')
  if (error) return NextResponse.json({ error }, { status: 500 })
  return NextResponse.json(data)
}`;

	const nextActionCode = `// app/todos/actions.ts — a server action.
'use server'
import { revalidatePath } from 'next/cache'
import { currentUser } from '@/lib/eurobase'

export async function addTodo(formData: FormData) {
  const user = await currentUser()
  if (!user) throw new Error('Not signed in')
  const { error } = await user.eb.db.from('todos').insert({
    title: String(formData.get('title') ?? ''),
    user_id: user.userId,
  })
  if (error) throw new Error(error)
  revalidatePath('/todos')
}`;

	const workerCode = `// cleanup.mjs — a nightly job (system cron, a Kubernetes CronJob, a CI schedule):
//   EUROBASE_URL=… EUROBASE_SECRET_KEY=… node cleanup.mjs
import { createClient } from '@eurobase/sdk'

const admin = createClient({
  url: process.env.EUROBASE_URL,
  apiKey: process.env.EUROBASE_SECRET_KEY,
  auth: { persistSession: false },
})

const cutoff = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString()
let deleted = 0
for (;;) {
  const { data, error } = await admin.db
    .from('activity_log')
    .select('id')
    .lt('created_at', cutoff)
    .limit(200)
  if (error) {
    console.error('read failed:', error)
    process.exit(1)
  }
  if (data.length === 0) break
  for (const row of data) {
    const { error } = await admin.db.from('activity_log').delete(row.id)
    if (error) {
      console.error('delete failed:', row.id, error)
      process.exit(1)
    }
    deleted++
  }
}
console.log(\`deleted \${deleted} rows older than \${cutoff}\`)`;

	const mobileServerCode = `// auth-proxy.mjs — your backend signs users in for a mobile app.
import express from 'express'
import { eb } from './lib/eurobase.mjs'   // the public-key client, server mode

const app = express()
app.use(express.json())

// Returns the session to the app; nothing is kept on the server.
app.post('/auth/signin', async (req, res) => {
  const { data, error } = await eb.auth.signIn({
    email: String(req.body.email ?? ''),
    password: String(req.body.password ?? ''),
  })
  if (error) return res.status(error === 'email_not_confirmed' ? 403 : 401).json({ error })
  res.json(data)   // { access_token, refresh_token, expires_in, expires_at, user }
})

app.post('/auth/signup', async (req, res) => {
  const { data, user, confirmationRequired, error } = await eb.auth.signUp({
    email: String(req.body.email ?? ''),
    password: String(req.body.password ?? ''),
    emailRedirectTo: 'https://example.com/verify',   // must be in Auth → Redirect URLs
  })
  if (error) return res.status(400).json({ error })
  if (confirmationRequired) return res.status(202).json({ confirmationRequired, user })
  res.status(201).json(data)
})

app.post('/auth/refresh', async (req, res) => {
  const { data, error } = await eb.auth.refreshSession(String(req.body.refresh_token ?? ''))
  if (error) return res.status(401).json({ error })
  res.json(data)   // a new refresh_token: the old one no longer works
})

app.post('/auth/signout', async (req, res) => {
  await eb.auth.signOut(String(req.body.refresh_token ?? ''))
  res.status(204).end()
})

// Sending emails through your server shares one per-IP sending ceiling among
// all your users (see the rate-limit note below); the app can call
// eb.auth.forgotPassword() directly with the public key instead.
app.post('/auth/forgot-password', async (req, res) => {
  await eb.auth.forgotPassword(String(req.body.email ?? ''), {
    emailRedirectTo: 'https://example.com/reset-password',
  })
  res.status(204).end()   // same answer whether or not the address exists
})

app.post('/auth/reset-password', async (req, res) => {
  const { error } = await eb.auth.resetPassword(String(req.body.token ?? ''), String(req.body.password ?? ''))
  if (error) return res.status(400).json({ error })
  res.status(204).end()
})

app.listen(3000)`;

	const mobileAppCode = `// In the React Native app (Expo): keep the refresh token in the keychain.
import * as SecureStore from 'expo-secure-store'

const API = 'https://api.example.com'
let accessToken = null

export async function signIn(email, password) {
  const res = await fetch(API + '/auth/signin', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  const body = await res.json()
  if (!res.ok) throw new Error(body.error)
  accessToken = body.access_token
  await SecureStore.setItemAsync('refresh_token', body.refresh_token)
}

// One refresh at a time: refresh tokens are single-use, so parallel 401s
// must share one refresh instead of each using the same token.
let refreshing = null
function refresh() {
  refreshing ??= (async () => {
    const refresh_token = await SecureStore.getItemAsync('refresh_token')
    const r = await fetch(API + '/auth/refresh', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ refresh_token }),
    })
    if (!r.ok) throw new Error('signed out')
    const session = await r.json()
    accessToken = session.access_token
    await SecureStore.setItemAsync('refresh_token', session.refresh_token)
  })().finally(() => { refreshing = null })
  return refreshing
}

// Calls your API; on 401 refreshes (shared) and retries once.
export async function api(path, init = {}) {
  const call = () => fetch(API + path, {
    ...init,
    headers: { ...init.headers, authorization: 'Bearer ' + accessToken },
  })
  let res = await call()
  if (res.status === 401) {
    await refresh()
    res = await call()
  }
  return res
}`;

	const oauthCode = `// On the callback page in the browser (a client that keeps its session):
const { data: session, error } = eb.auth.handleOAuthCallback()
if (session) {
  // Hand it to your backend, which stores it the way the sign-in route does.
  await fetch('/api/auth/session', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ access_token: session.access_token, refresh_token: session.refresh_token }),
  })
}`;

	const oauthServerCode = `// app/api/auth/session/route.ts — takes the session from the callback page.
import { NextResponse } from 'next/server'
import { cookies } from 'next/headers'
import { eb, ACCESS_COOKIE, REFRESH_COOKIE } from '@/lib/eurobase'

const APP_ORIGIN = 'https://app.example.com'
const cookie = { httpOnly: true, secure: true, sameSite: 'lax' as const, path: '/' }

export async function POST(request: Request) {
  // Login CSRF: without these checks another site could post ITS session here
  // and sign your visitor into the attacker's account. A cross-site form can't
  // send JSON, and the browser sets Origin itself.
  if (request.headers.get('content-type')?.split(';')[0].trim() !== 'application/json'
      || request.headers.get('origin') !== APP_ORIGIN) {
    return NextResponse.json({ error: 'forbidden' }, { status: 403 })
  }
  const { access_token, refresh_token } = await request.json()
  const { data, error } = await eb.auth.verifyAccessToken(String(access_token ?? ''))
  if (error || typeof refresh_token !== 'string') return NextResponse.json({ error: 'invalid session' }, { status: 401 })
  const jar = await cookies()
  jar.set(ACCESS_COOKIE, access_token, { ...cookie, maxAge: data.expiresAt - Math.floor(Date.now() / 1000) })
  jar.set(REFRESH_COOKIE, refresh_token, { ...cookie, maxAge: 60 * 60 * 24 * 30 })
  return NextResponse.json({ user: { id: data.userId, email: data.email } })
}`;
</script>

{#snippet code(id: string, src: string)}
	<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
		<button
			onclick={() => copyCode(src, id)}
			class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
		>
			{copiedId === id ? 'Copied!' : 'Copy'}
		</button>
		<pre>{src}</pre>
	</div>
{/snippet}

<section id="server-side" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">32. Using the SDK on a server</h2>
			<p class="text-sm italic text-gray-500 mb-4">Your own backend serves many users with one SDK client: check who is calling, act as that user under row-level security, or do privileged work with the secret key — in Node, Deno, Bun or an edge runtime.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">You need this chapter when your app talks to a backend of your own — an Express or Hono API, Next.js route handlers and server actions, a worker or a scheduled script — and that backend calls Eurobase. A browser or mobile app that talks to Eurobase directly doesn't; there, one signed-in user per client is exactly right. Requires <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code> 0.13.0 or later (0.13.1 or later if your server uses CommonJS — <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">require()</code>). Anything you'd otherwise write as server code can also be an <a href="/docs/edge-functions" class="text-eurobase-700 hover:underline">edge function</a>, which gets the caller's identity without any of this.</p>

				<h3 id="rule" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">The one rule</h3>
				<p class="text-sm text-amber-800 bg-amber-50 border-l-4 border-amber-400 px-3 py-2 rounded leading-relaxed">Never sign in on a client that is shared by requests. By default a client keeps the session of the user who signed in and sends that user's token on every later call. On a server, a module-level client would then answer the next visitor's request as the previous visitor. Create shared clients with <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">auth: &#123; persistSession: false &#125;</code> and act as a user with <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">eb.withAccessToken(token)</code>, per request.</p>
				<p class="text-sm text-gray-700 leading-relaxed">With <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">persistSession: false</code> the client keeps no session at all — not in storage, not in memory. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signIn</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signUp</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signInWithMagicLink</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">refreshSession(refreshToken)</code> still return the session, for you to hand back to your app; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">getSession()</code> stays <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">null</code> and no auth events fire. A client in a server runtime that signs in without the setting prints a one-time warning; set <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">persistSession</code> explicitly (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">true</code> for a single-user script) to silence it.</p>

				<h3 id="setup" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Two clients</h3>
				{@render code('setup', setupCode)}
				<table class="w-full text-sm text-gray-700 border-collapse mt-3">
					<thead>
						<tr class="border-b border-gray-200 text-left">
							<th class="py-2 pr-3 font-semibold">Client</th>
							<th class="py-2 pr-3 font-semibold">Acts as</th>
							<th class="py-2 font-semibold">Use for</th>
						</tr>
					</thead>
					<tbody>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb</code> (public key)</td>
							<td class="py-2 pr-3">anonymous; after <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">withAccessToken(token)</code> the token's user, under RLS</td>
							<td class="py-2">verifying tokens, auth calls on a user's behalf, everything the user may do themselves</td>
						</tr>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">admin</code> (secret key)</td>
							<td class="py-2 pr-3">the service role: RLS is skipped (on tables whose policies admit <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">is_service_role()</code>)</td>
							<td class="py-2">work users can't do themselves, jobs, vault, schedules, schema changes</td>
						</tr>
					</tbody>
				</table>
				<p class="text-sm text-gray-700 leading-relaxed">The secret key stays in your server's environment; never send it to an app or put it in a URL. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">withAccessToken()</code> throws on a secret-key client: the gateway runs every secret-key call as the service role, so a user's token there would not limit anything. Default to the user client and reach for <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">admin</code> only after your own code has decided the action is allowed.</p>

				<h3 id="verify" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Verifying the caller's token</h3>
				<p class="text-sm text-gray-700 leading-relaxed">Your app sends the user's access token as <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Authorization: Bearer …</code> (in the browser: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.getSession()?.access_token</code>). <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.verifyAccessToken()</code> takes the token with or without the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Bearer </code> prefix:</p>
				{@render code('verify', verifyCode)}
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li><strong>Locally, no request per token.</strong> Access tokens are signed with ES256 using your project's own key pair (see <a href="/docs/auth" class="text-eurobase-700 hover:underline">Authentication</a>). The SDK fetches the public key once from <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/v1/auth/.well-known/jwks.json</code> on the client's <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">url</code> — so <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">url</code> must be your project's own address, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">https://&lt;project&gt;.eurobase.app</code> — and verifies the signature with WebCrypto.</li>
					<li><strong>What is checked:</strong> the signature, expiry (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">exp</code>), issuer <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase</code>, audience (your project ID; set <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">projectId</code> on the client so it's compared) and that it is an end-user token. Platform tokens, tokens of other projects and anything unsigned are refused.</li>
					<li><strong>Cache:</strong> the key is kept for an hour and survives a failed re-read. A token naming a key the SDK doesn't know makes it re-read the key set at most once a minute; such tokens are refused, never sent on to the gateway.</li>
					<li><strong>Fallback to the gateway:</strong> when the runtime can't check locally — no WebCrypto (Node 18), or the key set can't be fetched — the token is checked with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /v1/auth/user</code> instead (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">verifiedBy: 'server'</code>). A token the gateway refused is refused again for 60 seconds without a request. Pass <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">&#123; serverFallback: false &#125;</code> to refuse those cases instead.</li>
					<li><strong>HS256 tokens are refused</strong> (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">'HS256 tokens are not accepted'</code>): new tokens are ES256, and anyone can write a token that claims HS256 — sending each one to the gateway would spend your server's request budget. Only tokens issued before ES256 signing was switched on are HS256; while yours may still have such tokens in use, pass <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">&#123; allowHS256: true &#125;</code> to have the gateway check them.</li>
					<li><strong>The token, not the user:</strong> <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">verifyAccessToken</code> checks the token only. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.getUser(token)</code> asks the gateway, which also checks that the user still exists (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">'user not found'</code>) and isn't suspended (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">'account_suspended'</code>, HTTP 403), and returns the current profile. It costs a request; use it before sensitive actions.</li>
				</ul>

				<h3 id="as-user" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Acting as the user</h3>
				<p class="text-sm text-gray-700 leading-relaxed"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.withAccessToken(token)</code> returns a small client with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">functions</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">realtime</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth</code> that sends that token on every call. The gateway checks the token on each request and applies your RLS policies as that user, exactly as if the app had made the call; edge functions see the user as <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">ctx.user</code>. Creating one is cheap and it holds no shared state, so make one per request. Its <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth</code> covers the user's own account: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">getUser()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">updatePassword()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">exportMyData()</code>. If you open a realtime subscription with it, call <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">realtime.disconnect()</code> when you are done. Its token can't be renewed: once it expires the client stops reconnecting and reports it through <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">realtime.onError(cb)</code> (a warning in the log without a listener); open a new one with a fresh token.</p>

				<h3 id="express" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Example: an Express API</h3>
				<p class="text-sm text-gray-700 leading-relaxed">Middleware that checks the token and puts the user and a per-request client on <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">req</code>; routes that read and write the caller's rows under RLS. It uses <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">lib/eurobase.mjs</code> from above.</p>
				{@render code('express', expressCode)}
				{@render code('express-sql', rlsSql)}

				<h3 id="privileged" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Example: privileged work after checking the user</h3>
				<p class="text-sm text-gray-700 leading-relaxed">Users may read their credits but not grant themselves any. Your server decides, then writes with the secret key. Take the user's id from the verified token, never from the request body, and let the database enforce limits (here a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">UNIQUE</code> constraint) so two parallel requests can't both succeed.</p>
				{@render code('credits-sql', creditsSql)}
				{@render code('credits', creditsCode)}

				<h3 id="nextjs" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Example: Next.js route handlers and server actions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">Server components and actions have no <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">localStorage</code>; keep the session in httpOnly cookies set by your own sign-in route, and accept a bearer token from other callers.</p>
				{@render code('next-lib', nextLibCode)}
				{@render code('next-signin', nextSigninCode)}
				{@render code('next-refresh', nextRefreshCode)}
				<p class="text-sm text-gray-700 leading-relaxed">In the browser, call the refresh route single-flight: keep the pending request in a variable and let every 401 in the tab wait for that one, then retry once. Tabs can still collide; after a refused refresh, retry the original request once (another tab may have stored new cookies) before you show the sign-in page.</p>
				{@render code('next-route', nextRouteCode)}
				{@render code('next-action', nextActionCode)}

				<h3 id="worker" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Example: a scheduled script with the secret key</h3>
				<p class="text-sm text-gray-700 leading-relaxed">A job that acts for no user uses the secret key alone. If the job only needs SQL or one of your functions, a <a href="/docs/cron" class="text-eurobase-700 hover:underline">scheduled job</a> in Eurobase does it without a server.</p>
				{@render code('worker', workerCode)}

				<h3 id="proxy" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Example: a backend that signs users in for a mobile app</h3>
				<p class="text-sm text-gray-700 leading-relaxed">An app can call Eurobase's auth directly with the public key. Route it through your backend when you want your own logic around it. The backend returns the session and keeps nothing; the app stores the refresh token in the device's secure storage (Keychain / Keystore) and sends the access token to your API.</p>
				{@render code('proxy-server', mobileServerCode)}
				{@render code('proxy-app', mobileAppCode)}
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li><strong>Refresh tokens rotate:</strong> each refresh returns a new one and the old one stops working. Store the new one every time, and don't refresh the same token twice in parallel.</li>
					<li><strong>If your server stores refresh tokens</strong> (cookies are fine; a database is a choice), treat them like passwords: encrypt them, never log them, and revoke them with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signOut(refreshToken)</code> when the user signs out.</li>
					<li><strong>Rate limits count your server's address.</strong> When every user comes through your backend, they all share its IP (see <a href="/docs/rate-limits" class="text-eurobase-700 hover:underline">Rate limits</a>).
						<ul class="list-[circle] pl-5 mt-1 space-y-1">
							<li><strong>You can raise</strong> (Auth → Rate Limits) the per-IP limits on sign-up + sign-in (at most 30 per 5 minutes), token refresh, and token verification (magic-link sign-in, phone OTP, completing a password reset, email verification; at most 150 per 5 minutes).</li>
							<li><strong>You can't raise</strong> the per-IP sending ceilings — 60 password-reset emails, 60 verification resends and 300 magic-link requests an hour, 30 SMS codes an hour — nor the per-address limits (3 password-reset emails and 3 magic links per 15 minutes, one verification resend per 5 minutes) or the sign-in failure limit (5 wrong passwords per email address per 15 minutes). The per-address limits are per user, so a proxy doesn't make them shared; the per-IP ceilings are. Let the app call <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">forgotPassword</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">requestMagicLink</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">resendVerification</code> directly with the public key, so each user counts against their own address.</li>
							<li>Rate-limit by client address on your own backend too.</li>
						</ul>
					</li>
					<li><strong>Email confirmation:</strong> with <strong>Require email confirmation</strong> on, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signUp</code> returns <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">confirmationRequired: true</code> and no session, and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signIn</code> answers <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_not_confirmed</code> until the user confirms; offer <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">resendVerification(email)</code>.</li>
				</ul>

				<h3 id="password-reset" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Password reset</h3>
				<ol class="list-decimal pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li>Your backend calls <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.forgotPassword(email, &#123; emailRedirectTo &#125;)</code>. It answers the same whether or not the address is registered, so it reveals nothing; answer your caller the same way. The URL must be in Auth → Redirect URLs.</li>
					<li>The email links to that URL with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">?token=…</code>. Your page (or the app, via a deep link) reads the token and asks for the new password.</li>
					<li>It sends both to your backend, which calls <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.resetPassword(token, newPassword)</code>. The user then signs in with the new password.</li>
				</ol>
				<p class="text-sm text-gray-700 leading-relaxed">A signed-in user who knows their password changes it with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.withAccessToken(token).auth.updatePassword(&#123; currentPassword, newPassword &#125;)</code>; it returns a new session, and the user's other refresh tokens stop working.</p>

				<h3 id="not-on-server" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">What happens in the browser or app, not on the server</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li><strong>OAuth sign-in</strong> (Google, Apple, …) is a browser redirect flow. The gateway sends the user back to your <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">redirect_url</code> with the session in the URL fragment (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">#access_token=…</code>), which browsers never send to a server — so a server can't complete it. Start it in the browser (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signInWithOAuth</code>) or the app (an in-app browser with a deep-link redirect URL in your allowlist), read the session on the callback page, and pass it to your backend if it needs one:</li>
				</ul>
				{@render code('oauth', oauthCode)}
				<p class="text-sm text-gray-700 leading-relaxed">The route that receives it must accept only JSON from your own origin: otherwise another site could post its own session to it and sign your visitor into the attacker's account (login CSRF).</p>
				{@render code('oauth-server', oauthServerCode)}
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li><strong>Passkeys</strong> need the browser's or device's authenticator; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">passkeysSupported()</code> is always false on a server.</li>
					<li><strong>Realtime</strong> with a user's token works from a server too, but the token travels in the WebSocket URL; with the secret key, don't use <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.realtime</code> — connect with a WebSocket client that sends the key in a header (see the SDK README, "Realtime → Server-side clients"). Realtime filters by the owner column, not by custom RLS policies.</li>
				</ul>

				<h3 id="limits" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Limits</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li><strong>An access token stays valid until it expires</strong> — also after the user signs out, changes their password, or is suspended or deleted. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">verifyAccessToken</code> can't see those, and the gateway still accepts the token for data calls (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">withAccessToken</code>) until it expires. Keep <strong>Session duration</strong> short (Auth settings), and call <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">getUser(token)</code> before sensitive actions: it refuses deleted and suspended users. A suspended user's refresh is refused, so their session ends at the next refresh.</li>
					<li>Signing out revokes the refresh token only.</li>
					<li>Node 18 has no WebCrypto global: tokens are then checked by the gateway, one request each. Node 20 or later, Deno, Bun and edge runtimes verify locally.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">To create, list, update or delete users from your server (an admin panel, an import), see <a href="/docs/user-admin" class="text-eurobase-700 hover:underline">Managing users from your server</a>.</p>

				<h3 id="from-supabase" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Coming from supabase-js on the server</h3>
				<table class="w-full text-sm text-gray-700 border-collapse">
					<thead>
						<tr class="border-b border-gray-200 text-left">
							<th class="py-2 pr-3 font-semibold">supabase-js</th>
							<th class="py-2 font-semibold">@eurobase/sdk</th>
						</tr>
					</thead>
					<tbody>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="text-xs font-mono">createClient(url, key, &#123; auth: &#123; persistSession: false, autoRefreshToken: false &#125; &#125;)</code></td>
							<td class="py-2"><code class="text-xs font-mono">createClient(&#123; url, apiKey, projectId, auth: &#123; persistSession: false &#125; &#125;)</code> — keeps no session at all, not even in memory (supabase-js still keeps one in memory). <code class="text-xs font-mono">detectSessionInUrl</code> has no equivalent: the SDK never reads the URL on its own.</td>
						</tr>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="text-xs font-mono">createClient(url, anonKey, &#123; global: &#123; headers: &#123; Authorization: 'Bearer ' + jwt &#125; &#125; &#125;)</code></td>
							<td class="py-2"><code class="text-xs font-mono">eb.withAccessToken(jwt)</code> on the public-key client</td>
						</tr>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="text-xs font-mono">supabase.auth.getUser(jwt)</code></td>
							<td class="py-2"><code class="text-xs font-mono">eb.auth.getUser(jwt)</code> (asks the gateway; also refuses suspended and deleted users), or <code class="text-xs font-mono">eb.auth.verifyAccessToken(jwt)</code> (local)</td>
						</tr>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="text-xs font-mono">supabase.auth.getClaims(jwt)</code> / <code class="text-xs font-mono">jose</code> with the JWT secret</td>
							<td class="py-2"><code class="text-xs font-mono">eb.auth.verifyAccessToken(jwt)</code> — public key, nothing secret on your server</td>
						</tr>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="text-xs font-mono">createClient(url, serviceRoleKey)</code></td>
							<td class="py-2"><code class="text-xs font-mono">createClient(&#123; url, apiKey: secretKey, auth: &#123; persistSession: false &#125; &#125;)</code></td>
						</tr>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="text-xs font-mono">auth.refreshSession(&#123; refresh_token &#125;)</code></td>
							<td class="py-2"><code class="text-xs font-mono">auth.refreshSession(refreshToken)</code> (a session object works too)</td>
						</tr>
						<tr class="border-b border-gray-100 align-top">
							<td class="py-2 pr-3"><code class="text-xs font-mono">auth.admin.signOut(jwt)</code></td>
							<td class="py-2"><code class="text-xs font-mono">auth.signOut(refreshToken)</code> — revokes the refresh token; the access token runs until it expires</td>
						</tr>
					</tbody>
				</table>
			</div>
		</section>
