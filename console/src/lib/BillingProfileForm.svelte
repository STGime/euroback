<script lang="ts">
	// Buyer identity + address form for Estonian VAT Act §37 /
	// Accounting Act invoice compliance. Used both stand-alone
	// on /billing/profile and inline in the upgrade-to-Pro flow.
	//
	// Server is authoritative — we mirror the DB CHECK constraints
	// client-side for immediate feedback but the PUT handler
	// re-runs them, and any 400 invalid_field response paints the
	// exact input server-side (err.field). Country is a fixed
	// select from a curated list (EU + EEA + a handful of common
	// non-EU) so the server's regex is a safety net, not a filter.
	import { api, APIError, type BillingProfile, type BillingProfileInput } from '$lib/api.js';

	interface Props {
		existing?: BillingProfile | null;
		saveLabel?: string;
		onSaved?: (profile: BillingProfile) => void;
	}

	let { existing = null, saveLabel = 'Save', onSaved = () => {} }: Props = $props();

	// Local form state, seeded from existing when editing.
	let entityType: 'individual' | 'business' = $state(existing?.entity_type ?? 'business');
	let legalName = $state(existing?.legal_name ?? '');
	let streetAddress = $state(existing?.street_address ?? '');
	let postalCode = $state(existing?.postal_code ?? '');
	let city = $state(existing?.city ?? '');
	let country = $state(existing?.country ?? 'EE');
	let registryCode = $state(existing?.registry_code ?? '');
	let vatNumber = $state(existing?.vat_number ?? '');

	let saving = $state(false);
	let error = $state<string | null>(null);
	let errorField = $state<string | null>(null);

	// EU-27 + EEA + a handful of common non-EU. The server
	// accepts any 2-letter code so widening later is a UI-only
	// change. Kept alphabetical by code for stable diffs.
	const COUNTRIES: Array<{ code: string; name: string }> = [
		{ code: 'AT', name: 'Austria' },
		{ code: 'BE', name: 'Belgium' },
		{ code: 'BG', name: 'Bulgaria' },
		{ code: 'CA', name: 'Canada' },
		{ code: 'CH', name: 'Switzerland' },
		{ code: 'CY', name: 'Cyprus' },
		{ code: 'CZ', name: 'Czechia' },
		{ code: 'DE', name: 'Germany' },
		{ code: 'DK', name: 'Denmark' },
		{ code: 'EE', name: 'Estonia' },
		{ code: 'ES', name: 'Spain' },
		{ code: 'FI', name: 'Finland' },
		{ code: 'FR', name: 'France' },
		{ code: 'GB', name: 'United Kingdom' },
		{ code: 'GR', name: 'Greece' },
		{ code: 'HR', name: 'Croatia' },
		{ code: 'HU', name: 'Hungary' },
		{ code: 'IE', name: 'Ireland' },
		{ code: 'IS', name: 'Iceland' },
		{ code: 'IT', name: 'Italy' },
		{ code: 'LI', name: 'Liechtenstein' },
		{ code: 'LT', name: 'Lithuania' },
		{ code: 'LU', name: 'Luxembourg' },
		{ code: 'LV', name: 'Latvia' },
		{ code: 'MT', name: 'Malta' },
		{ code: 'NL', name: 'Netherlands' },
		{ code: 'NO', name: 'Norway' },
		{ code: 'PL', name: 'Poland' },
		{ code: 'PT', name: 'Portugal' },
		{ code: 'RO', name: 'Romania' },
		{ code: 'SE', name: 'Sweden' },
		{ code: 'SI', name: 'Slovenia' },
		{ code: 'SK', name: 'Slovakia' },
		{ code: 'US', name: 'United States' }
	];

	// Server enforces "EE business ⇒ registry_code required".
	// Mirror it here as a `*` on the label so the user knows before
	// hitting submit.
	let registryCodeRequired = $derived(entityType === 'business' && country === 'EE');

	async function submit(): Promise<void> {
		if (saving) return;
		saving = true;
		error = null;
		errorField = null;
		try {
			const input: BillingProfileInput = {
				entity_type: entityType,
				legal_name: legalName.trim(),
				street_address: streetAddress.trim(),
				postal_code: postalCode.trim(),
				city: city.trim(),
				country: country.trim().toUpperCase()
			};
			if (registryCode.trim() !== '') {
				input.registry_code = registryCode.trim();
			}
			if (vatNumber.trim() !== '') {
				input.vat_number = vatNumber.trim().toUpperCase();
			}
			const saved = await api.upsertBillingProfile(input);
			onSaved(saved);
		} catch (err) {
			if (err instanceof APIError) {
				error = err.message;
				errorField = err.field ?? null;
			} else {
				error = err instanceof Error ? err.message : String(err);
			}
		} finally {
			saving = false;
		}
	}

	function fieldClass(name: string): string {
		const base =
			'block w-full rounded-md border px-3 py-2 text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-blue-500';
		return errorField === name
			? `${base} border-red-400 focus:ring-red-500`
			: `${base} border-gray-300`;
	}
</script>

<form
	class="space-y-4"
	onsubmit={(e) => {
		e.preventDefault();
		void submit();
	}}
>
	<!-- Entity type toggle -->
	<div>
		<span class="block text-sm font-medium text-gray-700">You are billing as</span>
		<div class="mt-1 flex gap-4">
			<label class="flex items-center gap-2 text-sm text-gray-700">
				<input type="radio" bind:group={entityType} value="business" class="text-blue-600" />
				A business
			</label>
			<label class="flex items-center gap-2 text-sm text-gray-700">
				<input type="radio" bind:group={entityType} value="individual" class="text-blue-600" />
				An individual
			</label>
		</div>
	</div>

	<div>
		<label for="legal-name" class="block text-sm font-medium text-gray-700">
			{entityType === 'business' ? 'Company legal name' : 'Full name'}
		</label>
		<input
			id="legal-name"
			type="text"
			required
			maxlength="200"
			bind:value={legalName}
			class="mt-1 {fieldClass('legal_name')}"
			placeholder={entityType === 'business' ? 'Example Kaubandus OÜ' : 'Jane Smith'}
		/>
	</div>

	<div>
		<label for="street" class="block text-sm font-medium text-gray-700">Street address</label>
		<input
			id="street"
			type="text"
			required
			maxlength="200"
			bind:value={streetAddress}
			class="mt-1 {fieldClass('street_address')}"
			placeholder="Ahtri 12"
		/>
	</div>

	<div class="grid grid-cols-2 gap-4">
		<div>
			<label for="postal" class="block text-sm font-medium text-gray-700">Postal code</label>
			<input
				id="postal"
				type="text"
				required
				maxlength="20"
				bind:value={postalCode}
				class="mt-1 {fieldClass('postal_code')}"
				placeholder="15551"
			/>
		</div>
		<div>
			<label for="city" class="block text-sm font-medium text-gray-700">City</label>
			<input
				id="city"
				type="text"
				required
				maxlength="100"
				bind:value={city}
				class="mt-1 {fieldClass('city')}"
				placeholder="Tallinn"
			/>
		</div>
	</div>

	<div>
		<label for="country" class="block text-sm font-medium text-gray-700">Country</label>
		<select id="country" bind:value={country} class="mt-1 {fieldClass('country')}">
			{#each COUNTRIES as c (c.code)}
				<option value={c.code}>{c.name} ({c.code})</option>
			{/each}
		</select>
	</div>

	{#if entityType === 'business'}
		<div>
			<label for="registry" class="block text-sm font-medium text-gray-700">
				Registry code {#if registryCodeRequired}<span class="text-red-500">*</span>{/if}
			</label>
			<input
				id="registry"
				type="text"
				maxlength="40"
				bind:value={registryCode}
				class="mt-1 {fieldClass('registry_code')}"
				placeholder={country === 'EE' ? 'Estonian registrikood (8 digits)' : 'Optional'}
			/>
			{#if registryCodeRequired}
				<p class="mt-1 text-xs text-gray-500">
					Required for Estonian businesses (registrikood, from the Business Register).
				</p>
			{/if}
		</div>

		<div>
			<label for="vat" class="block text-sm font-medium text-gray-700">
				VAT number <span class="text-gray-400">(optional)</span>
			</label>
			<input
				id="vat"
				type="text"
				maxlength="20"
				bind:value={vatNumber}
				class="mt-1 {fieldClass('vat_number')}"
				placeholder="e.g. EE123456789"
			/>
			<p class="mt-1 text-xs text-gray-500">
				Format: 2-letter country prefix + digits. Only needed if your accountant wants it on the invoice.
			</p>
		</div>
	{/if}

	{#if error}
		<div class="rounded-md border border-red-200 bg-red-50 p-3">
			<p class="text-sm text-red-800">{error}</p>
		</div>
	{/if}

	<div>
		<button
			type="submit"
			disabled={saving}
			class="inline-flex items-center rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50"
		>
			{saving ? 'Saving…' : saveLabel}
		</button>
	</div>
</form>
