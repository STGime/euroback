-- legal_entity held only the company form ("SAS (Société par Actions
-- Simplifiée)", "LLC", "Inc.", "B.V. (…)") for the processors seeded in
-- 000025. The public sub-processor list and the compliance report show it
-- as the company name, so set the full registered names.
UPDATE sub_processors SET legal_entity = 'Scaleway SAS'
 WHERE name = 'Scaleway SAS' AND legal_entity = 'SAS (Société par Actions Simplifiée)';
UPDATE sub_processors SET legal_entity = 'Mollie B.V.'
 WHERE name = 'Mollie B.V.' AND legal_entity = 'B.V. (Besloten Vennootschap)';
UPDATE sub_processors SET legal_entity = 'Google LLC'
 WHERE name = 'Google LLC' AND legal_entity = 'LLC';
UPDATE sub_processors SET legal_entity = 'GitHub, Inc.'
 WHERE name = 'GitHub Inc. (Microsoft)' AND legal_entity = 'Inc.';
