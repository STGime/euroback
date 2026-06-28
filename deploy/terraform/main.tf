##############################################################################
# Eurobase — Scaleway Infrastructure (FR jurisdiction only)
# No US cloud services. EU-sovereign by design.
##############################################################################

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    scaleway = {
      source  = "registry.terraform.io/scaleway/scaleway"
      version = "~> 2.40"
    }
  }

  backend "s3" {
    bucket                      = "eurobase-terraform-state"
    key                         = "infrastructure/terraform.tfstate"
    region                      = "fr-par"
    endpoint                    = "https://s3.fr-par.scw.cloud"
    skip_credentials_validation = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_s3_checksum            = true
  }
}

# ---------------------------------------------------------------------------
# Variables
# ---------------------------------------------------------------------------

variable "scw_access_key" {
  description = "Scaleway access key"
  type        = string
  sensitive   = true
}

variable "scw_secret_key" {
  description = "Scaleway secret key"
  type        = string
  sensitive   = true
}

variable "scw_project_id" {
  description = "Scaleway project ID"
  type        = string
}

variable "database_password" {
  description = "Password for the eurobase_api database user"
  type        = string
  sensitive   = true
}

variable "environment" {
  description = "Deployment environment"
  type        = string
  default     = "production"
}

# ---------------------------------------------------------------------------
# Provider
# ---------------------------------------------------------------------------

provider "scaleway" {
  access_key = var.scw_access_key
  secret_key = var.scw_secret_key
  project_id = var.scw_project_id
  region     = "fr-par"
  zone       = "fr-par-1"
}

# ---------------------------------------------------------------------------
# Private Network (required by Kapsule)
# ---------------------------------------------------------------------------

resource "scaleway_vpc_private_network" "eurobase" {
  name = "eurobase-pn"
  tags = ["eurobase", var.environment]
}

# ---------------------------------------------------------------------------
# Kubernetes — Kapsule
# ---------------------------------------------------------------------------

resource "scaleway_k8s_cluster" "eurobase" {
  name                        = "eurobase-cluster"
  version                     = "1.32"
  cni                         = "cilium"
  delete_additional_resources = true
  private_network_id          = scaleway_vpc_private_network.eurobase.id

  # etcd encryption at rest is managed by Scaleway on Kapsule — the
  # control plane (apiserver + etcd) is operator-owned and not
  # exposed to the customer. Scaleway encrypts both the etcd volume
  # (LUKS+AES-256-XTS, same as RDB) and Secret values via the
  # apiserver's `--encryption-provider-config`. Source: Scaleway
  # Kapsule security doc. This is documented here rather than asserted
  # via terraform because the Scaleway provider does not expose it.
  tags = ["eurobase", var.environment, "eu-sovereign", "etcd-encryption:scaleway-managed"]
}

resource "scaleway_k8s_pool" "eurobase" {
  cluster_id  = scaleway_k8s_cluster.eurobase.id
  name        = "eurobase-pool"
  node_type   = "DEV1-M"
  size        = 2
  min_size    = 2
  max_size    = 5
  autoscaling = true
  autohealing = true

  tags = ["eurobase", var.environment]
}

# ---------------------------------------------------------------------------
# Managed PostgreSQL 16
#
# Encryption at rest (#173 / Tier-1 GDPR #1)
# ==========================================
# Scaleway managed RDB encrypts every volume at rest with LUKS+AES-256-XTS,
# transparent to the workload. There is no opt-out and no opt-in — it is on
# by default on every Scaleway region, for every `volume_type`. Source:
# Scaleway "Encryption at rest" doc (scaleway.com/en/docs/managed-databases-
# for-postgresql/concepts/#encryption-at-rest). The provider does not expose
# this as an attribute (because it is non-toggleable), so the assertion is
# documentary, not a `precondition` block. The DPA report's
# `encryption_at_rest` flag is fed by the `ENCRYPTION_AT_REST` env var on
# the gateway, which production sets to "true" — see
# docs/compliance/data-residency.md for the full chain.
# ---------------------------------------------------------------------------

resource "scaleway_rdb_instance" "eurobase" {
  name           = "eurobase-db"
  node_type      = "DB-DEV-S"
  engine         = "PostgreSQL-16"
  is_ha_cluster  = false # MVP — upgrade for production HA later
  volume_type    = "lssd"

  tags = ["eurobase", var.environment, "encryption-at-rest:luks-aes256xts"]
}

resource "scaleway_rdb_database" "eurobase" {
  instance_id = scaleway_rdb_instance.eurobase.id
  name        = "eurobase"
}

resource "scaleway_rdb_user" "eurobase_api" {
  instance_id = scaleway_rdb_instance.eurobase.id
  name        = "eurobase_api"
  password    = var.database_password
  is_admin    = true
}

# ---------------------------------------------------------------------------
# Managed Redis
# ---------------------------------------------------------------------------

resource "scaleway_redis_cluster" "eurobase" {
  name         = "eurobase-redis"
  node_type    = "RED1-MICRO"
  version      = "8.4.0"
  cluster_size = 1
  user_name    = "eurobase"
  password     = var.database_password

  tags = ["eurobase", var.environment]
}

# ---------------------------------------------------------------------------
# Object Storage — platform assets bucket
# Tenant buckets are created dynamically by the gateway.
#
# Encryption at rest (#173): Scaleway Object Storage encrypts every object
# at rest with SSE-S3 (AES-256), enabled by default and not disable-able.
# https://www.scaleway.com/en/docs/object-storage/concepts/#encryption.
# The bucket policy + lifecycle for the WORM audit-archive bucket is
# specified in docs/compliance/audit-log.md (#171) and provisioned
# alongside the SIEM-export writer (#170) — kept out of this terraform
# until that PR lands so the bucket isn't dangling.
# ---------------------------------------------------------------------------

resource "scaleway_object_bucket" "platform_assets" {
  name = "eurobase-platform-assets"

  tags = {
    environment       = var.environment
    purpose           = "platform-assets"
    encryption_at_rest = "sse-s3-aes256"
  }
}

# ---------------------------------------------------------------------------
# Transactional Email (TEM)
# ---------------------------------------------------------------------------

resource "scaleway_tem_domain" "eurobase" {
  name       = "eurobase.app"
  accept_tos = true
}

# ---------------------------------------------------------------------------
# Container Registry
# ---------------------------------------------------------------------------

resource "scaleway_registry_namespace" "eurobase" {
  name      = "eurobase-app"
  is_public = false
}
