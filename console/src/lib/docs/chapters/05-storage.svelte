<script lang="ts">
	import { goto } from '$app/navigation';
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
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="storage" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">5. File Storage</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to store legal documents that clients upload.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Each project gets a dedicated Scaleway Object Storage bucket in your chosen EU region. The Storage page in the console gives you a file manager to upload, organize, and preview files.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Console features</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Drag-and-drop upload</strong> &mdash; drop files or click to browse</li>
					<li><strong>Folders</strong> &mdash; organize files with a folder structure and navigate with breadcrumbs</li>
					<li><strong>Preview</strong> &mdash; images, PDFs, and text files render inline</li>
					<li><strong>Signed URLs</strong> &mdash; generate time-limited download links for any file</li>
					<li><strong>List &amp; grid view</strong> &mdash; toggle between compact list and visual grid</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Upload via SDK</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("// Upload a file\nconst file = document.getElementById('fileInput').files[0]\nconst { key, error } = await eb.storage\n  .upload('contracts/nda-acme.pdf', file)\n\n// Get a signed download URL (1 hour expiry)\nconst { url } = await eb.storage\n  .createSignedUrl('contracts/nda-acme.pdf', 'download', { expiresIn: 3600 })\n\n// List files in a folder\nconst { objects } = await eb.storage\n  .list({ prefix: 'contracts/' })\n\n// Download a file\nconst blob = await eb.storage.download('contracts/nda-acme.pdf')\n\n// Delete a file\nawait eb.storage.remove('contracts/nda-acme.pdf')", 'sdk-storage')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-storage' ? 'Copied!' : 'Copy'}
					</button>
					<pre>// Upload a file
const file = document.getElementById('fileInput').files[0]
const {'{'} key, error {'}'} = await eb.storage
  .upload('contracts/nda-acme.pdf', file)

// Get a signed download URL (1 hour expiry)
const {'{'} url {'}'} = await eb.storage
  .createSignedUrl('contracts/nda-acme.pdf', 'download', {'{'} expiresIn: 3600 {'}'})

// List files in a folder
const {'{'} objects {'}'} = await eb.storage
  .list({'{'} prefix: 'contracts/' {'}'})

// Download a file
const blob = await eb.storage.download('contracts/nda-acme.pdf')

// Delete a file
await eb.storage.remove('contracts/nda-acme.pdf')</pre>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						All files are stored in Scaleway Object Storage in France. No data ever touches US-based cloud providers.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('auth')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Authentication Setup &rarr;
				</button>
			</div>
		</section>
