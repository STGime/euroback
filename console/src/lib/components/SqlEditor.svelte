<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { EditorView, keymap, placeholder as cmPlaceholder } from '@codemirror/view';
	import { EditorState } from '@codemirror/state';
	import { sql, PostgreSQL } from '@codemirror/lang-sql';
	import { oneDark } from '@codemirror/theme-one-dark';
	import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
	import { bracketMatching } from '@codemirror/language';
	import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
	import { highlightSelectionMatches } from '@codemirror/search';

	let {
		value = '',
		onchange,
		onexecute,
		placeholder = 'Write your SQL query here...'
	}: {
		value: string;
		onchange?: (value: string) => void;
		onexecute?: () => void;
		placeholder?: string;
	} = $props();

	let container: HTMLDivElement;
	let view: EditorView;

	onMount(() => {
		const executeKeymap = keymap.of([
			{
				key: 'Mod-Enter',
				run: () => {
					onexecute?.();
					return true;
				}
			}
		]);

		const updateListener = EditorView.updateListener.of((update) => {
			if (update.docChanged) {
				const newVal = update.state.doc.toString();
				onchange?.(newVal);
			}
		});

		const state = EditorState.create({
			doc: value,
			extensions: [
				executeKeymap,
				history(),
				keymap.of([...defaultKeymap, ...historyKeymap, ...closeBracketsKeymap]),
				bracketMatching(),
				closeBrackets(),
				highlightSelectionMatches(),
				sql({ dialect: PostgreSQL }),
				oneDark,
				cmPlaceholder(placeholder),
				updateListener,
				EditorView.lineWrapping,
				EditorView.theme({
					'&': { fontSize: '13px', height: '100%' },
					'.cm-scroller': { overflow: 'auto', fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, monospace' },
					'.cm-content': { padding: '12px 0' },
					'.cm-gutters': { borderRight: 'none' }
				})
			]
		});

		view = new EditorView({ state, parent: container });
	});

	// Sync external value changes into the editor.
	$effect(() => {
		if (view && value !== view.state.doc.toString()) {
			view.dispatch({
				changes: { from: 0, to: view.state.doc.length, insert: value }
			});
		}
	});

	onDestroy(() => {
		view?.destroy();
	});

	export function focus() {
		view?.focus();
	}
</script>

<div bind:this={container} class="h-full w-full overflow-hidden rounded-lg border border-gray-700"></div>
