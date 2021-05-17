package syncer

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/api/watch"
	"github.com/hashicorp/go-hclog"
)

type watcher func(ctx context.Context, s *Syncer) error

func noopWatcher(_ctx context.Context, _s *Syncer) error {
	return nil
}

var watches = map[string]func(*Config) (watcher, error){
	"service": watchServices,
	"kv":      watchKVs,
	"event":   watchEvents,
}

type watchPlans struct {
	name string
	m    map[string]*consulwatch.Plan
	wg   sync.WaitGroup

	mux    sync.Mutex
	c      *consulapi.Client
	logger hclog.Logger
	stopCh chan struct{}
	stop   bool
}

func newWatchPlans(name string, c *consulapi.Client, logger hclog.Logger) *watchPlans {
	return &watchPlans{
		name:   name,
		m:      make(map[string]*consulwatch.Plan),
		c:      c,
		logger: logger,
		stopCh: make(chan struct{}),
	}
}

func (wps *watchPlans) Exists(key string) bool {
	wps.mux.Lock()
	defer wps.mux.Unlock()
	_, ok := wps.m[key]
	return ok
}

func (wps *watchPlans) Add(key string, params map[string]interface{}, handler consulwatch.HybridHandlerFunc) error {
	wps.mux.Lock()
	defer wps.mux.Unlock()
	wp, err := consulwatch.Parse(params)
	if err != nil {
		wps.logger.Error(fmt.Sprintf("%s '%s' watch plan error: %v", wps.name, key, err))
		return err
	}
	wp.HybridHandler = handler
	wps.m[key] = wp
	wps.wg.Add(1)
	go func() {
		if err := wp.RunWithClientAndHclog(wps.c, wps.logger); err != nil {
			wps.logger.Warn(fmt.Sprintf("%s '%s' watch stop with error: %v", wps.name, key, err))
		}
		wps.wg.Done()
	}()
	return nil
}

func (wps *watchPlans) RemoveOthers(keys []string) (removed []string) {
	if len(keys) < 100 {
		for k := range wps.m {
			var found bool
			for _, k2 := range keys {
				if k == k2 {
					found = true
					break
				}
			}
			if !found {
				delete(wps.m, k)
				removed = append(removed, k)
			}
		}
	} else {
		km := make(map[string]bool, len(keys))
		for _, k := range keys {
			km[k] = true
		}
		for k := range wps.m {
			if !km[k] {
				delete(wps.m, k)
				removed = append(removed, k)
			}
		}
	}
	return
}

func (wps *watchPlans) Wait(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			wps.Close()
			return
		case <-wps.stopCh:
			return
		}
	}
}

func (wps *watchPlans) Close() {
	wps.mux.Lock()
	defer wps.mux.Unlock()
	if wps.stop {
		return
	}
	for k, wp := range wps.m {
		if wp.IsStopped() {
			continue
		}
		wps.logger.Info(fmt.Sprintf("stopping %s '%s' watch", wps.name, k))
		wp.Stop()
		wps.logger.Info(fmt.Sprintf("stopped %s '%s' watch", wps.name, k))
	}
	wps.wg.Wait()
	close(wps.stopCh)
	wps.stop = true
}

func watchServices(c *Config) (watcher, error) {
	if len(c.ServiceNames)+len(c.ServiceTags) == 0 {
		return noopWatcher, nil
	}
	wp, err := consulwatch.Parse(map[string]interface{}{
		"type":  "services",
		"stale": c.QueryOptions.AllowStale,
	})
	if err != nil {
		return nil, err
	}
	var names, tags []*regexp.Regexp
	for _, s := range c.ServiceNames {
		r, err := compileStringOrRegexp(s)
		if err != nil {
			return nil, err
		}
		names = append(names, r)
	}
	for _, s := range c.ServiceTags {
		r, err := compileStringOrRegexp(s)
		if err != nil {
			return nil, err
		}
		tags = append(tags, r)
	}
	ch := make(chan []string, 10)
	wp.HybridHandler = func(bpv consulwatch.BlockingParamVal, val interface{}) {
		services := val.(map[string][]string)
		var out []string
	SERVICES_LOOP:
		for serviceName, ts := range services {
			for _, r := range names {
				if r.MatchString(serviceName) {
					out = append(out, serviceName)
					continue SERVICES_LOOP
				}
			}
			if len(tags) > 0 {
				b := true
			SERVICE_TAGS_LOOP:
				for _, tag := range ts {
					for _, r := range tags {
						if r.MatchString(tag) {
							continue SERVICE_TAGS_LOOP
						}
					}
					b = false
					break
				}
				if b {
					out = append(out, serviceName)
				}
			}
		}
		ch <- out
	}

	return func(ctx context.Context, s *Syncer) error {
		logger := s.logger
		wps := newWatchPlans("service", s.c, s.logger)
		defer wps.Close()

		go func() {
			for {
				var services []string
				select {
				case <-ctx.Done():
					return
				case services = <-ch:
				}
				for _, service := range services {
					service := service
					if !wps.Exists(service) {
						wps.Add(
							service,
							map[string]interface{}{
								"type":    "service",
								"service": service,
								"stale":   s.cfg.QueryOptions.AllowStale,
							},
							func(bpv consulwatch.BlockingParamVal, val interface{}) {
								entries := val.([]*consulapi.ServiceEntry)
								if err := s.syncService(entries); err != nil {
									logger.Error(fmt.Sprintf("service '%s' sync error: %v", service, err))
								}
							},
						)
					}
				}
				s.syncServicesDeregister(wps.RemoveOthers(services))
			}
		}()

		go func() {
			wps.Wait(ctx)
			wp.Stop()
		}()

		return wp.RunWithClientAndHclog(s.c, s.logger)
	}, nil
}

func watchKVs(c *Config) (watcher, error) {
	if len(c.KVs)+len(c.KVPrefixs) == 0 {
		return noopWatcher, nil
	}
	return func(ctx context.Context, s *Syncer) error {
		wps := newWatchPlans("key/keyprefix", s.c, s.logger)
		defer wps.Close()
		handler := func(k string) consulwatch.HybridHandlerFunc {
			return func(bpv consulwatch.BlockingParamVal, val interface{}) {
				switch v := val.(type) {
				case nil:
					s.logger.Info("kv/kvprefix got nil")
				case *consulapi.KVPair:
					if err := s.syncKVs([]*consulapi.KVPair{v}); err != nil {
						s.logger.Error(fmt.Sprintf("key '%s' sync error: %v", k, err))
					}
				case consulapi.KVPairs:
					if err := s.syncKVs(v); err != nil {
						s.logger.Error(fmt.Sprintf("keyprefix '%s' sync error: %v", k, err))
					}
				default:
					s.logger.Error(fmt.Sprintf("unexpected watch value of key/keyprefix: %v", v))
					return
				}
			}
		}
		for _, k := range c.KVs {
			if err := wps.Add(k, map[string]interface{}{
				"type":  "key",
				"key":   k,
				"stale": s.cfg.QueryOptions.AllowStale,
			}, handler(k)); err != nil {
				return err
			}
		}
		for _, k := range c.KVPrefixs {
			if err := wps.Add(k, map[string]interface{}{
				"type":   "keyprefix",
				"prefix": k,
				"stale":  s.cfg.QueryOptions.AllowStale,
			}, handler(k)); err != nil {
				return err
			}
		}
		wps.Wait(ctx)
		return nil
	}, nil
}

func watchEvents(c *Config) (watcher, error) {
	if len(c.Events) == 0 {
		return noopWatcher, nil
	}
	return func(ctx context.Context, s *Syncer) error {
		wps := newWatchPlans("event", s.c, s.logger)
		defer wps.Close()
		for _, event := range c.Events {
			event := event
			err := wps.Add(
				event,
				map[string]interface{}{
					"type": "event",
					"name": event,
				},
				func(bpv consulwatch.BlockingParamVal, val interface{}) {
					evts := val.([]*consulapi.UserEvent)
					if err := s.syncEvents(evts); err != nil {
						s.logger.Error(fmt.Sprintf("event '%s' sync error: %v", event, err))
					}
				},
			)
			if err != nil {
				return err
			}
		}
		wps.Wait(ctx)
		return nil
	}, nil
}

func compileStringOrRegexp(s string) (*regexp.Regexp, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "re:") {
		s = strings.TrimPrefix(s, "re:")
	} else {
		s = "^" + s + "$"
	}
	return regexp.Compile(s)
}
