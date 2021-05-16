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
	Source, Target *consulapi.Config
	QueryOptions   consulapi.QueryOptions
	WriteOptions   consulapi.WriteOptions
	LoggerOptions  hclog.LoggerOptions
	ServiceNames   []string
	ServiceTags    []string
	KVs            []string
	KVPrefixs      []string
	Events         []string
	Check          bool
}

func NewConfig() *Config {
	return &Config{
		Source:        consulapi.DefaultConfig(),
		Target:        consulapi.DefaultConfig(),
		LoggerOptions: *hclog.DefaultOptions,
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
		e := s.cleanNodeServices()
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

func (s *Syncer) syncService(name string, entries []*consulapi.ServiceEntry) error {
	agent := s.cTarget.Agent()
	for i, entry := range entries {
		if i == 0 {
			if err := agent.ServiceDeregister(entry.Service.ID); err != nil {
				return err
			}
		}
		opts := &consulapi.AgentServiceRegistration{
			Kind:              entry.Service.Kind,
			ID:                entry.Service.ID,
			Name:              name,
			Tags:              entry.Service.Tags,
			Port:              entry.Service.Port,
			Address:           entry.Service.Address,
			TaggedAddresses:   entry.Service.TaggedAddresses,
			EnableTagOverride: entry.Service.EnableTagOverride,
			Meta:              entry.Service.Meta,
			Weights:           &entry.Service.Weights,
			Proxy:             entry.Service.Proxy,
			Connect:           entry.Service.Connect,
			Namespace:         entry.Service.Namespace,
		}
		if s.cfg.Check {
			opts.Check = entry.Service.Connect.SidecarService.Check
			opts.Checks = entry.Service.Connect.SidecarService.Checks
		}
		if err := agent.ServiceRegister(opts); err != nil {
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
	agent := s.cTarget.Agent()
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
			if err := agent.ServiceDeregister(service.ServiceID); err != nil {
				s.logger.Error(fmt.Sprintf("degregister %s service (id: %s) error: %v", name, service.ServiceID, err))
				if e == nil {
					e = err
				}
			}
		}
	}
	return e
}

func (s *Syncer) cleanNodeServices() error {
	agent := s.cTarget.Agent()
	nodeName, err := agent.NodeName()
	if err != nil {
		s.logger.Error(fmt.Sprintf("clean node name error: %v", err))
		return err
	}
	nodeServicesList, _, err := s.cTarget.Catalog().NodeServiceList(nodeName, &s.cfg.QueryOptions)
	if err != nil {
		s.logger.Error(fmt.Sprintf("clean node services list error: %v", err))
		return err
	}
	s.logger.Info(fmt.Sprintf("clean node %d service", len(nodeServicesList.Services)))
	var e error
	for _, service := range nodeServicesList.Services {
		if err := agent.ServiceDeregister(service.ID); err != nil {
			s.logger.Error(fmt.Sprintf("clean node service %s error: %v", service.ID, err))
			if e == nil {
				e = err
			}
		}
	}
	return e
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
