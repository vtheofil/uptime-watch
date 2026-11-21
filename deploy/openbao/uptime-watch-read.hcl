# OpenBao policy for External Secrets Operator in the kind cluster: read the
# uptime-watch secrets, nothing else. kv version 2 keeps the values under
# secret/data/<path>.
path "secret/data/uptime-watch/*" {
  capabilities = ["read"]
}
