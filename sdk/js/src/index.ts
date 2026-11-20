/**
 * @eurobase/sdk — EU-sovereign Backend-as-a-Service TypeScript SDK
 *
 * Zero external dependencies. Works in browsers, Node.js 18+, Deno, Bun and
 * edge runtimes. On a server, see `auth: { persistSession: false }` and
 * `withAccessToken()`.
 */

export { createClient } from './client'
export type { EurobaseClient, EurobaseConfig, EurobaseUserClient, EurobaseAuthOptions } from './client'
export type { VerifiedAccessToken, VerifyAccessTokenOptions, VerifyAccessTokenResult } from './jwt'
export { QueryBuilder, DatabaseClient } from './database'
export type { QueryResult, RpcResult } from './database'
export { StorageClient } from './storage'
export type {
  UploadResult,
  SignedUrlResult,
  ObjectInfo,
  ListResult,
  ListOptions,
  UploadOptions,
  SignedUrlOptions,
} from './storage'
export { RealtimeClient } from './realtime'
export type {
  RealtimeEvent,
  SubscriptionCallback,
  Subscription,
  RealtimeEventType,
  RealtimeErrorCallback,
} from './realtime'
export { AuthClient, EMAIL_NOT_CONFIRMED, ACCOUNT_SUSPENDED } from './auth'
export { FunctionsClient } from './functions'
export type {
  FunctionInvokeOptions,
  FunctionError,
  ScheduleSpec,
  ScheduleRow,
  ScheduleError,
} from './functions'
export { SchedulesClient } from './schedules'
export { VaultClient } from './vault'
export type { VaultSecret, VaultSecretMeta } from './vault'
export { SchemaClient } from './ddl'
export type {
  ColumnDefinition,
  CreateTableOptions,
  CreateTableResult,
  RLSPreset,
} from './ddl'
export type {
  AuthUser,
  AuthSession,
  SignUpCredentials,
  SignUpResult,
  SignInCredentials,
  AuthEvent,
  AuthStateChangeCallback,
  ExportRequest,
  ExportStatus,
  Passkey,
} from './auth'
export { passkeysSupported, passkeyErrorMessage } from './webauthn'
