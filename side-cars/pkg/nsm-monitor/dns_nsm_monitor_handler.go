package nsmmonitor

import (
	"fmt"
	"strings"

	"github.com/networkservicemesh/networkservicemesh/utils"
	"github.com/networkservicemesh/networkservicemesh/utils/caddyfile"
	"github.com/networkservicemesh/networkservicemesh/utils/dnsconfig"

	"github.com/sirupsen/logrus"

	"github.com/networkservicemesh/networkservicemesh/controlplane/api/connection"
	caddyfile_utils "github.com/networkservicemesh/networkservicemesh/utils/caddyfile"
)

const dnsPeersAsHostsEnv = "DNS_PEERS_AS_HOSTS"

type nsmConnPeer struct {
	serviceName    string
	serviceDomain  string
	serviceDstAddr string
}

//nsmDNSMonitorHandler implements Handler interface for handling dnsConfigs
type nsmDNSMonitorHandler struct {
	EmptyNSMMonitorHandler
	manager  dnsconfig.Manager
	reloadOp utils.Operation
	path     string

	nsmConnPeers map[string]nsmConnPeer
}

//NewNsmDNSMonitorHandler creates new DNS monitor handler
func NewNsmDNSMonitorHandler() Handler {
	p := caddyfile.Path()
	mgr, err := dnsconfig.NewManagerFromCaddyfile(p)
	if err != nil {
		logrus.Fatalf("An error during parse corefile: %v", err)
	}
	m := &nsmDNSMonitorHandler{
		manager:      mgr,
		path:         p,
		nsmConnPeers: map[string]nsmConnPeer{},
	}

	dnsPeersAsHosts := utils.EnvVar(dnsPeersAsHostsEnv).GetBooleanOrDefault(false)
	m.reloadOp = utils.NewSingleAsyncOperation(func() {
		c := m.manager.Caddyfile(m.path)

		if dnsPeersAsHosts {
			logrus.Debugf("writing nsm connection peers to Corefile: %v", m.nsmConnPeers)
			for _, p := range m.nsmConnPeers {
				updatePeerDnsHosts(c, p)
			}
			logrus.Debugf("updated Corefile: %v", c.String())
		}

		err := c.Save()

		if err != nil {
			logrus.Error(err)
		}
	})
	return m
}

func updatePeerDnsHosts(c caddyfile_utils.Caddyfile, peer nsmConnPeer) {
	updatePeerDnsHostsHelper(c, peer, false)
	updatePeerDnsHostsHelper(c, peer, true)
}

func updatePeerDnsHostsHelper(c caddyfile_utils.Caddyfile, peer nsmConnPeer, addSuffix bool) {

	connPeerName := peer.serviceName
	if addSuffix {
		connPeerName = fmt.Sprintf("%s.nsm", peer.serviceName)
	}
	connPeerAddr := strings.Split(peer.serviceDstAddr, "/")[0]

	var peerScope caddyfile_utils.Scope = nil
	if !c.HasScope(connPeerName) {
		peerScope = c.WriteScope(connPeerName).Write("reload 2s").Write("log")
	} else {
		peerScope = c.GetOrCreate(connPeerName)
	}

	var hostsScope caddyfile_utils.Scope = nil
	if !peerScope.HasScope("hosts") {
		hostsScope = peerScope.WriteScope("hosts")
		//hostsScope.Write("no_recursive")
		//hostsScope.Write("fallthrough")
	} else {
		hostsScope = peerScope.GetOrCreate("hosts")
	}

	record := fmt.Sprintf("%s   %s", connPeerAddr, connPeerName)
	hostsScope.Write(record)
}

func (m *nsmDNSMonitorHandler) Updated(old, new *connection.Connection) {
	logrus.Infof("Deleting config with id %v", old.Id)
	m.manager.Delete(old.Id)
	logrus.Infof("Adding config with id %v", new.Id)
	m.manager.Store(new.Id, new.GetContext().GetDnsContext().GetConfigs()...)

	netSvcName, nsmAddress := parseNetSvc(new.NetworkService)

	delete(m.nsmConnPeers, old.Id)
	peer := nsmConnPeer{
		//serviceName:    new.NetworkService,
		serviceName:    netSvcName,
		serviceDomain:  nsmAddress,
		serviceDstAddr: new.Context.IpContext.DstIpAddr,
	}
	logrus.Debugf("Updating nsm connection peer with %v: %v", new.Id, peer)
	m.nsmConnPeers[new.Id] = peer

	m.reloadOp.Run()
}

func (m *nsmDNSMonitorHandler) Connected(conns map[string]*connection.Connection) {
	for _, conn := range conns {
		logrus.Info(conn.Context.DnsContext)
		m.reloadOp.Run()
		m.manager.Store(conn.Id, conn.GetContext().GetDnsContext().GetConfigs()...)

		netSvcName, nsmAddress := parseNetSvc(conn.NetworkService)

		peer := nsmConnPeer{
			//serviceName:    conn.NetworkService,
			serviceName:    netSvcName,
			serviceDomain:  nsmAddress,
			serviceDstAddr: conn.Context.IpContext.DstIpAddr,
		}
		logrus.Debugf("Adding nsm connection peer with %v: %v", conn.Id, peer)
		m.nsmConnPeers[conn.Id] = peer
	}
	m.reloadOp.Run()
}

func (m *nsmDNSMonitorHandler) Closed(conn *connection.Connection) {
	logrus.Infof("Deleting config with id %v", conn.Id)
	m.manager.Delete(conn.Id)

	logrus.Debugf("Deleting nsm connection peer with %v", conn.Id)
	delete(m.nsmConnPeers, conn.Id)

	m.reloadOp.Run()
}

func parseNetSvc(netSvcUrl string) (netSvcName string, nsmAddress string) {
	if !strings.Contains(netSvcUrl, "@") {
		return netSvcUrl, ""
	}

	t := strings.SplitN(netSvcUrl, "@", 2)
	return t[0], t[1]
}
