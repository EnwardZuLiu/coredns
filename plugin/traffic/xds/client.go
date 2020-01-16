/*
This package contains code copied from github.com/grpc/grpc-co. The license for that code is:

Copyright 2019 gRPC authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package xds implements a bidirectional stream to an envoy ADS management endpoint. It will stream
// updates (CDS and EDS) from there to help load balance responses to DNS clients.
package xds

import (
	"context"
	"net"
	"time"

	clog "github.com/coredns/coredns/plugin/pkg/log"

	xdspb "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	corepb "github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	adsgrpc "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"
	"github.com/golang/protobuf/ptypes"
	"google.golang.org/grpc"
)

var log = clog.NewWithPlugin("traffic: xds")

const (
	cdsURL = "type.googleapis.com/envoy.api.v2.Cluster"
	edsURL = "type.googleapis.com/envoy.api.v2.ClusterLoadAssignment"
)

type adsStream adsgrpc.AggregatedDiscoveryService_StreamAggregatedResourcesClient

type Client struct {
	cc          *grpc.ClientConn
	ctx         context.Context
	assignments assignment
	node        *corepb.Node
	cancel      context.CancelFunc
	stop        chan struct{}
}

// New returns a new client that's dialed to addr using node as the local identifier.
func New(addr, node string) (*Client, error) {
	// todo credentials!
	opts := []grpc.DialOption{grpc.WithInsecure()}
	cc, err := grpc.Dial(addr, opts...)
	if err != nil {
		return nil, err
	}
	c := &Client{cc: cc, node: &corepb.Node{Id: "test-id"}} // do more with this node data? Hostname port??
	c.assignments = assignment{cla: make(map[string]*xdspb.ClusterLoadAssignment)}
	c.ctx, c.cancel = context.WithCancel(context.Background())

	return c, nil
}

func (c *Client) Close() { c.cancel(); c.cc.Close() }

func (c *Client) Run() (adsgrpc.AggregatedDiscoveryService_StreamAggregatedResourcesClient, error) {
	cli := adsgrpc.NewAggregatedDiscoveryServiceClient(c.cc)
	stream, err := cli.StreamAggregatedResources(c.ctx)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (c *Client) ClusterDiscovery(stream adsStream, version, nonce string, clusters []string) error {
	req := &xdspb.DiscoveryRequest{
		Node:          c.node,
		TypeUrl:       cdsURL,
		ResourceNames: clusters, // empty for all
		VersionInfo:   version,
		ResponseNonce: nonce,
	}
	return stream.Send(req)
}

func (c *Client) EndpointDiscovery(stream adsStream, version, nonce string, clusters []string) error {
	req := &xdspb.DiscoveryRequest{
		Node:          c.node,
		TypeUrl:       edsURL,
		ResourceNames: clusters,
		VersionInfo:   version,
		ResponseNonce: nonce,
	}
	return stream.Send(req)
}

func (c *Client) Receive(stream adsStream) error {
	for {
		resp, err := stream.Recv()
		if err != nil {
			log.Warningf("Trouble receiving from the gRPC connection: %s", err)
			time.Sleep(10 * time.Second) // better.
		}

		switch resp.GetTypeUrl() {
		case cdsURL:
			for _, r := range resp.GetResources() {
				var any ptypes.DynamicAny
				if err := ptypes.UnmarshalAny(r, &any); err != nil {
					continue
				}
				cluster, ok := any.Message.(*xdspb.Cluster)
				if !ok {
					continue
				}
				c.assignments.SetClusterLoadAssignment(cluster.GetName(), nil)
			}
			log.Debugf("Cluster discovery processed with %d resources", len(resp.GetResources()))
			// ack the CDS proto, with we we've got. (empty version would be NACK)
			if err := c.ClusterDiscovery(stream, resp.GetVersionInfo(), resp.GetNonce(), c.assignments.Clusters()); err != nil {
				log.Warningf("Failed to acknowledge cluster discovery: %s", err)
			}
			// need to figure out how to handle the versions and nounces exactly.

			// now kick off discovery for endpoints
			if err := c.EndpointDiscovery(stream, "", "", c.assignments.Clusters()); err != nil {
				log.Warningf("Failed to perform endpoint discovery: %s", err)
			}

		case edsURL:
			for _, r := range resp.GetResources() {
				var any ptypes.DynamicAny
				if err := ptypes.UnmarshalAny(r, &any); err != nil {
					log.Debugf("Failed to unmarshal endpoint discovery: %s", err)
					continue
				}
				cla, ok := any.Message.(*xdspb.ClusterLoadAssignment)
				if !ok {
					log.Debugf("Unexpected resource type: %T in endpoint discovery", any.Message)
					continue
				}
				c.assignments.SetClusterLoadAssignment(cla.GetClusterName(), cla)
				// ack the bloody thing
			}
			log.Debugf("Endpoint discovery processed with %d resources", len(resp.GetResources()))

		default:
			log.Warningf("Unknown response URL for discovery: %q", resp.GetTypeUrl())
			continue
		}
	}
}

// Select is a small wrapper. bla bla, keeps assigmens private.
func (c *Client) Select(cluster string) net.IP { return c.assignments.Select(cluster) }
