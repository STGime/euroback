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

/** Digits only (no sign), as the server's check; long numbers are out of range. */
function num(s: string): number | null {
	if (!/^\d+$/.test(s)) return null;
	return s.length > 6 ? Number.MAX_SAFE_INTEGER : Number(s);
}

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
				const step = num(part.slice(2)) ?? 0;
				if (step < 1 || step > 1000) return `${cap(f.name)} "${part}": use */N with N from 1 to 1000.`;
				parts.push((v) => v % step === 0);
			} else if (num(part) !== null) {
				const n = num(part)!;
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
		year: Number(p.year),
		minute: Number(p.minute),
		hour: Number(p.hour) % 24,
		day: Number(p.day),
		month: Number(p.month),
		weekday: WEEKDAYS[p.weekday]
	};
}

const MINUTE = 60_000;
const QUARTER = 15 * MINUTE;
const HOUR = 60 * MINUTE;
// 28 years: the calendar repeats, so a schedule that matches no date in
// that window (e.g. 29 February on a Monday that never comes) never runs.
const SEARCH_YEARS = 28;

function daysInMonth(year: number, month: number): number {
	return new Date(Date.UTC(year, month, 0)).getUTCDate();
}

/** True if some allowed month has some allowed day (31 Feb alone never runs). */
function dateCanExist(day: Matcher, month: Matcher): boolean {
	for (let mo = 1; mo <= 12; mo++) {
		if (!month(mo)) continue;
		const max = mo === 2 ? 29 : daysInMonth(2001, mo);
		for (let d = 1; d <= max; d++) if (day(d)) return true;
	}
	return false;
}

/**
 * The next `count` times the job would run after `from`, or [] if the
 * schedule is invalid or never runs (e.g. 31 February).
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
	if (!dateCanExist(day, month)) return [];
	const out: Date[] = [];
	let t = Math.floor(from.getTime() / MINUTE) * MINUTE + MINUTE;
	const end = t + SEARCH_YEARS * 366 * 24 * HOUR;
	while (t < end && out.length < count) {
		const p = partsIn(fmt, t);
		const toMidnight = ((23 - p.hour) * 60 + (60 - p.minute)) * MINUTE;
		let skip: number;
		if (!month(p.month)) {
			// Skip to the end of the month.
			skip = (daysInMonth(p.year, p.month) - p.day) * 24 * HOUR + toMidnight;
		} else if (!day(p.day) || !weekday(p.weekday)) {
			skip = toMidnight; // the rest of the day
		} else if (!hour(p.hour)) {
			skip = 0;
		} else {
			if (minute(p.minute)) out.push(new Date(t));
			t += MINUTE;
			continue;
		}
		// Land an hour early (a daylight saving change moves local midnight
		// by up to an hour), then step in quarter hours: every UTC offset is
		// a multiple of 15 minutes, so the local hour only changes there.
		t = Math.floor((t + Math.max(0, skip - HOUR)) / QUARTER) * QUARTER + QUARTER;
	}
	return out;
}

/** A run time in the job's timezone, e.g. "Wed 11 Nov, 15:00 CET". */
export function formatInZone(d: Date, timezone: string): string {
	try {
		return d.toLocaleString('en-GB', {
			timeZone: timezone || 'UTC',
			weekday: 'short',
			day: 'numeric',
			month: 'short',
			hour: '2-digit',
			minute: '2-digit',
			hourCycle: 'h23',
			timeZoneName: 'short'
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
