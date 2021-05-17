package syncer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
)

type Config struct {
	Source, Target   *consulapi.Config
	QueryOptions     consulapi.QueryOptions
	WriteOptions     consulapi.WriteOptions
	LoggerOptions    hclog.LoggerOptions
	ServiceNames     []string
	ServiceTags      []string
	KVs              []string
	KVPrefixs        []string
	Events           []string
	Check            bool
	ExternalNodeMeta map[string]string
}

func NewConfig() *Config {
	return &Config{
		Source:        consulapi.DefaultConfig(),
		Target:        consulapi.DefaultConfig(),
		LoggerOptions: *hclog.DefaultOptions,
		ExternalNodeMeta: map[string]string{
			"consul-syncer-external": "true",
		},
	}
}

type Syncer struct {
	cfg        Config
	c, cTarget *consulapi.Client
	logger     hclog.Logger
}

func NewSyncer(cfg *Config) (*Syncer, error) {
	c, err := consulapi.NewClient(cfg.Source)
	if err != nil {
		return nil, err
	}
	cTarget, err := consulapi.NewClient(cfg.Target)
	if err != nil {
		return nil, err
	}
	return &Syncer{
		*cfg, c, cTarget, hclog.New(&cfg.LoggerOptions),
	}, nil
}

func (s *Syncer) Sync(ctx context.Context) (err error) {
	defer func() {
		e := s.cleanImportedServices()
		if err == nil {
			err = e
		}
	}()
	return s.watch(ctx)
}

func (s *Syncer) watch(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()
	watchers := make(map[string]watcher)
	for k, f := range watches {
		s.logger.Info(fmt.Sprintf("init watch '%s'", k))
		w, err := f(&s.cfg)
		if err != nil {
			s.logger.Error(fmt.Sprintf("init watch '%s' error: %v", k, err))
			return err
		}
		watchers[k] = w
	}
	var wg sync.WaitGroup
	wg.Add(len(watchers))
	errCh := make(chan error, len(watchers))
	defer close(errCh)
	for k, w := range watchers {
		k := k
		w := w
		go func() {
			if err := w(ctx, s); err != nil {
				s.logger.Error(fmt.Sprintf("watch '%s' error: %v", k, err))
			} else {
				s.logger.Info(fmt.Sprintf("watch '%s' stopped successfully", k))
			}
			wg.Done()
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	return nil
}

func (s *Syncer) syncService(entries []*consulapi.ServiceEntry) error {
	catalog := s.cTarget.Catalog()
	s.logger.Info(fmt.Sprintf("register %d services", len(entries)))
	for _, entry := range entries {
		optsDe := &consulapi.CatalogDeregistration{
			Node:       entry.Node.Node,
			Address:    entry.Node.Address,
			Datacenter: entry.Node.Datacenter,
			ServiceID:  entry.Service.ID,
			Namespace:  entry.Service.Namespace,
		}
		s.logger.Debug("catalog deregister", optsDe)
		if _, err := catalog.Deregister(optsDe, &s.cfg.WriteOptions); err != nil {
			return err
		}
		opts := &consulapi.CatalogRegistration{
			Node:            entry.Node.Node,
			Address:         entry.Node.Address,
			TaggedAddresses: entry.Node.TaggedAddresses,
			NodeMeta:        s.appendExternalNodeMeta(entry.Node.Meta),
			Datacenter:      entry.Node.Datacenter,
			Service:         entry.Service,
			SkipNodeUpdate:  false,
		}
		if s.cfg.Check {
			opts.Checks = entry.Checks
		}
		s.logger.Debug("catalog register", opts)
		if _, err := catalog.Register(opts, &s.cfg.WriteOptions); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) syncServicesDeregister(names []string) error {
	if len(names) == 0 {
		return nil
	}
	s.logger.Info(fmt.Sprintf("deregister %d services: %v", len(names), names))
	catalog := s.cTarget.Catalog()
	var e error
	for _, name := range names {
		services, _, err := catalog.Service(name, "", &s.cfg.QueryOptions)
		if err != nil {
			s.logger.Error(fmt.Sprintf("degregister %s, query service error: %v", name, err))
			if e == nil {
				e = err
			}
		}
		for _, service := range services {
			opts := &consulapi.CatalogDeregistration{
				Node:       service.Node,
				Address:    service.Address,
				Datacenter: service.Datacenter,
				ServiceID:  service.ServiceID,
				Namespace:  service.Namespace,
			}
			if s.cfg.Check && len(service.Checks) == 1 {
				opts.CheckID = service.Checks[0].CheckID
			}
			if _, err := catalog.Deregister(opts, &s.cfg.WriteOptions); err != nil {
				s.logger.Error(fmt.Sprintf("degregister %s service (id: %s) error: %v", name, service.ServiceID, err))
				if e == nil {
					e = err
				}
			}
		}
	}
	return e
}

func (s *Syncer) cleanImportedServices() error {
	s.logger.Info("cleaning imported services")
	catalog := s.cTarget.Catalog()
	q := s.cfg.QueryOptions
	q.NodeMeta = s.cfg.ExternalNodeMeta
	nodes, _, err := catalog.Nodes(&q)
	if err != nil {
		s.logger.Error(fmt.Sprintf("get node lists error: %v", err))
		return err
	}
	s.logger.Info(fmt.Sprintf("cleaning imported services: %d nodes", len(nodes)))
	var errCount int
	var e error
	for _, node := range nodes {
		s.logger.Info(fmt.Sprintf("cleaning imported services: node %s", node.Address))
		nodeServicesList, _, err := catalog.NodeServiceList(node.Address, &s.cfg.QueryOptions)
		if err != nil {
			s.logger.Error(fmt.Sprintf("clean node services list error: %v", err))
			return err
		}
		s.logger.Info(fmt.Sprintf("clean node %d service", len(nodeServicesList.Services)))
		for _, service := range nodeServicesList.Services {
			opts := &consulapi.CatalogDeregistration{
				Node:       nodeServicesList.Node.Node,
				Address:    nodeServicesList.Node.Address,
				Datacenter: nodeServicesList.Node.Datacenter,
				ServiceID:  service.ID,
				Namespace:  service.Namespace,
			}
			if _, err := catalog.Deregister(opts, &s.cfg.WriteOptions); err != nil {
				errCount += 1
				s.logger.Error(fmt.Sprintf("clean node service %s error: %v", service.ID, err))
				if e == nil {
					e = err
				}
			}
		}
	}
	if e != nil {
		return fmt.Errorf("%d errors: %v", errCount, e)
	}
	return nil
}

func (s *Syncer) syncKVs(pairs []*consulapi.KVPair) error {
	if len(pairs) == 1 {
		kv := s.cTarget.KV()
		_, err := kv.Put(pairs[0], &s.cfg.WriteOptions)
		return err
	}
	txn := s.cTarget.Txn()
	var opts []*consulapi.TxnOp
	for _, pair := range pairs {
		opts = append(opts, &consulapi.TxnOp{
			KV: &consulapi.KVTxnOp{
				Verb:      consulapi.KVSet,
				Key:       pair.Key,
				Value:     pair.Value,
				Flags:     pair.Flags,
				Index:     0,
				Session:   pair.Session,
				Namespace: pair.Namespace,
			},
		})
	}
	ok, resp, _, err := txn.Txn(opts, &s.cfg.QueryOptions)
	if err != nil {
		var sb strings.Builder
		for _, e := range resp.Errors {
			sb.WriteString(strconv.Itoa(e.OpIndex))
			sb.WriteRune(':')
			sb.WriteString(e.What)
		}
		return fmt.Errorf("%w: %s", err, sb.String())
	}
	if !ok {
		return errors.New("transaction failed")
	}
	return nil
}

func (s *Syncer) syncEvents(events []*consulapi.UserEvent) error {
	e := s.cTarget.Event()
	for _, event := range events {
		_, _, err := e.Fire(event, &s.cfg.WriteOptions)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) appendExternalNodeMeta(meta map[string]string) map[string]string {
	m := make(map[string]string, len(meta)+len(s.cfg.ExternalNodeMeta))
	for k, v := range meta {
		m[k] = v
	}
	for k, v := range s.cfg.ExternalNodeMeta {
		m[k] = v
	}
	return m
}
