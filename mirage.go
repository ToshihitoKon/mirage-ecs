package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	api "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	core "github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/api/v2/endpoint"
	listener "github.com/envoyproxy/go-control-plane/envoy/api/v2/listener"
	route "github.com/envoyproxy/go-control-plane/envoy/api/v2/route"
	http_connection_manager "github.com/envoyproxy/go-control-plane/envoy/config/filter/network/http_connection_manager/v2"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v2"
	xds "github.com/envoyproxy/go-control-plane/pkg/server/v2"
	"github.com/golang/protobuf/ptypes/duration"
	"google.golang.org/grpc"
	any "google.golang.org/protobuf/types/known/anypb"
)

var app *Mirage

type Mirage struct {
	Config       *Config
	WebApi       *WebApi
	ReverseProxy *ReverseProxy
	ECS          *ECS
}

func Setup(cfg *Config) {
	m := &Mirage{
		Config:       cfg,
		WebApi:       NewWebApi(cfg),
		ReverseProxy: NewReverseProxy(cfg),
		ECS:          NewECS(cfg),
	}

	app = m
}

func Run() {
	// launch server
	var wg sync.WaitGroup
	for _, v := range app.Config.Listen.HTTP {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			laddr := fmt.Sprintf("%s:%d", app.Config.Listen.ForeignAddress, port)
			listener, err := net.Listen("tcp", laddr)
			if err != nil {
				log.Printf("[error] cannot listen %s", laddr)
				return
			}

			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
				app.ServeHTTPWithPort(w, req, port)
			})

			log.Println("[info] listen port:", port)
			http.Serve(listener, mux)
		}(v.ListenPort)

		// xds api
		go func() {
			defer wg.Done()
			snapshotCache := cache.NewSnapshotCache(false, cache.IDHash{}, nil)
			// NodeHashで返ってくるハッシュ値とその設定のスナップショットをキャッシュとして覚える
			err := snapshotCache.SetSnapshot("cluster.local/node0", defaultSnapshot())
			if err != nil {
				log.Println("xds: ", err.Error())
				return
			}
			server := xds.NewServer(context.Background(), snapshotCache, nil)
			grpcServer := grpc.NewServer()
			lis, _ := net.Listen("tcp", ":5000")

			discovery.RegisterAggregatedDiscoveryServiceServer(grpcServer, server)
			api.RegisterEndpointDiscoveryServiceServer(grpcServer, server)
			log.Println("[info] listen port:", 5000)
			if err := grpcServer.Serve(lis); err != nil {
				// error handling
				log.Printf("error: xds api %s", err.Error())
			}
		}()
	}

	log.Println("[info] Launch succeeded!")

	wg.Wait()
}

func (m *Mirage) ServeHTTPWithPort(w http.ResponseWriter, req *http.Request, port int) {
	host := strings.ToLower(strings.Split(req.Host, ":")[0])

	switch {
	case m.isWebApiHost(host):
		m.WebApi.ServeHTTP(w, req)

	case m.isDockerHost(host):
		m.ReverseProxy.ServeHTTPWithPort(w, req, port)

	default:
		if req.URL.Path == "/" {
			// otherwise root returns 200 (for healthcheck)
			http.Error(w, "mirage-ecs", http.StatusOK)
		} else {
			// return 404
			log.Printf("[warn] host %s is not found", host)
			http.NotFound(w, req)
		}
	}

}

func (m *Mirage) isDockerHost(host string) bool {
	if strings.HasSuffix(host, m.Config.Host.ReverseProxySuffix) {
		subdomain := strings.ToLower(strings.Split(host, ".")[0])
		return m.ReverseProxy.Exists(subdomain)
	}

	return false
}

func (m *Mirage) isWebApiHost(host string) bool {
	return isSameHost(m.Config.Host.WebApi, host)
}

func isSameHost(s1 string, s2 string) bool {
	lower1 := strings.Trim(strings.ToLower(s1), " ")
	lower2 := strings.Trim(strings.ToLower(s2), " ")

	return lower1 == lower2
}

// xds test

// NodeHash interfaceの実装。Envoyの識別子から文字列をかえすハッシュ関数を実装する。
type hash struct{}

func (hash) ID(node *core.Node) string {
	if node == nil {
		return "unknown"
	}
	return node.Cluster + "/" + node.Id
}

// スナップショットを返す。構造体の形はProtocol Bufferの定義と同じ。
// envoy.yamlで
func defaultSnapshot() cache.Snapshot {
	listeners := []types.Resource{
		&api.Listener{
			Name: "listener_1",
			Address: &core.Address{
				Address: &core.Address_SocketAddress{
					SocketAddress: &core.SocketAddress{
						Address: "0.0.0.0",
						PortSpecifier: &core.SocketAddress_PortValue{
							PortValue: 10000,
						},
					},
				},
			},
			FilterChains: []*listener.FilterChain{
				&listener.FilterChain{
					Filters: []*listener.Filter{
						&listener.Filter{
							Name: "envoy.filters.network.http_connection_manager",
							ConfigType: &listener.Filter_TypedConfig{
								TypedConfig: func() *any.Any {
									hcm := &http_connection_manager.HttpConnectionManager{
										StatPrefix: "ingress_http",
										CodecType:  http_connection_manager.HttpConnectionManager_AUTO,
										RouteSpecifier: &http_connection_manager.HttpConnectionManager_RouteConfig{
											RouteConfig: &api.RouteConfiguration{
												Name: "local_route",
												VirtualHosts: []*route.VirtualHost{
													&route.VirtualHost{
														Name:    "test",
														Domains: []string{"*"},
														Routes: []*route.Route{
															&route.Route{
																Match: &route.RouteMatch{
																	PathSpecifier: &route.RouteMatch_Prefix{"/"},
																},
																Action: &route.Route_Route{
																	Route: &route.RouteAction{
																		ClusterSpecifier: &route.RouteAction_Cluster{
																			Cluster: "test",
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
										HttpFilters: []*http_connection_manager.HttpFilter{
											&http_connection_manager.HttpFilter{
												Name: "envoy.filters.http.router",
											},
										},
									}
									message := hcm.ProtoReflect()
									ret, err := any.New(message)
									if err != nil {
										log.Println(err.Error())
										return nil
									}
									return ret
								}(),
							},
						},
					},
				},
			},
		},
	}

	clusters := []types.Resource{
		&api.Cluster{
			Name:            "test",
			ConnectTimeout:  &duration.Duration{},
			DnsLookupFamily: api.Cluster_V4_ONLY,
			LbPolicy:        api.Cluster_ROUND_ROBIN,
			LoadAssignment: &api.ClusterLoadAssignment{
				ClusterName: "test",
				Endpoints: []*endpoint.LocalityLbEndpoints{
					&endpoint.LocalityLbEndpoints{
						LbEndpoints: []*endpoint.LbEndpoint{
							&endpoint.LbEndpoint{
								HostIdentifier: &endpoint.LbEndpoint_Endpoint{
									Endpoint: &endpoint.Endpoint{
										Address: &core.Address{
											Address: &core.Address_SocketAddress{
												SocketAddress: &core.SocketAddress{
													Address: "10.96.0.102",
													PortSpecifier: &core.SocketAddress_PortValue{
														PortValue: 80,
													},
													ResolverName: "test",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		&api.Cluster{
			Name:            "test2",
			ConnectTimeout:  &duration.Duration{},
			DnsLookupFamily: api.Cluster_V4_ONLY,
			LbPolicy:        api.Cluster_ROUND_ROBIN,
			LoadAssignment: &api.ClusterLoadAssignment{
				ClusterName: "test2",
				Endpoints: []*endpoint.LocalityLbEndpoints{
					&endpoint.LocalityLbEndpoints{
						LbEndpoints: []*endpoint.LbEndpoint{
							&endpoint.LbEndpoint{
								HostIdentifier: &endpoint.LbEndpoint_Endpoint{
									Endpoint: &endpoint.Endpoint{
										Address: &core.Address{
											Address: &core.Address_SocketAddress{
												SocketAddress: &core.SocketAddress{
													Address: "10.96.0.52",
													PortSpecifier: &core.SocketAddress_PortValue{
														PortValue: 80,
													},
													ResolverName: "test",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return cache.NewSnapshot("0.0", listeners, clusters, nil, nil, nil)
}
