<!--
Customer-fillable template. Not a Eurobase policy — the Customer
completes this and keeps their own copy in their own audit file. We
ship the template so a Steuerberater or lawyer setting up their
Eurobase project doesn't have to build one from scratch.

Publication note: link this from the Legal Team console area with a
"Download and fill in" button that pre-fills the {{ }} tokens where
we can. Customer signs, retains locally.
-->

# GoBD Verfahrensdokumentation (Muster / template)

**Für Kunden mit steuerrelevanten Daten auf Eurobase.**

Dieses Dokument ist eine kundenseitig auszufüllende Verfahrens­dokumentation gemäß BMF-Schreiben vom 28.11.2019 (letztmalig ergänzt am 11.03.2024). Es bildet die dokumentarische Grundlage dafür, dass die auf Eurobase gespeicherten steuerrelevanten Daten den GoBD-Anforderungen (§§145–147 AO, §257 HGB) genügen.

Eurobase liefert das technische Fundament (Unveränderbarkeit via WORM-Storage, revisionssichere Retention-Holds, GoBD-konformer Datenexport) — der Kunde dokumentiert das *Verfahren*, mit dem er dieses Fundament nutzt.

## 1. Allgemeine Beschreibung

- **Kunde (Steuerpflichtiger):** {{CUSTOMER_LEGAL_NAME}}
- **Anschrift:** {{CUSTOMER_ADDRESS}}
- **Steuernummer:** {{CUSTOMER_TAX_ID}}
- **Verantwortliche Person:** {{RESPONSIBLE_PERSON}}
- **Dienstleister:** Eurobase OÜ, Ahtri 12, Tallinn 15551, Estonia — code registre 17557586
- **Vertragliche Grundlage:** Data Processing Agreement v2 + Legal Team Addendum (deutscher Legal-Tech-Zusatz)

## 2. Umfang der auf Eurobase gehaltenen steuerrelevanten Daten

_Kunden bitte konkretisieren, z. B.:_
- Ausgangsrechnungen mit Belegen (Bucket `/invoices/*`)
- Mandantenakten (Table `mandant_*`, Bucket `/mandant/*`)
- Zeiterfassung (Table `time_entries`)
- Kontoauszüge, sonstige Buchhaltungsdaten

## 3. Aufbewahrungspflichten

Die auf Eurobase gehaltenen Daten unterliegen folgenden Aufbewahrungspflichten:

| Datenkategorie | Rechtsgrundlage | Aufbewahrungsfrist | Umsetzung auf Eurobase |
|---|---|---|---|
| Buchungsbelege | §147 Abs. 1 Nr. 4 AO | 10 Jahre | Retention-Hold via `POST /compliance/retention-holds` mit `legal_basis: "§147 AO"` |
| Handelsbücher, Inventare, Jahresabschlüsse | §257 Abs. 1 Nr. 1 HGB | 10 Jahre | Retention-Hold `legal_basis: "§257 HGB"` |
| Handels-Geschäftsbriefe | §257 Abs. 1 Nr. 2 HGB | 6 Jahre | Retention-Hold `legal_basis: "§257 HGB (6y)"` |
| Handakten (Rechtsanwälte) | §50 BRAO | 6 Jahre nach Mandatsende | Retention-Hold `legal_basis: "§50 BRAO"` mit `expires_at` relative to Mandatsende |

## 4. Unveränderbarkeit (§146 Abs. 4 AO)

Steuerrelevante Datensätze werden nach Erstellung nicht mehr verändert. Technische Umsetzung:

- **Storage (Belege, PDF, Bilder):** Scaleway Object Storage mit S3 Object Lock (Compliance-Modus) — Objekte werden mit einer Retention-Sperre versehen, die auch mit Root-Credentials nicht aufhebbar ist. (Object-Lock-Support ships in M2b follow-up; current M2b handles data-row holds only. Update this section when the S3 WORM feature is live.)
- **Datenbank-Zeilen:** Retention-Holds (`retention_holds`) verhindern Löschversuche über die Standardschnittstelle. Änderungen an bereits gehaltenen Datensätzen werden vom Anwendungscode des Kunden zu unterdrücken sein.
- **Audit-Log:** Alle Anwendungs-Änderungen werden im `audit_log` mit einer Hash-Chain (per-project, siehe Eurobase-DPA Annex 2) revisionssicher protokolliert. Die Chain wird über `GET /compliance/audit-log/verify` kryptografisch überprüfbar.

## 5. Datenzugriff für Betriebsprüfer (Z1/Z2/Z3)

Für die Prüfung wird dem Betriebsprüfer der Zugang zu den steuerrelevanten Daten wie folgt bereitgestellt:

- **Z1 (unmittelbarer Datenzugriff):** Nicht anwendbar — Eurobase-Daten sind nur über die Anwendung des Kunden zugänglich.
- **Z2 (mittelbarer Datenzugriff):** Über den Anwendungs-Frontend des Kunden.
- **Z3 (Datenträgerüberlassung):** Über `POST /platform/projects/{id}/compliance/gobd-export`. Die generierte ZIP-Datei enthält:
  - `INDEX.XML` — Beschreibungsstandard der Finanzverwaltung (DTD-konform).
  - Eine CSV-Datei pro Tabelle (UTF-8, semikolongetrennt).
  - Eine gerenderte Kopie dieser Verfahrensdokumentation.

## 6. Verantwortlichkeiten

- **Der Kunde** ist verantwortlich für die inhaltliche Richtigkeit der Daten, die Anwendung der zutreffenden Retention-Holds bei Datenerzeugung, und die Aktualität dieser Verfahrensdokumentation.
- **Eurobase** ist verantwortlich für die technische Verfügbarkeit der Retention-Hold- und WORM-Mechanismen, die Unveränderbarkeit des Audit-Logs, und die Bereitstellung des GoBD-Exports.

---

**Unterschrift:** _____________________
**Ort, Datum:** _____________________
