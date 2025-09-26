locals {
  enabled = module.this.enabled
}

module "vpa" {
  source  = "cloudposse/helm-release/aws"
  version = "0.10.1"

  chart           = var.chart
  repository      = var.chart_repository
  description     = var.chart_description
  chart_version   = var.chart_version
  wait            = var.wait
  atomic          = var.atomic
  cleanup_on_fail = var.cleanup_on_fail
  timeout         = var.timeout

  create_namespace_with_kubernetes = var.create_namespace
  kubernetes_namespace             = var.kubernetes_namespace
  kubernetes_namespace_labels      = merge(module.this.tags, { name = var.kubernetes_namespace })

  eks_cluster_oidc_issuer_url = replace(module.eks.outputs.eks_cluster_identity_oidc_issuer, "https://", "")

  service_account_name      = module.this.name
  service_account_namespace = var.kubernetes_namespace

  iam_role_enabled = false # VPA doesn't require AWS IAM permissions

  values = compact([
    # standard k8s object settings
    yamlencode({
      fullnameOverride = module.this.name,
      rbac = {
        create = var.rbac_enabled
      }
    }),
    # VPA-specific values with component-specific resources
    yamlencode({
      recommender = {
        enabled   = true
        resources = var.recommender_resources
      }
      updater = {
        enabled   = false # Disable updater for recommendation-only mode
        resources = var.updater_resources
      }
      admissionController = {
        enabled   = var.admission_controller_enabled
        resources = var.admission_controller_resources
      }
    }),
    # additional values
    yamlencode(var.chart_values)
  ])

  context = module.this.context
}
