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

  auto_upgrade {
    enable                        = true
    maintenance_window_start_hour = 3
    maintenance_window_day        = "sunday"
  }

  tags = ["eurobase", var.environment, "eu-sovereign"]
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
# ---------------------------------------------------------------------------

resource "scaleway_rdb_instance" "eurobase" {
  name           = "eurobase-db"
  node_type      = "DB-DEV-S"
  engine         = "PostgreSQL-16"
  is_ha_cluster  = false # MVP — upgrade for production HA later
  volume_type    = "lssd"

  tags = ["eurobase", var.environment]
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
# ---------------------------------------------------------------------------

resource "scaleway_object_bucket" "platform_assets" {
  name = "eurobase-platform-assets"

  tags = {
    environment = var.environment
    purpose     = "platform-assets"
  }
}

# ---------------------------------------------------------------------------
# Transactional Email (TEM)
# Activate TEM in the Scaleway console first, then uncomment.
# ---------------------------------------------------------------------------

# resource "scaleway_tem_domain" "eurobase" {
#   name       = "eurobase.app"
#   accept_tos = true
# }

# ---------------------------------------------------------------------------
# Container Registry
# ---------------------------------------------------------------------------

resource "scaleway_registry_namespace" "eurobase" {
  name      = "eurobase-app"
  is_public = false
}
