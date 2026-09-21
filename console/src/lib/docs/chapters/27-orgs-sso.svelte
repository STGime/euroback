		<section id="orgs-sso" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">27. Organizations &amp; SSO <span class="text-sm font-normal text-emerald-700">(Team &amp; Legal Team)</span></h2>
			<p class="text-sm italic text-gray-500 mb-4">
				Alex hires Bea. She needs her own login for LexVault's projects — no shared passwords, no forwarded API keys.
			</p>

			<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900 mb-4">
				<strong>Not the same as chapter 19 (Team Collaboration).</strong> That covers <strong>project-level members</strong>
				(Viewer / Developer / Admin / Owner) — one project shared with individually-invited teammates. This chapter is about
				<strong>organizations</strong> — a company-level container with OIDC SSO login, member roles across the whole org,
				and orgs owning projects. On Team you can use either or both.
			</div>

			<div class="rounded-xl border border-gray-200 bg-white p-6 space-y-4">
				<h3 class="text-base font-semibold text-gray-900">What Alex is setting up</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1.5">
					<li>A <strong>LexVault</strong> organization that owns the Team project from chapter 26.</li>
					<li>Google Workspace as the identity provider (any OIDC-conformant provider works — Microsoft Entra ID, Okta, Authentik, custom).</li>
					<li>Bea invited to the org as a <strong>member</strong> — she'll sign in with her <code class="rounded bg-gray-100 px-1 text-[11px]">@lexvault.eu</code> Google account and see the LexVault project alongside her own.</li>
				</ul>

				<h3 class="text-base font-semibold text-gray-900 pt-2">1. Create the organization</h3>
				<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900">
					<strong>Team-tier feature.</strong> Organizations require <code class="rounded bg-amber-100 px-1">team_beta_access</code> on your account.
					Fresh accounts don't have it and won't see the <strong>Organizations</strong> entry in the sidebar or the <strong>Create organization</strong> button.
					Email <a href="mailto:contact@eurobase.app" class="underline hover:no-underline">contact@eurobase.app</a> or use the request form on the pricing page to ask for the beta grant.
				</div>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-1.5">
					<li>Console → <strong>Organizations</strong> → <strong>Create organization</strong>.</li>
					<li>Give it a name (<em>"LexVault"</em>) and submit.</li>
					<li>Alex is now the sole admin. Nothing broke on the project side — Alex still owns their existing project directly.</li>
					<li>
						<strong>One org per user, first release.</strong> After you create your first org, the <strong>Create organization</strong> button
						hides and the API refuses a second org for the same creator with <code class="rounded bg-gray-100 px-1 text-[11px]">409 you already own an organization</code>.
						Multi-org support (each with its own SSO) is on the roadmap; ping us on Discord if it blocks your setup.
					</li>
				</ol>

				<h3 class="text-base font-semibold text-gray-900 pt-2">2. Register Eurobase as an OIDC client at Google</h3>
				<p class="text-sm text-gray-700">You'll need four values from Google Cloud Console:</p>
				<div class="rounded-md border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50 text-left">
							<tr>
								<th class="px-3 py-2 font-medium text-gray-600">Field</th>
								<th class="px-3 py-2 font-medium text-gray-600">What it is</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100 text-gray-700">
							<tr><td class="px-3 py-2 font-mono">Issuer</td><td class="px-3 py-2"><code>https://accounts.google.com</code></td></tr>
							<tr><td class="px-3 py-2 font-mono">Client ID</td><td class="px-3 py-2">From your OAuth client registration.</td></tr>
							<tr><td class="px-3 py-2 font-mono">Client Secret</td><td class="px-3 py-2">Kept only server-side, sealed with AES-256-GCM.</td></tr>
							<tr><td class="px-3 py-2 font-mono">Redirect URL</td><td class="px-3 py-2">Register <strong>exactly</strong> <code>https://api.eurobase.app/platform/auth/sso/callback</code> at Google.</td></tr>
						</tbody>
					</table>
				</div>

				<div class="text-sm text-gray-700">
					<p class="font-medium text-gray-900 mb-1">Google Workspace walkthrough:</p>
					<ol class="list-decimal pl-5 space-y-1">
						<li>Google Cloud Console → <strong>APIs &amp; Services</strong> → <strong>Credentials</strong>.</li>
						<li><strong>Create Credentials</strong> → <strong>OAuth client ID</strong>.</li>
						<li>Application type: <strong>Web application</strong>. Name it <em>"Eurobase"</em>.</li>
						<li>Authorised redirect URIs: <code class="rounded bg-gray-100 px-1 text-[11px]">https://api.eurobase.app/platform/auth/sso/callback</code></li>
						<li>Save. Copy the Client ID + Client Secret.</li>
					</ol>
				</div>

				<div class="rounded-md bg-blue-50 border border-blue-200 p-3 text-xs text-blue-900">
					<strong>Same redirect URL for every org.</strong> The callback resolves the org from a signed
					<code class="rounded bg-blue-100 px-1">state</code> parameter, so registering
					<code class="rounded bg-blue-100 px-1">/platform/auth/sso/callback</code> once at the IdP is enough — no
					per-org URI edits.
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">3. Save the OIDC config in Eurobase</h3>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-1.5">
					<li>Console → <strong>Organizations</strong> → LexVault → <strong>SSO</strong> panel.</li>
					<li>
						Fill in <em>Provider</em> (<code class="rounded bg-gray-100 px-1 text-[11px]">google</code>),
						<em>Issuer</em>, <em>Client ID</em>, paste the <em>Client Secret</em>, and the redirect URL above.
					</li>
					<li>
						Save. The panel shows <code class="rounded bg-gray-100 px-1 text-[11px]">•••••••• (set)</code> once
						stored. To rotate later, paste a new secret and save again — old value overwritten.
					</li>
				</ol>

				<h3 class="text-base font-semibold text-gray-900 pt-2">4. Invite Bea</h3>
				<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900">
					<strong>Prerequisite.</strong> Bea must sign up as a regular Eurobase user first (Free tier is fine).
					The invite endpoint refuses email addresses that don't already exist in <code class="rounded bg-amber-100 px-1">platform_users</code>
					— you'll see <em>"no platform user with that email — user must sign up first"</em> otherwise.
				</div>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-1.5">
					<li>Bea creates a Eurobase account at <code class="rounded bg-gray-100 px-1 text-[11px]">console.eurobase.app</code> with <code class="rounded bg-gray-100 px-1 text-[11px]">bea@lexvault.eu</code> — the same email that lives in her Google Workspace.</li>
					<li>Alex → Organizations → LexVault → <strong>Members</strong> → <strong>Add member</strong> → paste <code class="rounded bg-gray-100 px-1 text-[11px]">bea@lexvault.eu</code>.</li>
					<li>Role: <strong>member</strong> (can't invite others or edit SSO). <strong>admin</strong> would let her manage the org config.</li>
					<li>Save.</li>
				</ol>
				<p class="text-sm text-gray-700">
					Bea receives a notification email from Eurobase — subject <em>"You've been added to LexVault on Eurobase"</em>
					— naming Alex as the inviter and linking to the sign-in page. The DB row is written before the mail is
					sent, so a mail-provider hiccup never leaves her in a half-invited state.
				</p>

				<h3 class="text-base font-semibold text-gray-900 pt-2">5. Bea signs in with SSO</h3>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-1.5">
					<li>Bea opens <code class="rounded bg-gray-100 px-1 text-[11px]">console.eurobase.app/login</code>.</li>
					<li>Clicks <strong>Sign in with SSO</strong> and enters <code class="rounded bg-gray-100 px-1 text-[11px]">bea@lexvault.eu</code>.</li>
					<li>
						Redirected to Google, completes the Workspace login, comes back to Eurobase.
						Backend validates the ID token against LexVault's OIDC config, checks Bea's email is in
						<code class="rounded bg-gray-100 px-1 text-[11px]">org_members</code>, and issues a platform session.
					</li>
					<li>Bea lands on the projects page with LexVault's project visible.</li>
				</ol>

				<h3 class="text-base font-semibold text-gray-900 pt-2">6. Attach a project to the org</h3>
				<p class="text-sm text-gray-700">
					Projects attach to an org <strong>at creation time</strong>. Every project Alex creates while admin of LexVault auto-attaches to LexVault by default,
					and the create-project wizard shows an <strong>Owner</strong> picker (see chapter 2) so Alex can pick <strong>Personal</strong> per-project when they don't want a project to be org-visible.
					Any org-attached project shows up in <em>every</em> LexVault member's project list with an <strong>Org</strong> badge.
				</p>
				<div class="rounded-md bg-emerald-50 border border-emerald-200 p-3 text-xs text-emerald-900">
					<strong>Org membership grants project access.</strong> Every route on an org-attached project — project middleware, storage, realtime, DDL — unions the caller's direct <code class="rounded bg-emerald-100 px-1">project_members</code> row (chapter 19) with the org's <code class="rounded bg-emerald-100 px-1">org_members</code> row via <code class="rounded bg-emerald-100 px-1">IsProjectAccessible</code>. Bea sees the project in her list AND can open it in a single click — no separate per-project invite required for read access.
					<br /><br />
					<strong>Role mapping</strong> (conservative by default; see chapter 19 to widen per-project):
					<ul class="list-disc pl-5 mt-1 space-y-1">
						<li>Org <strong>admin</strong> → project <strong>admin</strong>. Mirrors <code class="rounded bg-emerald-100 px-1">SetProjectOrg</code>'s auth check — "admin of the org that owns the project" ≈ "admin of the project." Can manage members, save Settings, run DDL, deploy edge functions, etc.</li>
						<li>Org <strong>member</strong> → project <strong>viewer</strong> (read-only). Can open the project, browse tables, read rows, read logs. Cannot run <code class="rounded bg-emerald-100 px-1">/sql</code> writes, deploy edge functions, change schema, upload storage, manage webhooks / cron / migrations. To grant an org member write access to a specific project, add them under that project's <strong>Members</strong> tab (chapter 19) with the role you want — the effective role is the higher of the direct membership and the org-derived one.</li>
					</ul>
					<span class="italic">Why viewer as the default:</span> RequireMinRole("developer") gates arbitrary SQL, DDL, function deploy (including RLS-bypass flags), storage writes, and more. Granting all that to every org member on every org project automatically would be a much larger surface than the least-privilege intent of the org / SSO series. Widening later is easy; narrowing after users depend on writes is not.
				</div>
				<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900">
					<strong>Existing projects (created before the org): no self-serve move today.</strong>
					The API endpoint (<code class="rounded bg-amber-100 px-1">PATCH /platform/projects/{'{id}'}/org</code>) exists, but no console UI is wired to it yet.
					If you have a personal project that predates your org and want to move it in (or vice versa), reply to any support email or ping us on Discord — we'll flip <code class="rounded bg-amber-100 px-1">org_id</code> for you.
					A self-serve <em>Owner</em> control on the project settings page is tracked as a follow-up.
				</div>
				<div class="rounded-md bg-blue-50 border border-blue-200 p-3 text-xs text-blue-900">
					<strong>Pro checkout now honours the picker.</strong> The wizard's Owner choice is persisted on the checkout intent (including across the billing-profile detour) and passed to project creation in the Mollie payment webhook. Two edge cases fall back safely to personal so nobody loses money after paying: (1) target org deleted between checkout and payment confirmation, and (2) caller's admin membership revoked between the two. Both events log a distinct WARN for ops. Every plan — Free, Pro, Team, Legal Team — respects the picker.
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">7. Require SSO for every org project</h3>
				<p class="text-sm text-gray-700">
					Once SSO is configured, you can lock every project the org owns behind SSO login. Turning this on stops password sessions from reaching any project attached to the org — even for the admin who flipped the switch, until they re-sign-in via SSO.
				</p>
				<ol class="list-decimal pl-5 text-sm text-gray-700 space-y-1.5">
					<li>
						Console → <strong>Organizations</strong> → LexVault → <strong>Require SSO</strong> toggle → ON.
						The toggle fires the PATCH the moment you click it — there is no confirmation modal today, so the switch is live before you finish reading this sentence. Have an SSO login ready first (see the red callout below).
					</li>
					<li>
						Any subsequent request from a password session hits <code class="rounded bg-gray-100 px-1 text-[11px]">403 sso_required_for_org</code>
						at every org-owned access site — ListProjects filters the org's projects out; the project middleware refuses direct navigation; the org detail page refuses reads; the WebSocket realtime upgrade refuses subscribes. Enforcement is re-checked on every request, so flipping the toggle takes effect without waiting for sessions to expire.
					</li>
					<li>
						When a user's password session hits an org-owned <em>org detail</em> route (<code class="rounded bg-gray-100 px-1 text-[11px]">/platform/orgs/&lt;id&gt;</code>), the console sends them to <code class="rounded bg-gray-100 px-1 text-[11px]">/login?sso_required_for=&lt;org&gt;</code>. On project or realtime 403s the browser goes to plain <code class="rounded bg-gray-100 px-1 text-[11px]">/login</code>; the user picks <strong>Sign in with SSO</strong> themselves and enters their email. Deep-link handling of the <code class="rounded bg-gray-100 px-1 text-[11px]">sso_required_for</code> query param on the login page is a follow-up.
					</li>
				</ol>
				<div class="rounded-md bg-red-50 border border-red-200 p-3 text-xs text-red-900">
					<strong>No admin exemption.</strong> The <em>Require SSO</em> toggle applies to every session equally — admin, member, or superadmin. If you flip it while signed in with a password, your <em>very next request</em> to a project belonging to the org gets 403. Have an SSO login ready before you enable it, or you'll immediately lock yourself out until you re-sign-in via SSO.
				</div>
				<div class="rounded-md bg-gray-50 border border-gray-200 p-3 text-xs text-gray-700">
					<strong>Turning it off.</strong> Same toggle — but only from an SSO-authed session for this org. The <code class="rounded bg-gray-100 px-1">PATCH /platform/orgs/&lt;id&gt;/sso-required</code> handler applies the same SSO gate to itself once <code class="rounded bg-gray-100 px-1">sso_required</code> is on, so a password session can't turn it back off (that's the "lock yourself out" case above — sign in via SSO first). Once flipped off, password sessions immediately regain access on the next request; no session invalidation, no re-login. Enforcement lives on the org row (<code class="rounded bg-gray-100 px-1">organizations.sso_required</code>) and is read fresh on every check.
				</div>
				<div class="rounded-md bg-gray-50 border border-gray-200 p-3 text-xs text-gray-700">
					<strong>SSO for org A doesn't unlock org B.</strong> If a user is a member of two orgs — both with <em>Require SSO</em> on — signing in via A's IdP grants access to A's projects only. B still refuses that session until the user signs in via B's IdP. Cross-org access requires SSO for each org whose <em>Require SSO</em> is on.
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">Troubleshooting</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1.5">
					<li>
						<strong>"no platform user with that email — user must sign up first"</strong> — the invitee hasn't
						created a Eurobase account yet. Ask them to sign up first, then retry the invite. The email must match
						<em>exactly</em> — case-insensitive OK, but <code class="rounded bg-gray-100 px-1 text-[11px]">+tag</code>
						aliases like <code class="rounded bg-gray-100 px-1 text-[11px]">bea+eurobase@lexvault.eu</code> won't.
					</li>
					<li>
						<strong>"SSO sign-in failed"</strong> after the IdP redirect — three usual causes: (1) the returned
						email isn't in <code class="rounded bg-gray-100 px-1 text-[11px]">org_members</code>; (2) the redirect
						URI at Google doesn't match the exact <code class="rounded bg-gray-100 px-1 text-[11px]">/platform/auth/sso/callback</code>
						path; (3) the client secret in the SSO panel doesn't match the current secret at Google.
					</li>
					<li>
						<strong>"SSO requires a Team-tier organization to have invited you"</strong> — the person hit
						<em>Sign in with SSO</em> but no org has invited them under that email. Add them via the members panel.
					</li>
				</ul>

				<h3 class="text-base font-semibold text-gray-900 pt-2">Security notes</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li>Client secret is encrypted at rest with AES-256-GCM using a server-side platform key (<code class="rounded bg-gray-100 px-1 text-[11px]">PLATFORM_ENCRYPTION_KEY</code>). Never persisted plaintext.</li>
					<li>Access to org-owned projects is gated on <code class="rounded bg-gray-100 px-1 text-[11px]">org_members</code>: an SSO login only succeeds for an email an org admin has already invited. Two properties fall out of that: (1) the load-bearing trust is on your org admins to only invite people they mean to; (2) a third-party IdP that returns a valid ID token for a user we've never invited still cannot get a platform session.</li>
					<li>SSO sessions have the same lifetime as password logins (24 h access token, refresh via the standard flow).</li>
				</ul>
			</div>

			<div class="mt-4">
				<a href="/docs/legal-tech" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: German legal-tech retention (Legal Team) &rarr;
				</a>
			</div>
		</section>
