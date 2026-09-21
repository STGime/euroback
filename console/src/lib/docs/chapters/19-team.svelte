<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="team" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">19. Team Collaboration</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to give a colleague access to the project without sharing API keys.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The <strong>Members</strong> tab on the Settings page lets you invite team members to your project with role-based access control. Each member gets their own login — no shared credentials needed.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Roles</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase has four roles, ordered from least to most permissions:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Viewer</strong> &mdash; read-only access to data, logs, and compliance reports</li>
					<li><strong>Developer</strong> &mdash; viewer + can edit data, manage schema (create/drop tables, columns), and manage edge functions</li>
					<li><strong>Admin</strong> &mdash; developer + can change project settings, regenerate API keys, manage vault secrets, and invite or remove members</li>
					<li><strong>Owner</strong> &mdash; admin + can delete the project and change other members' roles. Every project has exactly one owner (the creator).</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Inviting members</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Go to <strong>Settings &rarr; Members</strong>, enter an email address, select a role, and click <strong>Send Invite</strong>. The recipient receives an email with an invitation link that expires in 7 days. If they don't have a Eurobase account yet, they'll need to sign up first, then click the link again.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Managing members</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Change role</strong> &mdash; only the project owner can change a member's role using the dropdown in the members table</li>
					<li><strong>Remove</strong> &mdash; only the project owner can remove members. Members cannot remove themselves.</li>
					<li><strong>Resend invitation</strong> &mdash; if an invitation hasn't been accepted, click Resend to generate a fresh token and re-send the email</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">How it works</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Once invited, a member sees the project in their project list alongside any projects they own. All member actions are recorded in the <strong>Compliance &rarr; Audit Log</strong>: invitations sent, accepted, roles changed, and members removed.
				</p>

				<div class="rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-blue-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-blue-800">
						Members share the project's plan and API keys. They can access all data in the project's database and storage based on their role. Individual API keys per member are a future feature.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('cli')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: CLI Tool &rarr;
				</button>
			</div>
		</section>
