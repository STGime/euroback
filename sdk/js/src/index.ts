/**
 * @eurobase/sdk — EU-sovereign Backend-as-a-Service TypeScript SDK
 *
 * Zero external dependencies. Works in browsers and Node.js 18+.
 */

export { createClient } from './client'
export type { EurobaseClient, EurobaseConfig } from './client'
export { QueryBuilder, DatabaseClient } from './database'
export type { QueryResult } from './database'
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
} from './realtime'
export { AuthClient } from './auth'
export { FunctionsClient } from './functions'
export type { FunctionInvokeOptions, FunctionError } from './functions'
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
  SignInCredentials,
  AuthEvent,
  AuthStateChangeCallback,
} from './auth'
