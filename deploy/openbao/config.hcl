# OpenBao server for local development (see compose.yaml).
ui            = true
disable_mlock = true # mlock keeps secrets out of swap but needs IPC_LOCK; not worth it on a laptop

# Integrated storage (Raft): the secrets are stored encrypted in this
# directory, a Docker volume. Raft is built for several servers that agree on
# every write; here it runs with one. (OpenBao dropped Vault's simpler "file"
# storage.)
storage "raft" {
  path    = "/openbao/file"
  node_id = "openbao-1"
}

# Plain HTTP: the port is published on 127.0.0.1 only, and inside Docker only
# the kind network can reach it. Anywhere shared, this needs TLS.
listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}

api_addr     = "http://127.0.0.1:8200"
cluster_addr = "http://127.0.0.1:8201" # Raft needs it, even with one server
