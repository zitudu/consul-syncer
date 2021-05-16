# consul-syncer

Consul-Syncer syncs services, kvs and events from source consul to target consul.

## Examples

Sync by services' name:

`consul-syncer -source-addr=127.0.0.1:8500 -target-addr=127.0.0.1:8501 -service-name a -service-name b -service-name c`

Will sync services a, b and c.
