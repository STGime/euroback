/**
 * Utility functions for the Eurobase console.
 */

/**
 * Format a byte count into a human-readable string.
 * e.g. 1536 -> "1.5 KB", 1048576 -> "1.0 MB"
 */
export function formatBytes(bytes: number): string {
	if (bytes === 0) return '0 B';
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	const k = 1024;
	const i = Math.floor(Math.log(bytes) / Math.log(k));
	const value = bytes / Math.pow(k, i);
	return `${value < 10 && i > 0 ? value.toFixed(1) : Math.round(value)} ${units[i]}`;
}

/**
 * Format an ISO date string into a relative time description.
 * e.g. "2 hours ago", "yesterday", "3 days ago"
 */
export function formatRelativeTime(date: string): string {
	const now = Date.now();
	const then = new Date(date).getTime();
	const diffMs = now - then;
	const diffSec = Math.floor(diffMs / 1000);
	const diffMin = Math.floor(diffSec / 60);
	const diffHr = Math.floor(diffMin / 60);
	const diffDay = Math.floor(diffHr / 24);

	if (diffSec < 60) return 'just now';
	if (diffMin < 60) return `${diffMin} minute${diffMin === 1 ? '' : 's'} ago`;
	if (diffHr < 24) return `${diffHr} hour${diffHr === 1 ? '' : 's'} ago`;
	if (diffDay === 1) return 'yesterday';
	if (diffDay < 30) return `${diffDay} days ago`;
	if (diffDay < 365) {
		const months = Math.floor(diffDay / 30);
		return `${months} month${months === 1 ? '' : 's'} ago`;
	}
	const years = Math.floor(diffDay / 365);
	return `${years} year${years === 1 ? '' : 's'} ago`;
}

/**
 * Return an icon identifier based on MIME content type.
 */
export function getFileIcon(contentType: string): 'image' | 'pdf' | 'text' | 'file' {
	if (contentType.startsWith('image/')) return 'image';
	if (contentType === 'application/pdf') return 'pdf';
	if (contentType.startsWith('text/')) return 'text';
	return 'file';
}

/**
 * Extract the file extension from an object key / filename.
 */
export function getFileExtension(key: string): string {
	const dot = key.lastIndexOf('.');
	if (dot === -1) return '';
	return key.slice(dot + 1).toLowerCase();
}

const mimeMap: Record<string, string> = {
	jpg: 'image/jpeg', jpeg: 'image/jpeg', png: 'image/png', gif: 'image/gif',
	webp: 'image/webp', svg: 'image/svg+xml', ico: 'image/x-icon', bmp: 'image/bmp',
	pdf: 'application/pdf',
	json: 'application/json',
	js: 'application/javascript', mjs: 'application/javascript',
	xml: 'application/xml',
	html: 'text/html', htm: 'text/html',
	css: 'text/css',
	txt: 'text/plain', md: 'text/plain', csv: 'text/csv',
	ts: 'text/plain', tsx: 'text/plain', jsx: 'text/plain',
	yaml: 'text/plain', yml: 'text/plain', toml: 'text/plain',
	sh: 'text/plain', go: 'text/plain', py: 'text/plain', rs: 'text/plain',
	mp4: 'video/mp4', webm: 'video/webm',
	mp3: 'audio/mpeg', wav: 'audio/wav', ogg: 'audio/ogg',
	zip: 'application/zip', gz: 'application/gzip', tar: 'application/x-tar',
};

/**
 * Infer MIME type from file extension when the API doesn't provide one.
 */
export function inferContentType(key: string, apiContentType?: string): string {
	if (apiContentType) return apiContentType;
	const ext = getFileExtension(key);
	return mimeMap[ext] || 'application/octet-stream';
}
