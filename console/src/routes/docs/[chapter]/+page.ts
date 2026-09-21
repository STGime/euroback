import { error } from '@sveltejs/kit';
import type { EntryGenerator, PageLoad } from './$types';
import { chapterById, loadChapterComponent, pageChapters, prevNext } from '$lib/docs/registry';

// Tell the prerenderer every chapter URL up front so each one becomes a
// static HTML file at build time.
export const entries: EntryGenerator = () => pageChapters.map((c) => ({ chapter: c.id }));

export const load: PageLoad = async ({ params }) => {
	const chapter = chapterById[params.chapter];
	if (!chapter || chapter.id === 'welcome') {
		error(404, 'Documentation chapter not found');
	}
	const Component = await loadChapterComponent(chapter.id);
	return { chapter, Component, ...prevNext(chapter.id) };
};
