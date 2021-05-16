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

func (s *Syncer) Sync(ctx context.Context) error {
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
		s.logger.Info("init watch '%s'", k)
		w, err := f(&s.cfg)
		if err != nil {
			s.logger.Error("init watch '%s' error: %v", k, err)
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
				s.logger.Error("watch '%s' error: %v", k, err)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	for err := range errCh {
		return err
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
