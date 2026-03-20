<script lang="ts">
	// Redirect to projects or login depending on auth state.
	// For now, show a simple redirect to login.
</script>

<div class="flex min-h-screen items-center justify-center bg-gray-50">
	<div class="text-center">
		<h1 class="text-3xl font-bold text-gray-900">Eurobase</h1>
		<p class="mt-2 text-gray-500">EU-Sovereign Backend-as-a-Service</p>
		<div class="mt-6 flex gap-4 justify-center">
			<a
				href="/login"
				class="rounded-lg bg-eurobase-600 px-6 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors"
			>
				Sign In
			</a>
			<a
				href="/projects"
				class="rounded-lg border border-gray-300 bg-white px-6 py-2.5 text-sm font-semibold text-gray-700 shadow-sm hover:bg-gray-50 transition-colors"
			>
				Dashboard
			</a>
		</div>
	</div>
</div>
