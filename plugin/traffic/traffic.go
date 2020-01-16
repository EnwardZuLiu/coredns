package traffic

import (
	"context"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/dnsutil"
	"github.com/coredns/coredns/plugin/traffic/xds"
	"github.com/coredns/coredns/request"

	"github.com/miekg/dns"
)

// Traffic is a plugin that load balances according to assignments.
type Traffic struct {
	c    *xds.Client
	id   string
	Next plugin.Handler
}

// New returns a pointer to a new and initialized Traffic.
func New(addr, node string) (*Traffic, error) {
	c, err := xds.New(":18000", "mycoredns")
	if err != nil {
		return nil, err
	}

	return &Traffic{c: c}, nil
}

func (t *Traffic) Close() {
	t.c.Close()
}

// ServeDNS implements the plugin.Handler interface.
func (t *Traffic) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{Req: r, W: w}

	cluster, _ := dnsutil.TrimZone(state.Name(), "example.org")
	addr := t.c.Select(cluster)
	if addr == nil {
		return plugin.NextOrFailure(t.Name(), t.Next, ctx, w, r)
	}

	log.Debugf("Found address %q for %q", addr, cluster)

	// assemble reply
	m := new(dns.Msg)
	m.SetReply(r)

	m.Answer = []dns.RR{&dns.A{
		dns.RR_Header{Name: state.QName(), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5},
		addr,
	}}

	w.WriteMsg(m)
	return 0, nil
}

// Name implements the plugin.Handler interface.
func (t *Traffic) Name() string { return "traffic" }
