package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-kratos/kratos/v2/internal/endpoint"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"

	"github.com/go-kratos/aegis/subset"
	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/resolver"
)

type discoveryResolver struct {
	w  registry.Watcher
	cc resolver.ClientConn

	ctx    context.Context
	cancel context.CancelFunc

	insecure         bool
	debugLogDisabled bool
	selecterKey      string
	subsetSize       int
}

func (r *discoveryResolver) watch() {
	for {
		select {
		case <-r.ctx.Done():
			return
		default:
		}
		ins, err := r.w.Next()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Errorf("[resolver] Failed to watch discovery endpoint: %v", err)
			time.Sleep(time.Second)
			continue
		}
		r.update(ins)
	}
}

func (r *discoveryResolver) update(ins []*registry.ServiceInstance) {
	addrs := make([]resolver.Address, 0)
	endpoints := make(map[string]struct{})
	filtered := make([]*registry.ServiceInstance, 0, len(ins))
	for _, in := range ins {
		ept, err := endpoint.ParseEndpoint(in.Endpoints, endpoint.Scheme("grpc", !r.insecure))
		if err != nil {
			log.Errorf("[resolver] Failed to parse discovery endpoint: %v", err)
			continue
		}
		if ept == "" {
			continue
		}
		// filter redundant endpoints
		if _, ok := endpoints[ept]; ok {
			continue
		}
		filtered = append(filtered, in)
	}
	if r.subsetSize != 0 {
		filtered = subset.Subset(r.selecterKey, filtered, r.subsetSize)
	}
	for _, in := range filtered {
		ept, _ := endpoint.ParseEndpoint(in.Endpoints, endpoint.Scheme("grpc", !r.insecure))
		endpoints[ept] = struct{}{}
		addr := resolver.Address{
			ServerName: in.Name,
			Attributes: parseAttributes(in.Metadata),
			Addr:       ept,
		}
		addr.Attributes = addr.Attributes.WithValue("rawServiceInstance", in)
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		log.Warnf("[resolver] Zero endpoint found,refused to write, instances: %v", ins)
		return
	}
	err := r.cc.UpdateState(resolver.State{Addresses: addrs})
	if err != nil {
		log.Errorf("[resolver] failed to update state: %s", err)
	}

	if !r.debugLogDisabled {
		b, _ := json.Marshal(filtered)
		log.Infof("[resolver] update instances: %s", b)
	}
}

func (r *discoveryResolver) Close() {
	r.cancel()
	err := r.w.Stop()
	if err != nil {
		log.Errorf("[resolver] failed to watch top: %s", err)
	}
}

func (r *discoveryResolver) ResolveNow(options resolver.ResolveNowOptions) {}

func parseAttributes(md map[string]string) *attributes.Attributes {
	var a *attributes.Attributes
	for k, v := range md {
		if a == nil {
			a = attributes.New(k, v)
		} else {
			a = a.WithValue(k, v)
		}
	}
	return a
}
