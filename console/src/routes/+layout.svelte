<script lang="ts">
	import '../app.css';

	let { children } = $props();
</script>

<svelte:head>
	<title>Eurobase Console</title>
	<meta name="description" content="EU-Sovereign Backend-as-a-Service" />
	<link rel="preconnect" href="https://fonts.bunny.net" />
	<link href="https://fonts.bunny.net/css?family=inter:400,500,600,700" rel="stylesheet" />
</svelte:head>

{@render children()}
