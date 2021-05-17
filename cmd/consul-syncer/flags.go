package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
)

type arrayFlags []string

func (af *arrayFlags) String() string {
	return ""
}

func (af *arrayFlags) Set(value string) error {
	if value == "" {
		return nil
	}
	*af = append(*af, value)
	return nil
}

type loggerLevel hclog.Level

func (ll *loggerLevel) String() string {
	return "info"
}

func (ll *loggerLevel) Set(value string) error {
	switch strings.ToLower(value) {
	case "no", "none":
		*ll = loggerLevel(hclog.NoLevel)
	case "trace":
		*ll = loggerLevel(hclog.Trace)
	case "debug":
		*ll = loggerLevel(hclog.Debug)
	case "info":
		*ll = loggerLevel(hclog.Info)
	case "warn":
		*ll = loggerLevel(hclog.Warn)
	case "error":
		*ll = loggerLevel(hclog.Error)
	default:
		return errors.New("log level must be one of none, trace, debug, info, warn and error")
	}
	return nil
}

func init() {
	clientConfig := func(prefix, name string, c *consulapi.Config) {
		if prefix != "" {
			prefix = prefix + "-"
		}
		flag.StringVar(&c.Address, prefix+"addr", "", fmt.Sprintf("Address of %s consul client", name))
		flag.StringVar(&c.Scheme, prefix+"schema", c.Scheme, fmt.Sprintf("Schema of %s consul client", name))
		flag.StringVar(&c.Datacenter, prefix+"dc", c.Datacenter, fmt.Sprintf("Datacenter of %s consul client", name))
		flag.StringVar(&c.Token, prefix+"token", c.Token, fmt.Sprintf("Token of %s consul client", name))
		flag.StringVar(&c.TokenFile, prefix+"token-file", c.Token, fmt.Sprintf("Tokenfile of %s consul client", name))
		flag.StringVar(&c.Namespace, prefix+"namespace", c.Token, fmt.Sprintf("Namespace of %s consul client", name))
		// TODO(zitudu): TLSConfig
	}
	clientConfig("source", "source", cfg.Source)
	clientConfig("target", "target", cfg.Target)

	var serviceNames, serviceTags, kvs, kvprefixs, events arrayFlags
	flag.Var(&serviceNames, "service-name", "Services to sync, regexp")
	flag.Var(&serviceTags, "service-tag", "Services with tags to sync, regexp")
	flag.Var(&kvs, "kv", "KVs to sync")
	flag.Var(&kvprefixs, "kv-prefix", "KVs prefixed to sync")
	flag.Var(&events, "event", "Events to sync")
	flag.BoolVar(&cfg.QueryOptions.AllowStale, "stale", true, "Stale allows query from followers")

	var logLevel loggerLevel
	flag.Var(&logLevel, "log-level", "Log level: none, trace, debug, info, warn, error")

	logOut := flag.String("log-out", "stderr", "Log out: stdout, stderr, or any file path")
	flag.BoolVar(&cfg.LoggerOptions.JSONFormat, "log-json", false, "Control if the output should be in JSON")
	flag.BoolVar(&cfg.LoggerOptions.IncludeLocation, "log-location", false, "Include file and line information in each log line")
	flag.StringVar(&cfg.LoggerOptions.TimeFormat, "log-timeformat", cfg.LoggerOptions.TimeFormat, "Log timeformat")

	flag.Parse()

	cfg.ServiceNames = serviceNames
	cfg.ServiceTags = serviceTags
	cfg.KVs = kvs
	cfg.KVPrefixs = kvprefixs
	cfg.Events = events
	cfg.LoggerOptions.Level = hclog.Level(logLevel)
	*logOut = strings.ToLower(*logOut)
	switch *logOut {
	case "stdout":
		cfg.LoggerOptions.Output = os.Stdout
	case "stderr":
		cfg.LoggerOptions.Output = os.Stderr
	default:
		f, err := os.OpenFile(*logOut, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			panic(err)
		}
		cfg.LoggerOptions.Output = f
	}

	if cfg.Source.Address == "" || cfg.Target.Address == "" {
		panic("source, target address required")
	}
	if cfg.Source.Address == cfg.Target.Address {
		panic("source, target consul must be two consuls")
	}
}
