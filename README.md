# consul-syncer

Consul-Syncer is a tool that synchronizes services, key-value stores, and events from one Consul instance to another. 

## Installation

To install, ensure that Go version 1.16 or above is installed and run the command:

    $ go install github.com/zitudu/consul-syncer/cmd/consul-syncer

## Quick Start

### Services

To sync services by name:

`consul-syncer -source-addr=127.0.0.1:8500 -target-addr=127.0.0.1:8501 -service-name a -service-name b -service-name c`

This will sync services a, b, and c. To sync services by tags, use the `-service-tag` flag.

Regular expressions are also supported by prefixing with "re:", e.g., `-service-name re:^web-.* -service-tag tag1 -service-tag re:tag[23]` will watch and sync all services with names prefixed with "web-" and tagged with "tag1" and one of "tag2".

### KVs

`-kv name` and `-kvprefix prefix` are provided for syncing one or more kvs.

### Events

To watch events from target and fire them to source consul, use `-event name` flag.

### Hybrid

All of services, kvs and events support zero or more options and hybrid usage.

## How doest it work?

Consul-Syncer works by registering, firing, and putting services, key-value stores, and events to the target Consul instance, collecting them from the source Consul instance through [Consul watches](https://www.consul.io/docs/dynamic-app-config/watches).

For services, Consul-Syncer watches the "services" endpoint, which detects all services registered on the source Consul instance, and then watches each single service respectively. Any changes in the service list on the target Consul instance will cause Consul-Syncer to adjust the watching list. Each watch plan of services registers its corresponding service to the target Consul instance via the [catalog's register-entity API](https://www.consul.io/api-docs/catalog#register-entity) which keeps the node of the service the same as that on the source Consul instance and deregisters it when it is unwatched.

For events and key-value stores, a watch plan is launched for each one, and the data it receives is fired or put into the target Consul instance.

### Clean up

Consul-Syncer also provides a clean-up feature that deregisters services it synchronized from the source Consul instance. To make this process easier, Consul-Syncer attaches a special meta (`"consul-syncer-external": "true"`) to nodes of services it synchronized and deregisters all services of nodes with that meta information. Note that this behavior may cause conflicts if nodes register services to both the source and target Consul instances simultaneously.

## License

MPL-2.0 License
