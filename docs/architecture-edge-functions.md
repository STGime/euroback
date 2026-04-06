# Edge Functions — Architecture

**Eurobase Serverless Compute (v1.3)**

EU-sovereign function execution on Scaleway infrastructure. Zero US cloud dependency.

---

## Implementation Status

### Phase 1 — Core (Complete)
- [x] Database migration (000026): `edge_functions`, `edge_function_logs` tables, `edge_function_limit` on plan_limits
- [x] Go service: `internal/functions/` — CRUD, logs, invocation proxy
- [x] Gateway routes: Platform management (`/platform/projects/{id}/functions/*`) + SDK invocation (`/v1/functions/{name}`)
- [x] Plan limits: Free = 3 functions, Pro = 25 functions. Usage tracked in dashboard.
- [x] CLI: `eurobase edge-functions` — list, deploy, get, delete, logs, invoke
- [x] SDK: `eurobase.functions.invoke(name, { body })` method
- [x] Console: Functions tab with list, code editor, settings, logs viewer
- [x] Documentation: Section 13 in console docs
- [x] Postman tests: `eurobase-edge-functions.postman_collection.json`

### Phase 2 — Runtime (Next)
- [ ] Deno Function Runner: `functions-runner/server.ts` (code written, needs deployment)
- [ ] Docker image: `deploy/docker/Dockerfile.fn`
- [ ] K8s deployment: `deploy/k8s/functions.yaml`
- [ ] CI/CD: Build + push functions-runner image
- [ ] Set `FUNCTION_RUNNER_URL=http://functions:8000` in gateway env

### Phase 3 — Advanced Triggers (Future)
- [ ] Cron → edge function trigger
- [ ] Webhook → function trigger
- [ ] Auth hooks (post-signup, post-signin)

**Note:** Phase 1 is fully functional for management (create/edit/delete functions, view logs). Function invocation returns 501 until the Deno runner is deployed (Phase 2). This allows developers to prepare their functions before the runtime goes live.

---

## 1. Architecture Overview

```
                                    ┌─────────────────────────────────────────────┐
                                    │          Scaleway Kapsule Cluster            │
                                    │                                             │
  HTTP Request                      │  ┌──────────┐     ┌──────────────────────┐  │
  POST /v1/functions/process-order  │  │          │     │   Function Runner    │  │
  ──────────────────────────────►   │  │ Gateway  │────►│   (Deno Pool)        │  │
                                    │  │          │     │                      │  │
                                    │  │ (Go,     │     │  ┌────────────────┐  │  │
                                    │  │  chi)    │     │  │ V8 Isolate     │  │  │
                                    │  │          │     │  │ Project A      │  │  │
                                    │  └──────────┘     │  │ - process-order│  │  │
                                    │       │           │  │ - send-invoice │  │  │
                                    │       │           │  └────────────────┘  │  │
                                    │       │           │  ┌────────────────┐  │  │
                                    │       │           │  │ V8 Isolate     │  │  │
                                    │       │           │  │ Project B      │  │  │
                                    │       │           │  │ - sync-crm     │  │  │
                                    │       │           │  └────────────────┘  │  │
                                    │       │           └──────────────────────┘  │
                                    │       │                    │                │
                                    │       ▼                    ▼                │
                                    │  ┌──────────┐     ┌──────────────┐         │
                                    │  │ Postgres │     │ Scaleway S3  │         │
                                    │  │ (RDB)    │     │ (Storage)    │         │
                                    │  └──────────┘     └──────────────┘         │
                                    └─────────────────────────────────────────────┘
```

### Key Decision: In-Cluster Deno Pool (not Scaleway Serverless Containers)

**Why not one Scaleway Serverless Container per project:**
- Each container = separate deployment, separate scaling, separate billing
- 100 projects × 10 functions = 1,000 containers to manage
- Cold starts of 300ms-2s per container
- Complex orchestration to route requests to the right container
- Scaleway Serverless pricing adds up: €0.10/100k requests + compute time

**Why an in-cluster Deno pool:**
- Single Kubernetes deployment, horizontally scalable (2-10 pods)
- V8 isolates provide per-project sandboxing (same model as Cloudflare Workers)
- Sub-millisecond isolate startup (no container cold start)
- Direct access to Postgres and S3 via internal cluster networking (no public internet)
- One bill: just the K8s node pool
- We control the runtime, security, and resource limits completely

---

## 2. Component Architecture

### 2.1 Function Runner (new K8s deployment)

A Deno-based HTTP server running inside Kapsule. It receives proxied requests from the Gateway, loads the function code from the database, executes it in a sandboxed V8 isolate, and returns the response.

```
deploy/k8s/functions.yaml     — K8s Deployment + Service
deploy/docker/Dockerfile.fn   — Deno runtime container image
```

**Runtime:** Deno 2.x (V8 isolates, built-in TypeScript, secure by default)

**Why Deno, not Node:**
- `--allow-net`, `--allow-read` etc. provide granular permission control
- Built-in TypeScript (no build step)
- Web-standard APIs (Request, Response, fetch) — same DX as Supabase Edge Functions
- V8 isolates are lighter than Node processes (~2MB vs ~30MB per context)

### 2.2 Request Flow

```
1. Client → HTTPS → Ingress → Gateway (Go)
2. Gateway authenticates (API key + optional JWT)
3. Gateway resolves project context (schema, plan limits)
4. Gateway checks function exists in DB
5. Gateway proxies to Function Runner (internal ClusterIP:8000)
   - Headers: X-Project-ID, X-Schema-Name, X-Function-Name, X-User-ID, X-User-Role
   - Body: original request body
6. Function Runner loads code from DB (cached in-memory with TTL)
7. Function Runner creates V8 isolate with injected context (db, storage, vault)
8. Function executes (sandboxed: no filesystem, limited network, CPU/memory capped)
9. Response flows back: Runner → Gateway → Client
```

### 2.3 Data Model

```sql
-- Migration: 000026_edge_functions.up.sql

-- Function code storage (platform-level, not per-tenant schema)
CREATE TABLE public.edge_functions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    code            TEXT NOT NULL,           -- TypeScript/JavaScript source
    entrypoint      TEXT NOT NULL DEFAULT 'handler',  -- exported function name
    verify_jwt      BOOLEAN NOT NULL DEFAULT true,    -- require end-user JWT
    import_map      JSONB,                  -- optional Deno import map
    env_vars        JSONB,                  -- encrypted at rest (vault key)
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE(project_id, name)
);

CREATE INDEX idx_edge_functions_project ON public.edge_functions(project_id);

-- Execution logs
CREATE TABLE public.edge_function_logs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    function_id     UUID NOT NULL REFERENCES edge_functions(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL,
    status          INTEGER NOT NULL,       -- HTTP status code returned
    duration_ms     INTEGER NOT NULL,
    error           TEXT,
    request_method  TEXT NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_edge_function_logs_fn ON public.edge_function_logs(function_id, created_at DESC);
CREATE INDEX idx_edge_function_logs_project ON public.edge_function_logs(project_id, created_at DESC);
```

### 2.4 Plan Limits

| Limit | Free | Pro |
|-------|------|-----|
| Functions per project | 3 | 25 |
| Invocations / month | 100K | 2M |
| Execution timeout | 10s | 60s |
| Memory per isolate | 64 MB | 256 MB |
| Code size | 1 MB | 10 MB |
| Env vars per function | 5 | 20 |

---

## 3. Function Runner — Detailed Design

### 3.1 Container Image

```dockerfile
# deploy/docker/Dockerfile.fn
FROM denoland/deno:2.1.0

WORKDIR /app

# The runner server — handles HTTP dispatch to isolates
COPY functions-runner/ /app/

# Pre-cache the Eurobase context library
RUN deno cache /app/server.ts

EXPOSE 8000

# --allow-net: internal cluster only (postgres, s3, gateway)
# --allow-env: read injected env vars
# --allow-read: read cached function code
CMD ["deno", "run", "--allow-net", "--allow-env", "--allow-read=/app", "/app/server.ts"]
```

### 3.2 Runner Server (TypeScript, runs on Deno)

```
functions-runner/
├── server.ts           — HTTP server, receives proxied requests from Gateway
├── isolate.ts          — V8 isolate manager (create, cache, destroy)
├── context.ts          — Builds ctx object (db, storage, vault, env)
├── cache.ts            — In-memory LRU cache for function code
└── lib/
    └── eurobase.ts     — The ctx API that functions import
```

**server.ts** — Core loop:
```typescript
Deno.serve({ port: 8000 }, async (req: Request) => {
  const projectId = req.headers.get("X-Project-ID");
  const schemaName = req.headers.get("X-Schema-Name");
  const functionName = req.headers.get("X-Function-Name");
  const userId = req.headers.get("X-User-ID");
  const userRole = req.headers.get("X-User-Role");

  // 1. Load function code (from cache or DB)
  const fn = await loadFunction(projectId, functionName);
  if (!fn) return new Response("Function not found", { status: 404 });

  // 2. Build context object
  const ctx = buildContext(projectId, schemaName, userId, userRole, fn.env_vars);

  // 3. Execute in isolate with timeout + memory limit
  try {
    const response = await executeInIsolate(fn.code, req, ctx, {
      timeoutMs: fn.plan === "pro" ? 60_000 : 10_000,
      memoryLimitMb: fn.plan === "pro" ? 256 : 64,
    });
    return response;
  } catch (err) {
    // Log execution error
    await logExecution(fn.id, projectId, 500, Date.now() - start, err.message);
    return new Response(JSON.stringify({ error: err.message }), { status: 500 });
  }
});
```

### 3.3 Isolate Execution Model

```
┌─────────────────────────────────────────────────┐
│              Function Runner Pod                 │
│                                                  │
│  Main Deno process (server.ts)                   │
│  ├── LRU code cache (100 functions, 5min TTL)    │
│  ├── DB connection pool (to Postgres)            │
│  │                                               │
│  │  Per-request:                                 │
│  │  ┌──────────────────────────────────────┐     │
│  │  │ V8 Isolate (sandboxed)               │     │
│  │  │                                      │     │
│  │  │  - Cannot access filesystem          │     │
│  │  │  - Cannot access other isolates      │     │
│  │  │  - Network: only fetch() allowed     │     │
│  │  │  - CPU: killed after timeout         │     │
│  │  │  - Memory: hard cap per plan         │     │
│  │  │                                      │     │
│  │  │  Injected globals:                   │     │
│  │  │  - ctx.db      (Postgres client)     │     │
│  │  │  - ctx.storage  (S3 operations)      │     │
│  │  │  - ctx.vault    (read-only secrets)  │     │
│  │  │  - ctx.env      (function env vars)  │     │
│  │  │  - ctx.user     (authenticated user) │     │
│  │  └──────────────────────────────────────┘     │
│  │                                               │
│  │  Isolate is destroyed after response          │
│  └───────────────────────────────────────────────│
└─────────────────────────────────────────────────┘
```

**Security boundaries:**
- Each request gets a fresh isolate (no shared state between invocations)
- `fetch()` is the only network API — no raw sockets, no DNS queries
- `ctx.db` connects via the internal Postgres pool, scoped to the project's schema
- `ctx.vault` is read-only, secrets decrypted server-side before injection
- No `Deno.run()`, no `Deno.readFile()` — only the injected context

### 3.4 The `ctx` Object (Developer API)

```typescript
// What developers get inside their function:

interface FunctionContext {
  // Database — scoped to the project's tenant schema
  db: {
    from(table: string): QueryBuilder;   // Same API as @eurobase/sdk
    sql(query: string, params?: any[]): Promise<Row[]>;  // Raw SQL (read-only)
  };

  // Storage — scoped to the project's S3 bucket
  storage: {
    upload(key: string, body: ReadableStream, contentType?: string): Promise<void>;
    download(key: string): Promise<ReadableStream>;
    getSignedUrl(key: string, expiresIn?: number): Promise<string>;
    delete(key: string): Promise<void>;
    list(prefix?: string): Promise<StorageObject[]>;
  };

  // Vault — read-only access to project secrets
  vault: {
    get(name: string): Promise<string | null>;
    list(): Promise<string[]>;  // names only
  };

  // Environment — per-function env vars (set via console/CLI)
  env: Record<string, string>;

  // Authenticated user (from JWT, if verify_jwt is true)
  user: {
    id: string;
    email: string;
    role: string;
  } | null;

  // Logging — appears in function execution logs
  log: {
    info(message: string, data?: Record<string, unknown>): void;
    warn(message: string, data?: Record<string, unknown>): void;
    error(message: string, data?: Record<string, unknown>): void;
  };
}
```

---

## 4. Gateway Integration

### 4.1 API Routes

```
Platform (console management):
  GET    /platform/projects/{id}/functions              — List functions
  POST   /platform/projects/{id}/functions              — Create/deploy function
  GET    /platform/projects/{id}/functions/{name}       — Get function details + code
  PUT    /platform/projects/{id}/functions/{name}       — Update function code/config
  DELETE /platform/projects/{id}/functions/{name}       — Delete function
  GET    /platform/projects/{id}/functions/{name}/logs  — Execution logs
  POST   /platform/projects/{id}/functions/{name}/test  — Test invoke (from console)

SDK (runtime invocation):
  POST   /v1/functions/{name}                           — Invoke function (any method)
  GET    /v1/functions/{name}                           — Invoke function (GET)
  PUT    /v1/functions/{name}                           — Invoke function (PUT)
  DELETE /v1/functions/{name}                           — Invoke function (DELETE)
```

### 4.2 Gateway Handler (Go)

```go
// internal/functions/handler.go

// HandleInvoke proxies a function invocation to the Function Runner.
func HandleInvoke(pool *pgxpool.Pool, runnerURL string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        functionName := chi.URLParam(r, "name")
        projectCtx := auth.ProjectContextFromRequest(r)

        // 1. Check function exists and is active
        var fn EdgeFunction
        err := pool.QueryRow(r.Context(),
            `SELECT id, name, verify_jwt, status FROM edge_functions
             WHERE project_id = $1 AND name = $2 AND status = 'active'`,
            projectCtx.ProjectID, functionName,
        ).Scan(&fn.ID, &fn.Name, &fn.VerifyJWT, &fn.Status)
        if err != nil {
            http.Error(w, `{"error":"function not found"}`, http.StatusNotFound)
            return
        }

        // 2. Check plan invocation limits
        if err := checkInvocationLimit(r.Context(), pool, projectCtx); err != nil {
            http.Error(w, `{"error":"invocation limit exceeded"}`, http.StatusTooManyRequests)
            return
        }

        // 3. Proxy request to Function Runner
        proxyReq, _ := http.NewRequestWithContext(r.Context(), r.Method, runnerURL, r.Body)
        proxyReq.Header.Set("X-Project-ID", projectCtx.ProjectID)
        proxyReq.Header.Set("X-Schema-Name", projectCtx.SchemaName)
        proxyReq.Header.Set("X-Function-Name", functionName)
        proxyReq.Header.Set("X-Plan", projectCtx.Plan)

        if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
            proxyReq.Header.Set("X-User-ID", claims.Subject)
            proxyReq.Header.Set("X-User-Role", claims.Role)
        }

        // Copy original headers the function might need
        proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

        resp, err := http.DefaultClient.Do(proxyReq)
        if err != nil {
            http.Error(w, `{"error":"function execution failed"}`, http.StatusBadGateway)
            return
        }
        defer resp.Body.Close()

        // 4. Forward response back to client
        for k, v := range resp.Header {
            w.Header()[k] = v
        }
        w.WriteHeader(resp.StatusCode)
        io.Copy(w, resp.Body)
    }
}
```

### 4.3 Router Registration

```go
// In internal/gateway/router.go, inside /v1 route group:

// Edge Functions invocation (API key + optional end-user JWT).
if fnRunnerURL != "" {
    r.Route("/functions", func(r chi.Router) {
        r.Use(apiKeyMw.Handler)
        r.Use(endUserMw.Handler)
        r.HandleFunc("/{name}", functions.HandleInvoke(pool, fnRunnerURL))
    })
}

// In /platform/projects/{id} route group:
if fnRunnerURL != "" {
    fnSvc := functions.NewService(pool)
    r.Route("/functions", func(r chi.Router) {
        r.Get("/", functions.HandleList(fnSvc))
        r.Post("/", functions.HandleCreate(fnSvc, limitsSvc))
        r.Get("/{name}", functions.HandleGet(fnSvc))
        r.Put("/{name}", functions.HandleUpdate(fnSvc))
        r.Delete("/{name}", functions.HandleDelete(fnSvc))
        r.Get("/{name}/logs", functions.HandleLogs(fnSvc))
        r.Post("/{name}/test", functions.HandleTestInvoke(fnSvc, fnRunnerURL))
    })
}
```

---

## 5. Deployment Architecture

### 5.1 Kubernetes Resources

```yaml
# deploy/k8s/functions.yaml

apiVersion: apps/v1
kind: Deployment
metadata:
  name: functions
  namespace: eurobase
  labels:
    app: functions
    component: runtime
spec:
  replicas: 2                      # Min 2 for availability
  selector:
    matchLabels:
      app: functions
  template:
    metadata:
      labels:
        app: functions
        component: runtime
    spec:
      containers:
        - name: deno-runner
          image: rg.fr-par.scw.cloud/eurobase-app/functions:latest
          ports:
            - containerPort: 8000
              protocol: TCP
          resources:
            requests:
              memory: "256Mi"      # Base memory for Deno + V8
              cpu: "250m"
            limits:
              memory: "1Gi"        # Headroom for concurrent isolates
              cpu: "1000m"
          readinessProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 3
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 5
            periodSeconds: 15
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: eurobase-secrets
                  key: DATABASE_URL
            - name: S3_ENDPOINT
              valueFrom:
                secretKeyRef:
                  name: eurobase-secrets
                  key: S3_ENDPOINT
            - name: S3_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: eurobase-secrets
                  key: S3_ACCESS_KEY
            - name: S3_SECRET_KEY
              valueFrom:
                secretKeyRef:
                  name: eurobase-secrets
                  key: S3_SECRET_KEY
            - name: VAULT_ENCRYPTION_KEY
              valueFrom:
                secretKeyRef:
                  name: eurobase-secrets
                  key: VAULT_ENCRYPTION_KEY
            - name: MAX_CONCURRENT_ISOLATES
              value: "50"
            - name: CODE_CACHE_SIZE
              value: "200"
            - name: CODE_CACHE_TTL_SECONDS
              value: "300"
      imagePullSecrets:
        - name: scw-registry
---
apiVersion: v1
kind: Service
metadata:
  name: functions
  namespace: eurobase
  labels:
    app: functions
spec:
  type: ClusterIP               # Internal only — Gateway proxies to this
  selector:
    app: functions
  ports:
    - port: 8000
      targetPort: 8000
      protocol: TCP
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: functions
  namespace: eurobase
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: functions
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: 80
```

### 5.2 Scaling Model

```
Idle (no invocations):
  2 pods × 256MB = 512MB cluster memory
  Cost: included in existing DEV1-M nodes

Light load (< 50 concurrent):
  2 pods handle it. Each pod runs up to 50 isolates concurrently.
  Isolate memory: ~2-5MB each × 50 = ~250MB per pod

Medium load (50-200 concurrent):
  HPA scales to 4-6 pods based on CPU/memory
  Auto: Kapsule node pool scales 2→5 nodes if needed

Heavy load (200+ concurrent):
  10 pods (max), 50 isolates each = 500 concurrent functions
  Beyond this → 429 Too Many Requests (queue with retry)

Scale-to-near-zero:
  Can't scale to 0 pods (K8s HPA minimum is 1), but 2 idle pods
  cost almost nothing on DEV1-M nodes (~€0.02/hr)
```

---

## 6. Developer Experience

### 6.1 CLI Workflow

```bash
# Create a new function (generates template)
eurobase functions create process-order --runtime edge

# This creates: functions/process-order.ts
# (local file, not a DB function)

# Edit locally
code functions/process-order.ts

# Deploy to Eurobase
eurobase functions deploy process-order

# Deploy all functions
eurobase functions deploy --all

# View logs
eurobase functions logs process-order

# Set environment variables
eurobase functions env set process-order MOLLIE_KEY=live_xxx

# Test invoke
eurobase functions invoke process-order --data '{"orderId":"abc"}'

# Delete
eurobase functions delete process-order
```

### 6.2 Function Template

```typescript
// functions/process-order.ts

// The default export receives (Request, FunctionContext)
export default async function handler(req: Request, ctx: Eurobase.FunctionContext) {
  // Only allow POST
  if (req.method !== "POST") {
    return new Response("Method not allowed", { status: 405 });
  }

  const { orderId } = await req.json();

  // Query the database (scoped to project schema)
  const [order] = await ctx.db.sql(
    "SELECT * FROM orders WHERE id = $1",
    [orderId]
  );

  if (!order) {
    return new Response(JSON.stringify({ error: "Order not found" }), {
      status: 404,
      headers: { "Content-Type": "application/json" },
    });
  }

  // Read a secret from Vault
  const mollieKey = await ctx.vault.get("MOLLIE_API_KEY");

  // Call external API
  const payment = await fetch("https://api.mollie.com/v2/payments", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${mollieKey}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      amount: { value: order.total, currency: "EUR" },
      description: `Order ${orderId}`,
    }),
  });

  const paymentData = await payment.json();

  // Update the order
  await ctx.db.sql(
    "UPDATE orders SET status = 'processing', payment_id = $1 WHERE id = $2",
    [paymentData.id, orderId]
  );

  ctx.log.info("Payment initiated", { orderId, paymentId: paymentData.id });

  return new Response(JSON.stringify({
    status: "processing",
    paymentId: paymentData.id,
  }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
```

### 6.3 Console UI

```
┌─────────────────────────────────────────────────────────────────┐
│  Functions                                           [+ New]    │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │ process-order          active    v3    142 invocations/24h│  │
│  │ POST /v1/functions/process-order                    [···] │  │
│  └───────────────────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │ send-welcome-email     active    v1     38 invocations/24h│  │
│  │ POST /v1/functions/send-welcome-email               [···] │  │
│  └───────────────────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │ generate-invoice       active    v2     12 invocations/24h│  │
│  │ POST /v1/functions/generate-invoice                 [···] │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  Usage: 192 / 100,000 invocations this month (Free plan)       │
│                                                                 │
├─────────────────────────────────────────────────────────────────┤
│  Function Editor: process-order                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │ // TypeScript editor (Monaco or CodeMirror)               │  │
│  │ export default async function handler(req, ctx) {         │  │
│  │   const { orderId } = await req.json();                   │  │
│  │   const [order] = await ctx.db.sql(                       │  │
│  │     "SELECT * FROM orders WHERE id = $1",                 │  │
│  │     [orderId]                                             │  │
│  │   );                                                      │  │
│  │   ...                                                     │  │
│  │ }                                                         │  │
│  └───────────────────────────────────────────────────────────┘  │
│  Settings:                                                      │
│  JWT Required: [x]    Timeout: [10s ▼]                         │
│  Env vars: MOLLIE_KEY=••••••    [+ Add]                        │
│                                                                 │
│  [Save & Deploy]  [Test]  [View Logs]                          │
└─────────────────────────────────────────────────────────────────┘
```

---

## 7. Trigger Types

### 7.1 HTTP (v1.3 — initial release)

Direct invocation via REST endpoint. The primary trigger type.

```typescript
// Client-side (SDK)
const { data } = await eurobase.functions.invoke("process-order", {
  body: { orderId: "abc-123" },
});

// Or raw HTTP
POST https://myproject.eurobase.app/v1/functions/process-order
Authorization: Bearer <jwt>
Content-Type: application/json

{"orderId": "abc-123"}
```

### 7.2 Cron Schedule (v1.3 — extend existing cron system)

Cron jobs gain a third action type: `edge_function` (alongside `sql` and `rpc`).

```sql
-- Existing cron_jobs table, new action_type value:
INSERT INTO cron_jobs (project_id, name, schedule, action_type, action)
VALUES ($1, 'Daily cleanup', '0 3 * * *', 'edge_function', 'cleanup-stale-orders');
```

The cron executor in the worker calls the Function Runner internally when `action_type = 'edge_function'`.

### 7.3 Webhook (v1.4 — extend existing webhook system)

When a database event (INSERT/UPDATE/DELETE) fires, the webhook system can target an edge function instead of an external URL.

```json
{
  "table": "orders",
  "events": ["INSERT"],
  "target_type": "edge_function",
  "target": "process-new-order"
}
```

### 7.4 Auth Hooks (v1.4 — post-signup, post-signin)

```json
{
  "hook": "post_signup",
  "function": "onboard-new-user"
}
```

---

## 8. Security Model

### 8.1 Isolation Layers

```
Layer 1: Kubernetes namespace isolation
  - functions pods run in eurobase namespace
  - NetworkPolicy: only gateway can reach port 8000

Layer 2: Deno permissions
  - No filesystem access (--deny-read, --deny-write)
  - No subprocess spawning (--deny-run)
  - No system info (--deny-sys)
  - Network: fetch() only (no raw TCP/UDP)

Layer 3: V8 isolate sandbox
  - Separate memory heap per invocation
  - No shared globals between projects
  - CPU time limit enforced via Deno.core deadline

Layer 4: Database scoping
  - ctx.db always sets search_path to project's tenant schema
  - RLS policies apply (function runs as the authenticated user)
  - No access to other schemas or platform tables

Layer 5: Resource limits
  - Memory: hard cap per plan (64MB / 256MB)
  - CPU: timeout enforced (10s / 60s)
  - Concurrency: max 50 isolates per pod
  - Request body: max 2MB (free) / 10MB (pro)
```

### 8.2 Network Policy

```yaml
# deploy/k8s/network-policy-functions.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: functions-ingress
  namespace: eurobase
spec:
  podSelector:
    matchLabels:
      app: functions
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: gateway        # Only gateway can call functions
        - podSelector:
            matchLabels:
              app: worker         # Worker can call for cron triggers
      ports:
        - port: 8000
  egress:
    - to: []                      # Allow outbound (fetch to external APIs)
      ports:
        - port: 443               # HTTPS only
        - port: 5432              # PostgreSQL (internal)
```

### 8.3 Abuse Prevention

| Threat | Mitigation |
|--------|-----------|
| Crypto mining | CPU timeout (10s/60s), memory cap, billing alerts |
| DDoS amplification | Rate limit on invocations, no raw sockets |
| Data exfiltration | DB scoped to project schema, RLS applies |
| Secret leaking | Vault secrets injected server-side, not in code |
| Infinite loops | V8 deadline terminates isolate after timeout |
| Memory bomb | V8 heap limit kills isolate at cap |
| Fork bomb | No subprocess spawning (Deno --deny-run) |

---

## 9. SDK Integration

```typescript
// sdk/js/src/functions.ts

export class FunctionsClient {
  private baseUrl: string;
  private headers: Record<string, string>;

  constructor(baseUrl: string, headers: Record<string, string>) {
    this.baseUrl = baseUrl;
    this.headers = headers;
  }

  async invoke<T = unknown>(
    functionName: string,
    options?: {
      body?: unknown;
      method?: "GET" | "POST" | "PUT" | "DELETE";
      headers?: Record<string, string>;
    }
  ): Promise<{ data: T; error: null } | { data: null; error: FunctionError }> {
    const method = options?.method ?? "POST";
    const url = `${this.baseUrl}/v1/functions/${functionName}`;

    const response = await fetch(url, {
      method,
      headers: {
        ...this.headers,
        "Content-Type": "application/json",
        ...options?.headers,
      },
      body: options?.body ? JSON.stringify(options.body) : undefined,
    });

    if (!response.ok) {
      const error = await response.json().catch(() => ({ error: "Unknown error" }));
      return { data: null, error: { status: response.status, message: error.error } };
    }

    const data = await response.json();
    return { data: data as T, error: null };
  }
}

// Usage:
const { data, error } = await eurobase.functions.invoke("process-order", {
  body: { orderId: "abc-123" },
});
```

---

## 10. File Summary

### New files

| File | Description |
|------|-------------|
| `migrations/000026_edge_functions.up.sql` | Tables: edge_functions, edge_function_logs |
| `migrations/000026_edge_functions.down.sql` | Reverse migration |
| `internal/functions/service.go` | CRUD for edge functions (DB operations) |
| `internal/functions/handler.go` | Platform API handlers (list, create, update, delete, logs) |
| `internal/functions/invoke.go` | Gateway → Runner proxy handler |
| `functions-runner/server.ts` | Deno HTTP server (receives proxied requests) |
| `functions-runner/isolate.ts` | V8 isolate lifecycle management |
| `functions-runner/context.ts` | Builds ctx object with db/storage/vault bindings |
| `functions-runner/cache.ts` | LRU code cache |
| `functions-runner/lib/eurobase.ts` | TypeScript types for FunctionContext |
| `deploy/docker/Dockerfile.fn` | Deno runtime container |
| `deploy/k8s/functions.yaml` | K8s Deployment + Service + HPA |
| `deploy/k8s/network-policy-functions.yaml` | Network isolation |
| `sdk/js/src/functions.ts` | SDK functions client |

### Modified files

| File | Change |
|------|--------|
| `internal/gateway/router.go` | Add function invoke routes + platform management routes |
| `cmd/gateway/main.go` | Accept FUNCTION_RUNNER_URL env var, pass to router |
| `internal/plans/limits.go` | Add function limits (count, invocations, timeout) |
| `internal/cron/executor.go` | Add `edge_function` action type |
| `internal/cli/functions.go` | Add deploy, invoke, env, logs subcommands |
| `deploy/terraform/main.tf` | (optional) Add node pool for function pods |
| `.github/workflows/ci.yml` | Build + push functions-runner image |
| `console/src/routes/(app)/p/[id]/functions/+page.svelte` | Functions management UI |
| `console/src/lib/api.ts` | Add functions API methods |

---

## 11. Implementation Phases

### Phase 1 (1 week): Core runtime
- Migration + DB tables
- Function Runner (Deno server + isolate execution)
- Gateway proxy handler
- Platform CRUD API
- Deploy via CLI

### Phase 2 (3 days): Console + SDK
- Functions management page in console
- Code editor with syntax highlighting
- SDK `functions.invoke()` method
- Execution logs viewer

### Phase 3 (3 days): Triggers + polish
- Cron → edge function trigger
- Env vars management (encrypted)
- Plan limit enforcement
- Invocation metering

### Phase 4 (v1.4, later): Advanced triggers
- Webhook → function trigger
- Auth hooks (post-signup, post-signin)
- Scheduled function triggers via console UI

---

## 12. Cost Estimate

### Infrastructure (monthly)

| Component | Spec | Cost |
|-----------|------|------|
| 2× DEV1-M nodes (existing pool) | Shared with gateway/worker | €0 incremental |
| Additional node (if HPA scales) | 1× DEV1-M | ~€12/mo |
| Container Registry (function images) | Already exists | €0 |
| Postgres (function code storage) | Already exists | €0 |

**Total incremental cost: €0-12/month** (functions share existing K8s nodes)

The in-cluster approach is dramatically cheaper than Scaleway Serverless Containers (~€0.10/100k requests + compute) or any external FaaS.

### Break-even vs external FaaS

At 10M invocations/month across all projects:
- Scaleway Serverless: ~€1,000/mo
- In-cluster Deno pool: ~€24/mo (2 extra nodes)

The in-cluster model wins at any scale above ~100K invocations/month.
