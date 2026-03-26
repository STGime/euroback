<script lang="ts">
	import { onMount } from 'svelte';
	import ELK from 'elkjs/lib/elk.bundled.js';
	import type { TableSchema } from '$lib/api.js';

	let { tables: rawTables = [] }: { tables: TableSchema[] } = $props();

	const HIDDEN_TABLES = new Set(['users', 'refresh_tokens', 'storage_objects', 'email_tokens']);
	let tables = $derived(rawTables.filter(t => !HIDDEN_TABLES.has(t.name)));

	// Layout state
	let layoutNodes: Map<string, { x: number; y: number; width: number; height: number }> = $state(new Map());
	let edges: { id: string; sourceTable: string; sourceCol: string; targetTable: string; targetCol: string }[] = $state([]);
	let ready = $state(false);

	// Pan/zoom state
	let viewBox = $state({ x: 0, y: 0, width: 1200, height: 800 });
	let scale = $state(1);
	let isPanning = $state(false);
	let panStart = { x: 0, y: 0, vx: 0, vy: 0 };
	let svgEl: SVGSVGElement | undefined = $state(undefined);

	// Selection
	let selectedTable: string | null = $state(null);

	const ROW_HEIGHT = 24;
	const HEADER_HEIGHT = 32;
	const TABLE_PADDING = 8;
	const COL_WIDTH = 280;

	function shortType(type: string): string {
		if (type === 'timestamp with time zone') return 'timestamptz';
		if (type === 'timestamp without time zone') return 'timestamp';
		if (type === 'character varying') return 'varchar';
		if (type === 'double precision') return 'float8';
		return type;
	}

	function tableHeight(cols: number): number {
		return HEADER_HEIGHT + cols * ROW_HEIGHT + TABLE_PADDING;
	}

	onMount(async () => {
		const elk = new ELK();

		// Build edges from FK relationships (exclude edges to/from hidden tables)
		const visibleNames = new Set(tables.map(t => t.name));
		const edgeList: typeof edges = [];
		for (const table of tables) {
			for (const col of table.columns) {
				if (col.foreign_key && visibleNames.has(col.foreign_key.referenced_table)) {
					edgeList.push({
						id: `${table.name}.${col.name}->${col.foreign_key.referenced_table}.${col.foreign_key.referenced_column}`,
						sourceTable: table.name,
						sourceCol: col.name,
						targetTable: col.foreign_key.referenced_table,
						targetCol: col.foreign_key.referenced_column
					});
				}
			}
		}
		edges = edgeList;

		// Build ELK graph
		const graph = {
			id: 'root',
			layoutOptions: {
				'elk.algorithm': 'layered',
				'elk.direction': 'RIGHT',
				'elk.spacing.nodeNode': '80',
				'elk.layered.spacing.nodeNodeBetweenLayers': '120'
			},
			children: tables.map(t => ({
				id: t.name,
				width: COL_WIDTH,
				height: tableHeight(t.columns.length)
			})),
			edges: edgeList.map((e, i) => ({
				id: `e${i}`,
				sources: [e.sourceTable],
				targets: [e.targetTable]
			}))
		};

		try {
			const layout = await elk.layout(graph);
			const nodes = new Map<string, { x: number; y: number; width: number; height: number }>();
			for (const child of layout.children ?? []) {
				nodes.set(child.id, {
					x: child.x ?? 0,
					y: child.y ?? 0,
					width: child.width ?? COL_WIDTH,
					height: child.height ?? 100
				});
			}
			layoutNodes = nodes;

			// Calculate viewBox to fit all nodes
			let maxX = 0, maxY = 0;
			for (const n of nodes.values()) {
				maxX = Math.max(maxX, n.x + n.width + 80);
				maxY = Math.max(maxY, n.y + n.height + 80);
			}
			viewBox = { x: -40, y: -40, width: Math.max(maxX, 600), height: Math.max(maxY, 400) };
			ready = true;
		} catch {
			// Fallback: simple grid layout
			const nodes = new Map<string, { x: number; y: number; width: number; height: number }>();
			const cols = Math.ceil(Math.sqrt(tables.length));
			tables.forEach((t, i) => {
				const col = i % cols;
				const row = Math.floor(i / cols);
				nodes.set(t.name, {
					x: col * (COL_WIDTH + 100),
					y: row * 300,
					width: COL_WIDTH,
					height: tableHeight(t.columns.length)
				});
			});
			layoutNodes = nodes;
			ready = true;
		}
	});

	function getColY(table: TableSchema, colName: string): number {
		const node = layoutNodes.get(table.name);
		if (!node) return 0;
		const idx = table.columns.findIndex(c => c.name === colName);
		return node.y + HEADER_HEIGHT + idx * ROW_HEIGHT + ROW_HEIGHT / 2;
	}

	function handlePointerDown(e: PointerEvent) {
		if ((e.target as Element)?.closest?.('.table-node')) return;
		isPanning = true;
		panStart = { x: e.clientX, y: e.clientY, vx: viewBox.x, vy: viewBox.y };
		(e.currentTarget as Element)?.setPointerCapture?.(e.pointerId);
	}

	function handlePointerMove(e: PointerEvent) {
		if (!isPanning) return;
		const dx = (e.clientX - panStart.x) / scale;
		const dy = (e.clientY - panStart.y) / scale;
		viewBox = { ...viewBox, x: panStart.vx - dx, y: panStart.vy - dy };
	}

	function handlePointerUp() {
		isPanning = false;
	}

	function handleWheel(e: WheelEvent) {
		e.preventDefault();
		const factor = e.deltaY > 0 ? 0.9 : 1.1;
		const newScale = Math.max(0.25, Math.min(3.0, scale * factor));
		const ratio = scale / newScale;
		scale = newScale;

		if (!svgEl) return;
		const rect = svgEl.getBoundingClientRect();
		const mx = e.clientX - rect.left;
		const my = e.clientY - rect.top;

		const svgX = viewBox.x + (mx / rect.width) * viewBox.width;
		const svgY = viewBox.y + (my / rect.height) * viewBox.height;

		viewBox = {
			x: svgX - (svgX - viewBox.x) * ratio,
			y: svgY - (svgY - viewBox.y) * ratio,
			width: viewBox.width * ratio,
			height: viewBox.height * ratio
		};
	}

	function fitToView() {
		if (layoutNodes.size === 0 || !svgEl) return;
		let minX = Infinity, minY = Infinity, maxX = 0, maxY = 0;
		for (const n of layoutNodes.values()) {
			minX = Math.min(minX, n.x);
			minY = Math.min(minY, n.y);
			maxX = Math.max(maxX, n.x + n.width);
			maxY = Math.max(maxY, n.y + n.height);
		}
		const padding = 60;
		viewBox = {
			x: minX - padding,
			y: minY - padding,
			width: maxX - minX + padding * 2,
			height: maxY - minY + padding * 2
		};
		scale = 1;
	}

	function zoomIn() {
		const factor = 1.3;
		const cx = viewBox.x + viewBox.width / 2;
		const cy = viewBox.y + viewBox.height / 2;
		const newW = viewBox.width / factor;
		const newH = viewBox.height / factor;
		viewBox = { x: cx - newW / 2, y: cy - newH / 2, width: newW, height: newH };
		scale = Math.min(3.0, scale * factor);
	}

	function zoomOut() {
		const factor = 1.3;
		const cx = viewBox.x + viewBox.width / 2;
		const cy = viewBox.y + viewBox.height / 2;
		const newW = viewBox.width * factor;
		const newH = viewBox.height * factor;
		viewBox = { x: cx - newW / 2, y: cy - newH / 2, width: newW, height: newH };
		scale = Math.max(0.25, scale / factor);
	}

	function isConnected(tableName: string): boolean {
		if (!selectedTable) return true;
		if (tableName === selectedTable) return true;
		return edges.some(e =>
			(e.sourceTable === selectedTable && e.targetTable === tableName) ||
			(e.targetTable === selectedTable && e.sourceTable === tableName)
		);
	}

	function isEdgeConnected(edge: typeof edges[0]): boolean {
		if (!selectedTable) return true;
		return edge.sourceTable === selectedTable || edge.targetTable === selectedTable;
	}
</script>

{#if !ready}
	<div class="flex items-center justify-center h-full text-sm text-gray-400">
		<svg class="h-5 w-5 animate-spin mr-2" viewBox="0 0 24 24" fill="none">
			<circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
			<path d="M4 12a8 8 0 018-8" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="opacity-75" />
		</svg>
		Laying out schema...
	</div>
{:else if tables.length === 0}
	<div class="flex items-center justify-center h-full text-sm text-gray-400">
		No tables to display
	</div>
{:else}
	<div class="relative w-full h-full">
	<!-- Zoom controls -->
	<div class="absolute top-3 right-3 z-10 flex flex-col gap-1.5">
		<button
			type="button"
			class="cursor-pointer flex items-center justify-center h-8 w-8 rounded-lg border border-gray-300 bg-white text-gray-600 shadow-sm hover:bg-gray-50 transition-colors"
			onclick={zoomIn}
			title="Zoom in"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
			</svg>
		</button>
		<button
			type="button"
			class="cursor-pointer flex items-center justify-center h-8 w-8 rounded-lg border border-gray-300 bg-white text-gray-600 shadow-sm hover:bg-gray-50 transition-colors"
			onclick={zoomOut}
			title="Zoom out"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 12h-15" />
			</svg>
		</button>
		<button
			type="button"
			class="cursor-pointer flex items-center justify-center h-8 w-8 rounded-lg border border-gray-300 bg-white text-gray-600 shadow-sm hover:bg-gray-50 transition-colors"
			onclick={fitToView}
			title="Fit to view"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M3.75 3.75v4.5m0-4.5h4.5m-4.5 0L9 9M3.75 20.25v-4.5m0 4.5h4.5m-4.5 0L9 15M20.25 3.75h-4.5m4.5 0v4.5m0-4.5L15 9m5.25 11.25h-4.5m4.5 0v-4.5m0 4.5L15 15" />
			</svg>
		</button>
	</div>

	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<svg
		bind:this={svgEl}
		class="w-full h-full cursor-grab {isPanning ? 'cursor-grabbing' : ''}"
		viewBox="{viewBox.x} {viewBox.y} {viewBox.width} {viewBox.height}"
		onpointerdown={handlePointerDown}
		onpointermove={handlePointerMove}
		onpointerup={handlePointerUp}
		onwheel={handleWheel}
	>
		<defs>
			<marker id="arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
				<polygon points="0 0, 10 3.5, 0 7" fill="#6366f1" />
			</marker>
		</defs>

		<!-- Edges (FK relationships) -->
		{#each edges as edge}
			{@const srcTable = tables.find(t => t.name === edge.sourceTable)}
			{@const tgtTable = tables.find(t => t.name === edge.targetTable)}
			{@const srcNode = layoutNodes.get(edge.sourceTable)}
			{@const tgtNode = layoutNodes.get(edge.targetTable)}
			{#if srcTable && tgtTable && srcNode && tgtNode}
				{@const x1 = srcNode.x + srcNode.width}
				{@const y1 = getColY(srcTable, edge.sourceCol)}
				{@const x2 = tgtNode.x}
				{@const y2 = getColY(tgtTable, edge.targetCol)}
				{@const cx1 = x1 + 50}
				{@const cx2 = x2 - 50}
				<path
					d="M {x1} {y1} C {cx1} {y1}, {cx2} {y2}, {x2} {y2}"
					fill="none"
					stroke={isEdgeConnected(edge) ? '#6366f1' : '#d1d5db'}
					stroke-width={isEdgeConnected(edge) ? 2 : 1}
					marker-end="url(#arrowhead)"
					class="transition-colors"
				/>
			{/if}
		{/each}

		<!-- Table nodes -->
		{#each tables as table}
			{@const node = layoutNodes.get(table.name)}
			{#if node}
				<!-- svelte-ignore a11y_click_events_have_key_events -->
				<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
				<g
					class="table-node cursor-pointer"
					transform="translate({node.x}, {node.y})"
					onclick={() => { selectedTable = selectedTable === table.name ? null : table.name; }}
					opacity={isConnected(table.name) ? 1 : 0.3}
				>
					<!-- Shadow -->
					<rect
						x="2" y="2"
						width={node.width} height={node.height}
						rx="8"
						fill="rgba(0,0,0,0.06)"
					/>
					<!-- Body -->
					<rect
						width={node.width} height={node.height}
						rx="8"
						fill="white"
						stroke={selectedTable === table.name ? '#7c3aed' : '#e5e7eb'}
						stroke-width={selectedTable === table.name ? 2 : 1}
					/>
					<!-- Header -->
					<rect
						width={node.width} height={HEADER_HEIGHT}
						rx="8"
						fill="#7c3aed"
					/>
					<rect
						y={HEADER_HEIGHT - 8}
						width={node.width} height="8"
						fill="#7c3aed"
					/>
					<text x="12" y={HEADER_HEIGHT / 2 + 5} font-size="13" font-weight="600" fill="white">{table.name}</text>

					<!-- Columns -->
					{#each table.columns as col, i}
						{@const cy = HEADER_HEIGHT + i * ROW_HEIGHT}
						<line
							x1="0" y1={cy}
							x2={node.width} y2={cy}
							stroke="#f3f4f6"
						/>
						<clipPath id="clip-{table.name}-{col.name}">
							<rect x="10" y={cy} width={node.width / 2 - 12} height={ROW_HEIGHT} />
						</clipPath>
						<text x="12" y={cy + ROW_HEIGHT / 2 + 4} font-size="11" fill="#374151" font-family="monospace" clip-path="url(#clip-{table.name}-{col.name})">
							{col.name}
						</text>
						<text x={node.width - 8} y={cy + ROW_HEIGHT / 2 + 4} font-size="10" fill="#9ca3af" text-anchor="end" font-family="monospace">
							{shortType(col.data_type)}
						</text>
						<!-- Badges -->
						{#if col.is_primary_key}
							<circle cx={node.width / 2 + 4} cy={cy + ROW_HEIGHT / 2} r="3" fill="#f59e0b" />
						{/if}
						{#if col.foreign_key}
							<circle cx={node.width / 2 + 14} cy={cy + ROW_HEIGHT / 2} r="3" fill="#6366f1" />
						{/if}
					{/each}
				</g>
			{/if}
		{/each}
	</svg>
	</div>
{/if}
