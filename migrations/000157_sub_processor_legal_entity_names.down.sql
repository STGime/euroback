UPDATE sub_processors SET legal_entity = 'SAS (Société par Actions Simplifiée)'
 WHERE name = 'Scaleway SAS' AND legal_entity = 'Scaleway SAS';
UPDATE sub_processors SET legal_entity = 'B.V. (Besloten Vennootschap)'
 WHERE name = 'Mollie B.V.' AND legal_entity = 'Mollie B.V.';
UPDATE sub_processors SET legal_entity = 'LLC'
 WHERE name = 'Google LLC' AND legal_entity = 'Google LLC';
UPDATE sub_processors SET legal_entity = 'Inc.'
 WHERE name = 'GitHub Inc. (Microsoft)' AND legal_entity = 'GitHub, Inc.';
