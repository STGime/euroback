import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [tailwindcss(), sveltekit()],
	server: {
		proxy: {
			// Proxy API calls to the Go gateway during local development.
			// Rewrites /api/v1/tenants → /v1/tenants on the gateway.
			'/api': {
				target: 'http://localhost:8080',
				changeOrigin: true,
				rewrite: (path: string) => path.replace(/^\/api/, '')
			}
		}
	}
});
