<script lang="ts">
	const PG_TYPES = [
		'text',
		'integer',
		'bigint',
		'boolean',
		'uuid',
		'timestamp',
		'timestamptz',
		'jsonb',
		'real',
		'numeric'
	];

	interface ColumnDef {
		name: string;
		type: string;
		nullable: boolean;
		defaultValue: string;
		isPrimaryKey: boolean;
	}

	let {
		open = false,
		onClose,
		onCreate
	}: {
		open: boolean;
		onClose: () => void;
		onCreate?: (sql: string) => void;
	} = $props();

	let tableName = $state('');
	let columns: ColumnDef[] = $state([
		{
			name: 'id',
			type: 'uuid',
			nullable: false,
			defaultValue: 'uuid_generate_v4()',
			isPrimaryKey: true
		},
		{
			name: 'created_at',
			type: 'timestamptz',
			nullable: false,
			defaultValue: 'now()',
			isPrimaryKey: false
		}
	]);

	let generatedSql = $derived(generateSql());
	let showSql = $state(false);

	function addColumn() {
		columns = [
			...columns,
			{
				name: '',
				type: 'text',
				nullable: true,
				defaultValue: '',
				isPrimaryKey: false
			}
		];
	}

	function removeColumn(index: number) {
		columns = columns.filter((_, i) => i !== index);
	}

	function togglePrimaryKey(index: number) {
		columns = columns.map((col, i) => ({
			...col,
			isPrimaryKey: i === index ? !col.isPrimaryKey : col.isPrimaryKey
		}));
	}

	function generateSql(): string {
		if (!tableName.trim()) return '-- Enter a table name to generate SQL';

		const safeName = tableName.trim().replace(/[^a-zA-Z0-9_]/g, '_');
		const colDefs = columns
			.filter((c) => c.name.trim())
			.map((col) => {
				const parts = [`  "${col.name.trim()}"`, col.type];
				if (!col.nullable) parts.push('NOT NULL');
				if (col.defaultValue.trim()) parts.push(`DEFAULT ${col.defaultValue.trim()}`);
				return parts.join(' ');
			});

		const pks = columns.filter((c) => c.isPrimaryKey && c.name.trim()).map((c) => `"${c.name.trim()}"`);
		if (pks.length > 0) {
			colDefs.push(`  PRIMARY KEY (${pks.join(', ')})`);
		}

		return `CREATE TABLE "${safeName}" (\n${colDefs.join(',\n')}\n);`;
	}

	function handleCreate() {
		if (onCreate) {
			onCreate(generatedSql);
		}
		showSql = true;
	}

	function handleClose() {
		tableName = '';
		columns = [
			{
				name: 'id',
				type: 'uuid',
				nullable: false,
				defaultValue: 'uuid_generate_v4()',
				isPrimaryKey: true
			},
			{
				name: 'created_at',
				type: 'timestamptz',
				nullable: false,
				defaultValue: 'now()',
				isPrimaryKey: false
			}
		];
		showSql = false;
		onClose();
	}

	let isValid = $derived(
		tableName.trim().length > 0 && columns.some((c) => c.name.trim().length > 0)
	);
</script>

{#if open}
	<!-- Backdrop -->
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<!-- Overlay -->
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={handleClose}
			tabindex="-1"
			aria-label="Close modal"
		></button>

		<!-- Modal -->
		<div class="relative z-10 w-full max-w-3xl max-h-[85vh] overflow-y-auto rounded-xl bg-white shadow-2xl">
			<!-- Header -->
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Create New Table</h2>
				<button
					type="button"
					class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
					onclick={handleClose}
					aria-label="Close"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<!-- Body -->
			<div class="px-6 py-5 space-y-5">
				<!-- Table name -->
				<div>
					<label for="table-name" class="block text-sm font-medium text-gray-700 mb-1.5">
						Table Name
					</label>
					<input
						id="table-name"
						type="text"
						bind:value={tableName}
						placeholder="e.g. users, orders, products"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-400 focus:border-eurobase-500 focus:ring-2 focus:ring-eurobase-500/20 focus:outline-none"
					/>
				</div>

				<!-- Column definitions -->
				<div>
					<div class="flex items-center justify-between mb-3">
						<span class="text-sm font-medium text-gray-700">Columns</span>
						<button
							type="button"
							class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 transition-colors"
							onclick={addColumn}
						>
							<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
							</svg>
							Add Column
						</button>
					</div>

					<!-- Column header labels -->
					<div class="grid grid-cols-[1fr_120px_60px_1fr_60px_32px] gap-2 mb-2 px-1">
						<span class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">Name</span>
						<span class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">Type</span>
						<span class="text-[10px] font-semibold uppercase tracking-wider text-gray-400 text-center">Null</span>
						<span class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">Default</span>
						<span class="text-[10px] font-semibold uppercase tracking-wider text-gray-400 text-center">PK</span>
						<span></span>
					</div>

					<div class="space-y-2">
						{#each columns as col, i}
							<div class="grid grid-cols-[1fr_120px_60px_1fr_60px_32px] gap-2 items-center">
								<input
									type="text"
									bind:value={col.name}
									placeholder="column_name"
									class="rounded-lg border border-gray-300 px-2.5 py-1.5 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none"
								/>
								<select
									bind:value={col.type}
									class="rounded-lg border border-gray-300 px-2 py-1.5 text-sm text-gray-900 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none cursor-pointer"
								>
									{#each PG_TYPES as t}
										<option value={t}>{t}</option>
									{/each}
								</select>
								<div class="flex justify-center">
									<input
										type="checkbox"
										bind:checked={col.nullable}
										class="h-4 w-4 rounded border-gray-300 text-eurobase-600 focus:ring-eurobase-500 cursor-pointer"
									/>
								</div>
								<input
									type="text"
									bind:value={col.defaultValue}
									placeholder="optional"
									class="rounded-lg border border-gray-300 px-2.5 py-1.5 text-sm font-mono text-gray-600 placeholder-gray-300 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none"
								/>
								<div class="flex justify-center">
									<button
										type="button"
										class="cursor-pointer h-6 w-6 flex items-center justify-center rounded transition-colors
											{col.isPrimaryKey
												? 'bg-eurobase-100 text-eurobase-700'
												: 'bg-gray-100 text-gray-400 hover:bg-gray-200'}"
										onclick={() => togglePrimaryKey(i)}
										title="Toggle primary key"
									>
										<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z" />
										</svg>
									</button>
								</div>
								<button
									type="button"
									class="cursor-pointer flex h-7 w-7 items-center justify-center rounded-lg text-gray-300 hover:bg-red-50 hover:text-red-500 transition-colors"
									onclick={() => removeColumn(i)}
									title="Remove column"
								>
									<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
										<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
									</svg>
								</button>
							</div>
						{/each}
					</div>
				</div>

				<!-- Generated SQL preview -->
				{#if showSql || tableName.trim()}
					<div>
						<span class="block text-sm font-medium text-gray-700 mb-1.5">Generated SQL</span>
						<pre class="rounded-lg bg-gray-900 p-4 text-sm font-mono text-green-400 overflow-x-auto">{generatedSql}</pre>
					</div>
				{/if}
			</div>

			<!-- Footer -->
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={handleClose}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
					disabled={!isValid}
					onclick={handleCreate}
				>
					Create Table
				</button>
			</div>
		</div>
	</div>
{/if}
