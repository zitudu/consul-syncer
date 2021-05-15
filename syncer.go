package syncer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/antonmedv/expr"
	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/api/watch"
	"github.com/hashicorp/go-hclog"
)

type Config struct {
	Source, Target   *consulapi.Config
	RegisterServices []string
	ServiceNames     []string
	ServiceTags      []string
	KVs              []string
	KVPrefixs        []string
	Events           []string
	ExprOptions      []expr.Option
	Check            bool
	QueryOptions     consulapi.QueryOptions
	WriteOptions     consulapi.WriteOptions
}

func NewConfig() Config {
	return Config{}
}

type Syncer struct {
	cfg        Config
	wp         *consulwatch.Plan
	c, cTarget *consulapi.Client
	logger     hclog.Logger
}

func NewSyncer(cfg Config) (*Syncer, error) {
	wp, err := consulwatch.Parse(map[string]interface{}{
		"type": "services",
	})
	if err != nil {
		return nil, err
	}
	wp.HybridHandler = func(bpv consulwatch.BlockingParamVal, val interface{}) {
		switch val.(type) {
		}
	}
	return nil, nil
}

func (s *Syncer) watch() error {
	if s == nil {
		return nil
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
