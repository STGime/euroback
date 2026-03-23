<script lang="ts">
	import { api, type ColumnInfo } from '$lib/api.js';

	interface Props {
		open: boolean;
		column: ColumnInfo;
		tableName: string;
		projectId: string;
		onClose: () => void;
		onSaved: () => void;
	}

	let { open, column, tableName, projectId, onClose, onSaved }: Props = $props();

	let name = $state('');
	let type_ = $state('');
	let nullable = $state(false);
	let defaultValue = $state('');
	let dropDefault = $state(false);
	let saving = $state(false);
	let error: string | null = $state(null);

	const pgTypes = [
		'text', 'integer', 'bigint', 'smallint', 'boolean', 'uuid',
		'timestamp', 'timestamptz', 'jsonb', 'json', 'real', 'numeric',
		'date', 'time', 'bytea', 'serial', 'bigserial',
		'double precision', 'character varying', 'varchar'
	];

	// Reset form when modal opens.
	$effect(() => {
		if (open && column) {
			name = column.name;
			type_ = column.data_type;
			nullable = column.is_nullable;
			defaultValue = column.default_value ?? '';
			dropDefault = false;
			error = null;
		}
	});

	let hasChanges = $derived(
		name !== column?.name ||
		type_ !== column?.data_type ||
		nullable !== column?.is_nullable ||
		dropDefault ||
		(!dropDefault && defaultValue !== (column?.default_value ?? ''))
	);

	let sqlPreview = $derived(() => {
		if (!column) return '';
		const parts: string[] = [];
		if (type_ !== column.data_type) {
			parts.push(`ALTER COLUMN "${column.name}" TYPE ${type_.toUpperCase()}`);
		}
		if (nullable !== column.is_nullable) {
			parts.push(`ALTER COLUMN "${column.name}" ${nullable ? 'DROP NOT NULL' : 'SET NOT NULL'}`);
		}
		if (dropDefault) {
			parts.push(`ALTER COLUMN "${column.name}" DROP DEFAULT`);
		} else if (defaultValue !== (column.default_value ?? '')) {
			parts.push(`ALTER COLUMN "${column.name}" SET DEFAULT ${defaultValue || "''"}`);
		}
		if (name !== column.name) {
			parts.push(`RENAME COLUMN "${column.name}" TO "${name}"`);
		}
		if (parts.length === 0) return '';
		return parts.map(p => `ALTER TABLE "${tableName}" ${p};`).join('\n');
	});

	async function handleSave() {
		if (!hasChanges || !column) return;
		saving = true;
		error = null;
		try {
			const changes: Record<string, any> = {};
			if (type_ !== column.data_type) changes.new_type = type_;
			if (nullable !== column.is_nullable) changes.nullable = nullable;
			if (dropDefault) {
				changes.drop_default = true;
			} else if (defaultValue !== (column.default_value ?? '')) {
				changes.default_value = defaultValue;
			}
			if (name !== column.name) changes.new_name = name;

			await api.alterColumn(projectId, tableName, column.name, changes);
			onSaved();
			onClose();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			error = jsonMatch ? jsonMatch[1] : raw;
		} finally {
			saving = false;
		}
	}
</script>

{#if open && column}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={onClose}
			tabindex="-1"
			aria-label="Close modal"
		></button>
		<div class="relative z-10 w-full max-w-lg max-h-[80vh] overflow-y-auto rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">
					Edit Column — <span class="font-mono text-eurobase-600">{column.name}</span>
				</h2>
				<button
					type="button"
					class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
					onclick={onClose}
					aria-label="Close"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<div class="px-6 py-5 space-y-4">
				{#if error}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{error}</p>
					</div>
				{/if}

				<!-- Name -->
				<div>
					<label class="block text-sm font-medium text-gray-700 mb-1" for="col-name">Name</label>
					<input
						id="col-name"
						type="text"
						bind:value={name}
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none"
					/>
				</div>

				<!-- Type -->
				<div>
					<label class="block text-sm font-medium text-gray-700 mb-1" for="col-type">Type</label>
					<select
						id="col-type"
						bind:value={type_}
						class="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:outline-none cursor-pointer"
					>
						{#each pgTypes as t}
							<option value={t}>{t}</option>
						{/each}
					</select>
				</div>

				<!-- Nullable -->
				<div class="flex items-center gap-3">
					<input
						id="col-nullable"
						type="checkbox"
						bind:checked={nullable}
						class="h-4 w-4 rounded border-gray-300 text-eurobase-600 focus:ring-eurobase-500 cursor-pointer"
					/>
					<label for="col-nullable" class="text-sm text-gray-700 cursor-pointer">Nullable</label>
				</div>

				<!-- Default value -->
				<div>
					<div class="flex items-center justify-between mb-1">
						<label class="block text-sm font-medium text-gray-700" for="col-default">Default value</label>
						{#if column.default_value}
							<label class="flex items-center gap-1.5 text-xs text-gray-500 cursor-pointer">
								<input type="checkbox" bind:checked={dropDefault} class="h-3.5 w-3.5 rounded border-gray-300 text-red-500 cursor-pointer" />
								Drop default
							</label>
						{/if}
					</div>
					<input
						id="col-default"
						type="text"
						bind:value={defaultValue}
						disabled={dropDefault}
						placeholder="e.g. now(), 0, 'hello'"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none disabled:bg-gray-50 disabled:text-gray-400"
					/>
				</div>

				<!-- SQL Preview -->
				{#if sqlPreview()}
					<div>
						<div class="text-xs font-medium text-gray-500 mb-1">SQL Preview</div>
						<pre class="rounded-lg bg-gray-900 text-green-400 text-xs p-3 overflow-x-auto font-mono">{sqlPreview()}</pre>
					</div>
				{/if}
			</div>

			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={onClose}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
					disabled={!hasChanges || saving}
					onclick={handleSave}
				>
					{saving ? 'Saving...' : 'Save Changes'}
				</button>
			</div>
		</div>
	</div>
{/if}
