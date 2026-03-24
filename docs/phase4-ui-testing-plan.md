# Phase 4 — Manual UI Testing Plan

**Prerequisites:**
1. Migration 000014 applied (`migrate -path migrations -database $DATABASE_URL up 1`)
2. Gateway running in dev mode: `DEV_MODE=true go run ./cmd/gateway/`
3. Console running: `cd console && npm run dev`
4. At least one project created (via Projects page)
5. Navigate to `/p/{project_id}/database`

---

## A. Bulk Delete Rows

### A1. Setup — need rows to delete
1. Select or create a table (e.g. `todos`)
2. Insert 5+ rows using the "Insert Row" button
3. Verify all rows appear in the DataGrid

### A2. Select and delete multiple rows
1. Check the checkboxes on 3 rows
2. **Verify:** Selection bar appears showing "3 rows selected"
3. **Verify:** "Delete Selected" button appears (red outline) alongside "Clear"
4. Click "Delete Selected"
5. **Verify:** Confirmation dialog appears with correct count ("Delete 3 Rows")
6. Click "Cancel" — dialog closes, selection preserved
7. Click "Delete Selected" again, then confirm
8. **Verify:** Rows are removed from the grid
9. **Verify:** Selection is cleared
10. **Verify:** Row count updates in the sidebar and toolbar

### A3. System table guard
1. Select the `storage_objects` system table
2. Check some rows (if any exist)
3. **Verify:** "Delete Selected" button does NOT appear — only "Clear"

### A4. Edge case — select all then delete
1. Select all rows via the header checkbox
2. Click "Delete Selected" and confirm
3. **Verify:** Table shows "No rows found" empty state

---

## B. Enriched Schema Introspection — Column Badges

### B1. PK badge
1. Select any table with a primary key (e.g. the default `id` column)
2. **Verify:** Column header shows `PK` badge in amber next to the type badge

### B2. FK badge
1. Create a table with a foreign key (use New Table modal with FK config)
2. **Verify:** The FK column header shows `FK` badge in indigo
3. Hover over the `FK` badge
4. **Verify:** Tooltip shows the referenced `table.column`

### B3. UQ badge
1. Select a table and click the edit pencil on a column header (e.g. `title`)
2. In the Column Edit modal, check the **"Unique"** checkbox and click "Save Changes"
3. **Verify:** Column header now shows `UQ` badge in teal

### B4. Multiple badges
1. On a column that is both PK and unique
2. **Verify:** Both `PK` and `UQ` badges are visible and not overlapping

---

## C. Foreign Keys

### C1. Create table with inline FK
1. Click "New" to open the New Table modal
2. Enter table name: `orders`
3. Add columns: `id` (uuid, PK), `customer_name` (text)
4. Add column `product_id` (uuid)
5. In the FK row for `product_id`: select target table → target column → ON DELETE action
6. **Verify:** The generated SQL preview shows the FOREIGN KEY clause
7. Click "Create Table"
8. **Verify:** Table created, `product_id` column shows `FK` badge in DataGrid
9. **Verify:** Schema introspection reflects the FK

### C2. Add FK via Column Edit
1. Select a table with no FK on a particular column
2. Click the edit pencil on a column header
3. In the "Foreign Key" section, select a target table and column
4. Choose ON DELETE action
5. Click "Save Changes"
6. **Verify:** `FK` badge appears on the column

### C3. Drop FK via Column Edit
1. Open Column Edit on a column that has an FK
2. **Verify:** Current FK is shown (e.g. "References categories.id")
3. Click "Drop FK"
4. **Verify:** FK is removed, badge disappears after reload

### C4. Error handling
1. Try adding FK to a column referencing a nonexistent table
2. **Verify:** Error message is displayed

---

## D. Unique Constraints

### D1. Create table with inline unique
1. In the New Table modal, check the "UQ" checkbox on a column
2. Create the table
3. **Verify:** `UQ` badge appears on that column

### D2. Add unique via Add Column modal
1. Click "Add Column" in the toolbar
2. Enter a column name and type
3. Check the **"Unique"** checkbox (next to "Nullable")
4. Click "Add Column"
5. **Verify:** New column appears with `UQ` badge in the DataGrid header

### D3. Toggle unique via Column Edit
1. Click the edit pencil icon on a column header that does NOT have a `UQ` badge
2. In the Column Edit modal, check the **"Unique"** checkbox (next to "Nullable")
3. Click "Save Changes"
4. **Verify:** `UQ` badge appears on that column header
5. Click the edit pencil on the same column again
6. Uncheck "Unique", click "Save Changes"
7. **Verify:** `UQ` badge disappears

### D4. Unique violation
1. Add a unique constraint on a column
2. Insert two rows with the same value for that column
3. **Verify:** Second insert shows a user-friendly error about unique constraint

---

## E. Indexes

### E1. Index Panel visibility
1. Select a non-system table
2. **Verify:** "Indexes" collapsible panel appears below the DataGrid
3. Click to expand it
4. **Verify:** Any existing indexes are listed (at minimum the PK index)

### E2. Create index
1. Expand the Indexes panel
2. Select a column from the dropdown
3. Leave "Unique" unchecked
4. Click "Create Index"
5. **Verify:** New index appears in the list with name `idx_{table}_{column}`

### E3. Create unique index
1. Select a column, check "Unique"
2. Click "Create Index"
3. **Verify:** Index appears with `UNIQUE` badge

### E4. Drop index
1. Click the trash icon on an index
2. **Verify:** Index is removed from the list

### E5. Error handling
1. Try creating an index on a column that already has one with the same name
2. **Verify:** Error message is displayed inline

### E6. System table guard
1. Select `storage_objects`
2. **Verify:** Index panel does NOT appear

---

## F. Schema Diagram (Visual ERD)

### F1. Tab navigation
1. From the Database page, look at the tab bar
2. **Verify:** Four tabs visible: "Table Editor", "SQL Editor", "Schema Diagram", "Migration History"
3. Click "Schema Diagram"
4. **Verify:** URL changes to `/p/{id}/database/schema`
5. **Verify:** Tab is highlighted with the eurobase-600 underline

### F2. Diagram rendering
1. Ensure at least 2-3 tables exist (with at least one FK relationship)
2. Navigate to Schema Diagram tab
3. **Verify:** Tables render as rounded rectangles with purple headers
4. **Verify:** Each table shows its column names and types
5. **Verify:** FK relationships shown as bezier curves with arrow markers

### F3. Pan
1. Click and drag on the background (not on a table)
2. **Verify:** The diagram pans smoothly
3. Release — panning stops

### F4. Zoom
1. Scroll up (mouse wheel) on the diagram
2. **Verify:** Diagram zooms in
3. Scroll down
4. **Verify:** Diagram zooms out
5. **Verify:** Zoom is clamped (doesn't go infinitely small or large)

### F5. Table highlight
1. Click on a table node
2. **Verify:** That table and its connected tables stay full opacity
3. **Verify:** Unconnected tables fade to ~30% opacity
4. **Verify:** Connected FK edges turn indigo, unconnected edges turn gray
5. Click the same table again
6. **Verify:** Highlight is cleared, all tables return to full opacity

### F6. Empty state
1. Delete all tables (or use a project with no tables)
2. Navigate to Schema Diagram
3. **Verify:** Shows "No tables to display" message

### F7. Loading state
1. On a slower connection, navigate to Schema Diagram
2. **Verify:** Spinner shown with "Laying out schema..." text while ELK calculates layout

---

## Cross-Feature Checks

### Schema History
1. After performing FK/unique/index operations, go to "Migration History" tab
2. **Verify:** New action types appear: `add_foreign_key`, `drop_foreign_key`, `add_unique_constraint`, `drop_unique_constraint`, `create_index`, `drop_index`

### Refresh consistency
1. Perform any DDL operation (add FK, create index, etc.)
2. Click "Refresh" on the Table Editor
3. **Verify:** Updated metadata (badges, indexes) reflects immediately

### Navigation
1. Switch between all 4 tabs rapidly
2. **Verify:** No blank pages, no console errors
3. Go back to Table Editor — data and schema are intact

---

## Browser Compatibility
Test the above in:
- [ ] Chrome (latest)
- [ ] Firefox (latest)
- [ ] Safari (latest)
