# Phase 3b — Manual Testing Plan (New Features)

Covers features added after the initial Phase 3 console UI:
Schema DDL endpoints, SQL Editor, row selection checkboxes, system table protection, database sub-tabs.

For the original console UI tests (login, onboarding, table browsing, CRUD, storage, responsive design, error handling), see the Phase 3 testing plan in the conversation history.

## Prerequisites

Same as Phase 3 — gateway in dev mode, console dev server running, at least one project provisioned.

---

## 1. Schema DDL — Create Table (NEW)

### 1.1 Create Table via Console
- [ ] Navigate to Database > Table Editor
- [ ] Click "New" in the sidebar
- [ ] Enter table name: `todos`
- [ ] Verify default columns (id uuid PK, created_at timestamptz) are pre-filled
- [ ] Add columns: `title` (text, NOT NULL), `done` (boolean, default `false`)
- [ ] Click "Create Table"
- [ ] Verify: modal closes, `todos` appears in sidebar, is auto-selected, shows 0 rows with correct columns
- [ ] Insert a row into `todos` via the "Insert Row" button — verify it works end-to-end

### 1.2 Create Table — Error Handling
- [ ] Try creating `todos` again — should show "already exists" error inside the modal
- [ ] Try table name `my-bad-table!` — should show invalid characters error
- [ ] Verify "Create Table" button is disabled when name is empty or no columns defined

### 1.3 Add/Drop Column via Postman
- [ ] POST `.../schema/tables/todos/columns` with `{"name":"priority","type":"integer","nullable":true,"default_value":"0"}` — expect 201
- [ ] Refresh Database page — `priority` column should appear in the grid
- [ ] DELETE `.../schema/tables/todos/columns/priority` — expect 204
- [ ] Refresh — column gone

### 1.4 Drop Table Protection
- [ ] DELETE `.../schema/tables/storage_objects` — expect 400 with "system table" error
- [ ] DELETE `.../schema/tables/todos` — expect 204 (user tables can be dropped)

---

## 2. SQL Editor (NEW)

### 2.1 Basic Execution
- [ ] Navigate to Database > SQL Editor tab
- [ ] Type `SELECT * FROM users;`
- [ ] Click "Run" — results table appears with columns, rows, row count, execution time
- [ ] Verify trailing semicolon is accepted (no error)

### 2.2 Schema Sidebar
- [ ] Sidebar shows all tables (users, storage_objects, plus any created tables)
- [ ] Click chevron on `users` — expands to show columns with type badges
- [ ] Click table name `users` — inserts "users" into the editor
- [ ] Click column name `email` — inserts "email" into the editor
- [ ] Chevron click and name click are independent actions

### 2.3 Multi-Tab
- [ ] Click "+" to create a new query tab
- [ ] Type a different query in tab 2
- [ ] Switch tabs — each retains its SQL and results
- [ ] Close tab 2 via the X — switches back to tab 1
- [ ] Verify you cannot close the last remaining tab

### 2.4 Tab + History Persistence
- [ ] Execute 3 different queries
- [ ] Click "History" — all 3 appear with timestamps
- [ ] Click a history entry — populates the editor
- [ ] Create 2 tabs, type different queries
- [ ] Refresh the page — tabs and content should be restored from localStorage

### 2.5 Blocked Queries
- [ ] `DROP TABLE users` → "only SELECT queries are allowed"
- [ ] `INSERT INTO users (email) VALUES ('x')` → "only SELECT queries are allowed"
- [ ] `SELECT 1; DROP TABLE users` → "only single statements are allowed"
- [ ] `SELECT * FROM users FOR UPDATE` → "FOR UPDATE/SHARE is not allowed"
- [ ] Empty query → Run button should be disabled

### 2.6 Keyboard Shortcuts
- [ ] Ctrl/Cmd+Enter — executes the query
- [ ] Ctrl/Cmd+N — creates a new tab

---

## 3. Row Selection Checkboxes (NEW)

- [ ] Select `users` or `todos` table in Table Editor
- [ ] Verify checkboxes appear on each row and in the header
- [ ] Click individual row checkboxes — rows highlight
- [ ] Click header checkbox — all visible rows selected
- [ ] Selection bar appears: "N rows selected" + "Clear" button
- [ ] Click Clear — all checkboxes uncheck, bar disappears
- [ ] Click header checkbox when all selected — deselects all
- [ ] Partial selection — header checkbox shows indeterminate state
- [ ] Switch to a different table — selection resets

---

## 4. System Table Protection (NEW)

- [ ] Select `storage_objects` in sidebar — "system" badge appears next to name
- [ ] Toolbar shows "System table" label with lock icon
- [ ] No trash icon (delete button) on any row
- [ ] Cell editing still works on editable columns
- [ ] Select `users` table — NO system badge, delete buttons ARE visible

---

## 5. Database Sub-Tabs Navigation (NEW)

- [ ] Navigate to Database — "Table Editor" sub-tab is active (underlined)
- [ ] Click "SQL Editor" sub-tab — switches to SQL editor page
- [ ] Verify "Database" in the top project nav bar remains highlighted/active (not deselected)
- [ ] Click "Table Editor" — switches back to table browser
- [ ] Direct URL navigation: go to `/p/{id}/database/sql` — SQL Editor loads, both tabs correct

---

## 6. Postman Collection

- [ ] Import `docs/eurobase-phase3.postman_collection.json` into Postman
- [ ] Set `project_id` variable to your test project
- [ ] Run all folders in order: Schema DDL → Data API → SQL Editor → Cleanup
- [ ] All automated tests should pass (green)
