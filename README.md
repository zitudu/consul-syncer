# consul-syncer

Consul-Syncer syncs services, kvs and events from source consul to target consul.

## Installation

Go1.16 and above is required.

`$ github.com/zitudu/consul-syncer/cmd/consul-syncer`

## Examples

### Services

Sync services by names:

`consul-syncer -source-addr=127.0.0.1:8500 -target-addr=127.0.0.1:8501 -service-name a -service-name b -service-name c`

Will sync services a, b and c. By tags: `-service-tag front-tier`.

It also supports regular expressions by prefixing with `re:`, e.g. `-service-name re:^web-.* -service-tag tag1 -service-tag re:tag[23]` will watch and sync all services naming prefixed with `web-` and tagged with `tag1` and one of `tag2` or `tag3`.

### KVs

`-kv name` and `-kvprefix prefix` are provided for syncing one or more kvs.

### Events

Watch events from target and fire them to source consul using flag `-event name`.

### Hybrid

All of services, kvs and events support zero or more options and hybrid usage.
