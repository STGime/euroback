<!--
  The root route redirects to /login?signup=1 (see +page.ts). This
  component only renders in the extremely unlikely case the redirect
  didn't fire — e.g. a preview build. Keep the fallback minimal.
-->
<div class="flex min-h-screen items-center justify-center bg-gray-50">
	<div class="text-center">
		<h1 class="text-lg font-semibold text-gray-900">Eurobase</h1>
		<p class="mt-2 text-sm text-gray-500">
			Redirecting to <a href="/login?signup=1" class="text-eurobase-600 hover:text-eurobase-700 underline">sign up</a>…
		</p>
	</div>
</div>
