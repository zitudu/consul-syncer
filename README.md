# consul-syncer

Consul-Syncer syncs services, kvs and events from one consul instance to another one.

## Installation

Go1.16 and above is required. Run

`$ go install github.com/zitudu/consul-syncer/cmd/consul-syncer`

## How it works?

Consul-syncer register/fire/put services/events/kvs to target consul which collected from source consul via [consul watches](https://www.consul.io/docs/dynamic-app-config/watches).

For services, consul-syncer watches `services` which detects all services registered on source consul and then watches each single service respectly. Any changes of service list on target consul will cause consul-syncer to adjust the list watching list. Each watch plan of services registers its coresponding service to target consul via [catalog's register-entity API](https://www.consul.io/api-docs/catalog#register-entity) which keep the node of service to the same as that on source consul, and deregisters that when it unwatched.

As for events and kvs, launchs watch plan for each one and fire/put data it received to target consul.

### Clean up

Consul-syncer will deregister services it synced from source consul. To make it easy, consul-syncer attaches a special meta (`"consul-syncer-external": "true"`) to nodes of services it synced, and deregisters all services of nodes with that meta info. Note that this behavior may cause conflicts if nodes registering services to both source and target consul in the meantime.

## Examples

### Services

Sync services by names:

`consul-syncer -source-addr=127.0.0.1:8500 -target-addr=127.0.0.1:8501 -service-name a -service-name b -service-name c`

It will sync services a, b and c. To sync services by tags, use `-service-tag` flag.

It also supports regular expressions by prefixing with `re:`, e.g. `-service-name re:^web-.* -service-tag tag1 -service-tag re:tag[23]` will watch and sync all services naming prefixed with `web-` and tagged with `tag1` and one of `tag2` or `tag3`.

### KVs

`-kv name` and `-kvprefix prefix` are provided for syncing one or more kvs.

### Events

To watch events from target and fire them to source consul, use `-event name` flag.

### Hybrid

All of services, kvs and events support zero or more options and hybrid usage.
