// Schedule helpers for the Scheduled Jobs page. They mirror the scheduler
// (internal/cron: validateCronSchedule, shouldRun): five fields, each "*",
// "*/N", a number or a comma list of these, matched against the time in the
// job's timezone once a minute.

const FIELDS = [
	{ name: 'minute', min: 0, max: 59 },
	{ name: 'hour', min: 0, max: 23 },
	{ name: 'day of month', min: 1, max: 31 },
	{ name: 'month', min: 1, max: 12 },
	{ name: 'weekday', min: 0, max: 6 }
] as const;

type Matcher = (v: number) => boolean;

/** The reason a schedule would be refused on save, or null when it's valid. */
export function scheduleError(schedule: string): string | null {
	const r = parse(schedule);
	return typeof r === 'string' ? r : null;
}

function parse(schedule: string): Matcher[] | string {
	const fields = schedule.trim().split(/\s+/);
	if (fields.length !== 5) return 'A schedule has 5 fields: minute hour day month weekday.';
	const matchers: Matcher[] = [];
	for (let i = 0; i < 5; i++) {
		const f = FIELDS[i];
		const parts: Matcher[] = [];
		for (const part of fields[i].split(',')) {
			if (part === '*') {
				parts.push(() => true);
			} else if (part.startsWith('*/')) {
				const step = /^\d+$/.test(part.slice(2)) ? Number(part.slice(2)) : 0;
				if (step < 1) return `${cap(f.name)} "${part}": use */N with N of 1 or more.`;
				parts.push((v) => v % step === 0);
			} else if (/^\d+$/.test(part)) {
				const n = Number(part);
				if (n < f.min || n > f.max) {
					const hint = i === 4 && n === 7 ? ' Use 0 for Sunday.' : '';
					return `${cap(f.name)} ${n} is outside ${f.min}-${f.max}.${hint}`;
				}
				parts.push((v) => v === n);
			} else {
				return `${cap(f.name)} "${part}": use *, */N, a number or a comma list. Ranges like 1-5 and names aren't supported; list the values instead (1,2,3,4,5).`;
			}
		}
		matchers.push((v) => parts.some((p) => p(v)));
	}
	return matchers;
}

function cap(s: string): string {
	return s[0].toUpperCase() + s.slice(1);
}

const WEEKDAYS: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 };

function partsIn(fmt: Intl.DateTimeFormat, t: number) {
	const p: Record<string, string> = {};
	for (const x of fmt.formatToParts(new Date(t))) p[x.type] = x.value;
	return {
		minute: Number(p.minute),
		hour: Number(p.hour) % 24,
		day: Number(p.day),
		month: Number(p.month),
		weekday: WEEKDAYS[p.weekday]
	};
}

const MINUTE = 60_000;
const QUARTER = 15 * MINUTE;
const SEARCH_DAYS = 400;

/**
 * The next `count` times the job would run after `from`, or [] if the
 * schedule is invalid or doesn't run within about a year (e.g. 31 February).
 */
export function nextRuns(schedule: string, timezone: string, count = 3, from = new Date()): Date[] {
	const m = parse(schedule);
	if (typeof m === 'string') return [];
	let fmt: Intl.DateTimeFormat;
	try {
		fmt = new Intl.DateTimeFormat('en-US', {
			timeZone: timezone || 'UTC',
			hourCycle: 'h23',
			year: 'numeric',
			month: 'numeric',
			day: 'numeric',
			hour: 'numeric',
			minute: 'numeric',
			weekday: 'short'
		});
	} catch {
		return [];
	}
	const [minute, hour, day, month, weekday] = m;
	const out: Date[] = [];
	let t = Math.floor(from.getTime() / MINUTE) * MINUTE + MINUTE;
	const end = t + SEARCH_DAYS * 24 * 60 * MINUTE;
	while (t < end && out.length < count) {
		const p = partsIn(fmt, t);
		if (month(p.month) && day(p.day) && weekday(p.weekday) && hour(p.hour)) {
			if (minute(p.minute)) out.push(new Date(t));
			t += MINUTE;
		} else {
			// Every UTC offset is a multiple of 15 minutes, so the local hour
			// can only change on a quarter-hour UTC boundary.
			t = Math.floor(t / QUARTER) * QUARTER + QUARTER;
		}
	}
	return out;
}

/** A run time in the job's timezone, e.g. "Wed 11 Nov, 15:00". */
export function formatInZone(d: Date, timezone: string): string {
	try {
		return d.toLocaleString('en-GB', {
			timeZone: timezone || 'UTC',
			weekday: 'short',
			day: 'numeric',
			month: 'short',
			hour: '2-digit',
			minute: '2-digit',
			hourCycle: 'h23'
		});
	} catch {
		return d.toISOString();
	}
}

/** The viewer's own timezone, e.g. "Europe/Stockholm". */
export function browserTimezone(): string {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
	} catch {
		return 'UTC';
	}
}

/** UTC first, then the viewer's timezone, then every other IANA name. */
export function timezoneOptions(): string[] {
	let all: string[] = [];
	try {
		all = (Intl as unknown as { supportedValuesOf(k: string): string[] }).supportedValuesOf('timeZone');
	} catch {
		/* older browsers: UTC and the viewer's zone only */
	}
	const first = ['UTC', browserTimezone()];
	return [...new Set([...first, ...all])];
}
