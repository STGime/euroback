##############################################################################
# Eurobase — Terraform Outputs
##############################################################################

output "cluster_kubeconfig" {
  description = "Kubeconfig for the Kapsule cluster"
  value       = scaleway_k8s_cluster.eurobase.kubeconfig[0].config_file
  sensitive   = true
}

output "database_endpoint" {
  description = "PostgreSQL connection endpoint"
  value       = try(scaleway_rdb_instance.eurobase.load_balancer[0].ip, scaleway_rdb_instance.eurobase.endpoint_ip, "")
}

output "database_port" {
  description = "PostgreSQL connection port"
  value       = try(scaleway_rdb_instance.eurobase.load_balancer[0].port, scaleway_rdb_instance.eurobase.endpoint_port, 0)
}

output "redis_endpoint" {
  description = "Redis cluster endpoint"
  value = format(
    "redis://%s:%d",
    scaleway_redis_cluster.eurobase.public_network[0].ips[0],
    scaleway_redis_cluster.eurobase.public_network[0].port
  )
}

output "registry_endpoint" {
  description = "Container registry endpoint"
  value       = scaleway_registry_namespace.eurobase.endpoint
}

output "object_storage_endpoint" {
  description = "Object storage endpoint for platform assets"
  value       = "https://${scaleway_object_bucket.platform_assets.name}.s3.fr-par.scw.cloud"
}

# output "tem_domain_status" {
#   description = "TEM domain verification status"
#   value       = scaleway_tem_domain.eurobase.status
# }
