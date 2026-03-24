<script lang="ts">
	import { onMount } from 'svelte';
	import type { TableSchema } from '$lib/api.js';

	let { tables = [] }: { tables: TableSchema[] } = $props();

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
	const COL_WIDTH = 220;

	function tableHeight(cols: number): number {
		return HEADER_HEIGHT + cols * ROW_HEIGHT + TABLE_PADDING;
	}

	onMount(async () => {
		const ELK = (await import('elkjs')).default;
		const elk = new ELK();

		// Build edges from FK relationships
		const edgeList: typeof edges = [];
		for (const table of tables) {
			for (const col of table.columns) {
				if (col.foreign_key) {
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
						<text x="12" y={cy + ROW_HEIGHT / 2 + 4} font-size="11" fill="#374151" font-family="monospace">
							{col.name}
						</text>
						<text x={node.width - 8} y={cy + ROW_HEIGHT / 2 + 4} font-size="10" fill="#9ca3af" text-anchor="end" font-family="monospace">
							{col.data_type}
						</text>
						<!-- Badges -->
						{#if col.is_primary_key}
							<circle cx={node.width - 60} cy={cy + ROW_HEIGHT / 2} r="3" fill="#f59e0b" />
						{/if}
						{#if col.foreign_key}
							<circle cx={node.width - 70} cy={cy + ROW_HEIGHT / 2} r="3" fill="#6366f1" />
						{/if}
					{/each}
				</g>
			{/if}
		{/each}
	</svg>
{/if}
