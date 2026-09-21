<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="signup" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">1. Signing Up</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex has heard about Eurobase and navigates to the login page.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase uses email-and-password authentication by default. You can also enable social login (Google, GitHub, LinkedIn, Apple) &mdash; providers are used only to verify identity; all user records stay within EU infrastructure.
				</p>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">1</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Navigate to the login page</p>
						<p class="text-sm text-gray-600">Open your browser and go to the Eurobase console URL. You'll see the sign-in form.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">2</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Toggle to "Sign Up"</p>
						<p class="text-sm text-gray-600">Below the form, click the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Don't have an account? Sign up</code> link to switch to registration mode.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">3</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Enter your email and password</p>
						<p class="text-sm text-gray-600">Choose a strong password (minimum 8 characters). Hit <strong>Sign Up</strong> and you're in.</p>
					</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						Your account is a <strong>platform account</strong> &mdash; it's separate from end-user accounts in your projects. One platform account can manage many projects.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('create-project')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Creating Your First Project &rarr;
				</button>
			</div>
		</section>
