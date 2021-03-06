//
// Copyright (c) 2016 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package libsnnet

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/vishvananda/netlink"
)

// Cnci represents a Concentrator for a single tenant
// All subnets belonging to this tenant that are handled
// by this concentrator. A separate bridge will be setup
// for each subnet with its own dnsmasq service.
// Traffic is routable between tenant bridges
type Cnci struct {
	*NetworkConfig
	MgtAddr     []netlink.Addr //TODO: Remove this and just use the link
	MgtLink     []netlink.Link
	ComputeAddr []netlink.Addr //TODO: Remove this and just use the link
	ComputeLink []netlink.Link

	ID     string // UUID of the concentrator generated by the Controller
	Tenant string // UUID of the tenant

	//APITimeout specifies the amount of time the API will wait for netlink
	//operations to complete. When multiple go routines  invoke the API
	//simultaneously certain netlink calls suffer higher latencies
	APITimeout time.Duration

	// IPAddress of the concentrator that is routable
	// The UUID to IP mapping in this case has to be
	// performed using the datacenter DHCP
	IP net.IP

	// Public IPAddress this concentrator is assigned
	PublicIPs   []net.IP
	PublicIPMap map[string]net.IP //Key is public IPNet

	topology *cnciTopology
}

//Network topology of the node
type cnciTopology struct {
	sync.Mutex
	linkMap   map[string]*linkInfo //Alias to Link mapping
	nameMap   map[string]bool      //Link name
	bridgeMap map[string]*bridgeInfo
}

func newCnciTopology() *cnciTopology {
	return &cnciTopology{
		linkMap:   make(map[string]*linkInfo),
		nameMap:   make(map[string]bool),
		bridgeMap: make(map[string]*bridgeInfo),
	}
}

func reinitTopology(topology *cnciTopology) {
	topology.linkMap = make(map[string]*linkInfo)
	topology.nameMap = make(map[string]bool)
	topology.bridgeMap = make(map[string]*bridgeInfo)
}

type bridgeInfo struct {
	tunnels int
	*Dnsmasq
}

// Neighbor contains information about other CNCIs for this tenant.
type Neighbor struct {
	PhysicalIP string
	Subnet     string
	TunnelIP   string
	TunnelID   uint32
}

func enableForwarding() error {
	return nil
}

//Adds a physical link to the management or compute network
//if the link has an IP address the falls within one of the configured subnets
//However if the subnets are not specified just add the links
//It is the callers responsibility to pick the correct link
func (cnci *Cnci) addPhyLinkToConfig(link netlink.Link, ipv4Addrs []netlink.Addr) {

	for _, addr := range ipv4Addrs {

		if cnci.ManagementNet == nil {
			cnci.MgtAddr = append(cnci.MgtAddr, addr)
			cnci.MgtLink = append(cnci.MgtLink, link)
		} else {
			for _, mgt := range cnci.ManagementNet {
				if mgt.Contains(addr.IPNet.IP) {
					cnci.MgtAddr = append(cnci.MgtAddr, addr)
					cnci.MgtLink = append(cnci.MgtLink, link)
				}
			}
		}

		if cnci.ComputeNet == nil {
			cnci.ComputeAddr = append(cnci.ComputeAddr, addr)
			cnci.ComputeLink = append(cnci.ComputeLink, link)
		} else {
			for _, comp := range cnci.ComputeNet {
				if comp.Contains(addr.IPNet.IP) {
					cnci.ComputeAddr = append(cnci.ComputeAddr, addr)
					cnci.ComputeLink = append(cnci.ComputeLink, link)
				}
			}
		}
	}
}

//This will return error if it cannot find valid physical
//interfaces with IP addresses assigned
//This may be just a delay in acquiring IP addresses
func (cnci *Cnci) findPhyNwInterface() error {

	links, err := netlink.LinkList()
	if err != nil {
		return err
	}

	phyInterfaces := 0
	cnci.MgtAddr = nil
	cnci.MgtLink = nil
	cnci.ComputeAddr = nil
	cnci.ComputeLink = nil

	for _, link := range links {
		if !validPhysicalLink(link) {
			continue
		}

		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil || len(addrs) == 0 {
			continue //Ignore links with no IP addresses
		}

		phyInterfaces++
		cnci.addPhyLinkToConfig(link, addrs)

	}

	if len(cnci.MgtAddr) == 0 {
		return fmt.Errorf("unable to associate with management network %v", cnci.ManagementNet)
	}
	if len(cnci.ComputeAddr) == 0 {
		return fmt.Errorf("unable to associate with compute network %v", cnci.ComputeNet)
	}

	//Allow auto configuration only in the case where there is a single physical
	//interface with an IP address
	if (cnci.ManagementNet == nil || cnci.ComputeNet == nil) && phyInterfaces > 1 {
		return fmt.Errorf("unable to autoconfigure network")
	}

	return nil
}

// Init sets the CNCI configuration
// Discovers the physical interfaces and classifies them as management or compute
// Performs any node specific networking setup.
func (cnci *Cnci) Init() error {

	cnci.APITimeout = time.Second * 6

	if cnci.NetworkConfig == nil {
		return fmt.Errorf("CNCI uninitialized")
	}

	err := cnci.findPhyNwInterface()
	if err != nil {
		return err
	}

	cnci.topology = newCnciTopology()
	if err = cnci.RebuildTopology(); err != nil {
		return err
	}

	if err = enableForwarding(); err != nil {
		return err
	}
	return nil
}

func (cnci *Cnci) rebuildLinkAndNameMap(links []netlink.Link) {

	for _, link := range links {

		alias := link.Attrs().Alias
		name := link.Attrs().Name

		cnci.topology.nameMap[name] = true

		if alias == "" {
			continue
		}
		cnci.topology.linkMap[alias] = &linkInfo{
			index: link.Attrs().Index,
			name:  name,
			ready: make(chan struct{}),
		}
		close(cnci.topology.linkMap[alias].ready)
	}
}

func (cnci *Cnci) rebuildBridgeMap(links []netlink.Link) error {
	for _, link := range links {
		if link.Type() != "bridge" {
			continue
		}

		bridgeID := link.Attrs().Alias

		if !strings.HasPrefix(bridgeID, bridgePrefix) {
			continue
		}

		br, err := NewBridge(bridgeID)
		if err != nil {
			return (err)
		}

		if err = br.GetDevice(); err != nil {
			return (err)
		}

		subnet, err := stringToSubnet(strings.TrimPrefix(bridgeID, bridgePrefix))
		if err != nil {
			return (err)
		}

		dns, err := startDnsmasq(br, cnci.Tenant, *subnet)
		if err != nil {
			return (err)
		}

		cnci.topology.bridgeMap[bridgeID] = &bridgeInfo{
			Dnsmasq: dns,
		}
	}
	return nil
}

func (cnci *Cnci) verifyTopology(links []netlink.Link) error {
	for _, link := range links {
		if link.Type() != "gretap" {
			continue
		}

		gre := link.Attrs().Alias
		if !strings.HasPrefix(gre, grePrefix) {
			continue
		}

		subnetID := strings.TrimPrefix(strings.Split(gre, "##")[0], grePrefix)
		bridgeID := bridgePrefix + subnetID

		if _, ok := cnci.topology.linkMap[bridgeID]; !ok {
			return fmt.Errorf("missing bridge for gre tunnel %s", gre)
		}

		brInfo, ok := cnci.topology.bridgeMap[bridgeID]
		if !ok {
			return fmt.Errorf("missing bridge map for gre tunnel %s", gre)
		}
		brInfo.tunnels++
	}
	return nil
}

//RebuildTopology CNCI network database using the information contained
//in the aliases. It can be called if the agent using the library
//crashes and loses network topology information.
//It can also be called, to rebuild the network topology on demand.
//TODO: Restarting the DNS Masq here - Define a re-attach method
//TODO: Log failures when making best effort progress
func (cnci *Cnci) RebuildTopology() error {

	if cnci.NetworkConfig == nil || cnci.topology == nil {
		return fmt.Errorf("cnci not initialized")
	}

	links, err := netlink.LinkList()
	if err != nil {
		return err
	}

	cnci.topology.Lock()
	defer cnci.topology.Unlock()
	reinitTopology(cnci.topology)

	//Update the link and name map
	//Do this to ensure the link map is updated even on failure
	cnci.rebuildLinkAndNameMap(links)

	//Create the bridge map
	err = cnci.rebuildBridgeMap(links)
	if err != nil {
		return err
	}

	//Ensure that all tunnels have the associated bridges
	err = cnci.verifyTopology(links)
	return err
}

func subnetToString(subnet net.IPNet) string {
	return strings.Replace(subnet.String(), "/", "+", -1)
}

func stringToSubnet(subnet string) (*net.IPNet, error) {
	s := strings.Replace(subnet, "+", "/", -1)
	_, ipNet, err := net.ParseCIDR(s)
	return ipNet, err
}

func genBridgeAlias(subnet net.IPNet) string {
	return fmt.Sprintf("%s%s", bridgePrefix, subnetToString(subnet))
}

func genGreAlias(subnet net.IPNet, cnIP net.IP) string {
	return fmt.Sprintf("%s%s##%s", grePrefix, subnetToString(subnet), cnIP.String())
}

func genLinkName(device interface{}, nameMap map[string]bool) (string, error) {
	for i := 0; i < ifaceRetryLimit; {
		name, _ := genIface(device, false)
		if !nameMap[name] {
			nameMap[name] = true
			return name, nil
		}
	}
	return "", fmt.Errorf("Unable to generate unique device name")
}

func startDnsmasq(bridge *Bridge, tenant string, subnet net.IPNet) (*Dnsmasq, error) {
	dns, err := newDnsmasq(bridge.GlobalID, tenant, subnet, 0, bridge)
	if err != nil {
		return nil, fmt.Errorf("NewDnsmasq failed %v", err)
	}

	if _, err = dns.attach(); err != nil {
		err = dns.restart()
		if err != nil {
			return nil, fmt.Errorf("dns.start failed %v", err)
		}
	}
	return dns, nil
}

func createCnciBridge(bridge *Bridge, brInfo *bridgeInfo, tenant string, subnet net.IPNet) (err error) {
	if bridge == nil || brInfo == nil {
		return fmt.Errorf("nil pointer encountered bridge[%v] brInfo[%v]", bridge, brInfo)
	}
	if err = bridge.Create(); err != nil {
		return err
	}
	if err = bridge.Enable(); err != nil {
		return err
	}
	brInfo.Dnsmasq, err = startDnsmasq(bridge, tenant, subnet)
	return err
}

func createCnciTunnel(gre *GreTapEP) (err error) {
	if err = gre.create(); err != nil {
		return err
	}
	if err = gre.enable(); err != nil {
		return err
	}
	return nil
}

func checkInputParams(subnet net.IPNet, subnetKey int, cnIP net.IP) error {
	switch {
	case subnet.IP == nil:
		return fmt.Errorf("Invalid input parameters - Subnet IP")
	case subnet.Mask == nil:
		return fmt.Errorf("Invalid input parameters - Subnet Mask")
	case subnetKey == 0:
		return fmt.Errorf("Invalid input parameters - Subnet Key")
	case cnIP == nil:
		return fmt.Errorf("Invalid input parameters - CN IP")
	}
	return nil
}

//This function inserts the remote subnet in the topology
//If the function returns error the bridgeName can be ignored
//If the function does not return error and has a valid bridge name
//then the subnet has been found and no further processing is needed
func (cnci *Cnci) addSubnetToTopology(bridge *Bridge, gre *GreTapEP, brInfo **bridgeInfo) (brExists bool,
	greExists bool, bLink *linkInfo, gLink *linkInfo, err error) {
	err = nil

	// CS Start
	cnci.topology.Lock()
	bLink, brExists = cnci.topology.linkMap[bridge.GlobalID]
	gLink, greExists = cnci.topology.linkMap[gre.GlobalID]

	if brExists && greExists {
		cnci.topology.Unlock()
		return
	}

	if !brExists {
		bridge.LinkName, err = genLinkName(bridge, cnci.topology.nameMap)
		if err != nil {
			cnci.topology.Unlock()
			return
		}

		bLink = &linkInfo{
			name:  bridge.LinkName,
			ready: make(chan struct{}),
		}
		cnci.topology.linkMap[bridge.GlobalID] = bLink
		*brInfo = &bridgeInfo{}
		cnci.topology.bridgeMap[bridge.GlobalID] = *brInfo
	} else {
		var present bool
		*brInfo, present = cnci.topology.bridgeMap[bridge.GlobalID]
		if !present {
			cnci.topology.Unlock()
			err = fmt.Errorf("Internal error. Missing bridge info")
			return
		}
	}

	if !greExists {
		gre.LinkName, err = genLinkName(gre, cnci.topology.nameMap)
		if err != nil {
			cnci.topology.Unlock()
			return
		}

		gLink = &linkInfo{
			name:  gre.LinkName,
			ready: make(chan struct{}),
		}
		cnci.topology.linkMap[gre.GlobalID] = gLink
		(*brInfo).tunnels++
	}
	cnci.topology.Unlock()
	//End CS
	return
}

// confirm that the gre tunnel device exists. If not, create
// it. Confirm that the correct address is associated with
// the tunnel device.
func (cnci *Cnci) confirmTunnel(n Neighbor) (*GreTunEP, error) {
	IP := net.ParseIP(n.PhysicalIP)
	if IP == nil {
		return nil, fmt.Errorf("Unable to parse local physical IP address")
	}

	tun, err := newGreTunEP("cncitun", IP, n.TunnelID)
	if err != nil {
		return nil, err
	}

	// see if the device already exists
	err = tun.getDevice()
	if err != nil {
		if err = tun.create(); err != nil {
			return nil, err
		}
		if err = tun.enable(); err != nil {
			return nil, err
		}
	}

	// see if my address already exists
	addrs, err := netlink.AddrList(tun.Link, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}

	// XXX: hardcode netmask to 18 for now.
	addr, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", n.TunnelIP, 18))
	if err != nil {
		return nil, err
	}

	var added bool
	for _, a := range addrs {
		if addr.Equal(a) {
			added = true
		} else {
			// we should remove this addr.
			// we can do this because there should
			// be only one.
			err = netlink.AddrDel(tun.Link, &a)
			if err != nil {
				return nil, err
			}
		}
	}
	if !added {
		err = netlink.AddrAdd(tun.Link, addr)
		if err != nil {
			return nil, err
		}
	}

	return tun, nil
}

func neighborEqual(a netlink.Neigh, b netlink.Neigh) (equal bool) {
	if a.IP.Equal(b.IP) && a.LLIPAddr.Equal(b.LLIPAddr) {
		return true
	}
	return false
}

// make sure that the neighbor entries are correct, as well as the
// route entry for the neighbor.
func (cnci *Cnci) confirmNeighbors(tun *GreTunEP, n Neighbor, neighs []netlink.Neigh) (netlink.Neigh, error) {
	neigh := netlink.Neigh{
		IP:        net.ParseIP(n.TunnelIP),
		LLIPAddr:  net.ParseIP(n.PhysicalIP),
		LinkIndex: tun.Link.Index,
		State:     netlink.NUD_PERMANENT,
	}

	var exists bool
	// see if this already exists
	for _, neighbor := range neighs {
		exists = neighborEqual(neighbor, neigh)
		if exists {
			break
		}
	}

	if !exists {
		err := netlink.NeighAdd(&neigh)
		if err != nil {
			return neigh, err
		}

		dst := net.IPNet{
			IP:   net.ParseIP(n.TunnelIP),
			Mask: net.CIDRMask(32, 32),
		}

		route := netlink.Route{
			LinkIndex: tun.Link.Index,
			Dst:       &dst,
		}
		err = netlink.RouteAdd(&route)
		if err != nil {
			return neigh, err
		}

		_, IPnet, err := net.ParseCIDR(n.Subnet)
		if err != nil {
			return neigh, err
		}

		route = netlink.Route{
			LinkIndex: tun.Link.Index,
			Dst:       IPnet,
			Gw:        net.ParseIP(n.TunnelIP),
		}

		err = netlink.RouteAdd(&route)
		if err != nil {
			return neigh, err
		}
	}
	return neigh, nil
}

func (cnci *Cnci) confirmRoutes(tun *GreTunEP, updated []netlink.Neigh, old []netlink.Neigh) error {
	routes, err := netlink.RouteList(tun.Link, netlink.FAMILY_V4)
	if err != nil {
		return err
	}

	for _, n := range old {
		var found bool
		for _, new := range updated {
			found = neighborEqual(n, new)
			if found {
				break
			}
		}

		if !found {
			err := netlink.NeighDel(&n)
			if err != nil {
				glog.Warningf("Unable to delete stale neighbor: (%v)\n", err)
				// keep going.
			}

			// remove routes.
			for _, r := range routes {
				if r.Dst.IP.Equal(n.IP) || r.Gw.Equal(n.IP) {
					err = netlink.RouteDel(&r)
					if err != nil {
						glog.Warningf("Unable to delete stale route (%v)\n", err)
						// keep going.
					}
				}
			}
		}
	}
	return nil
}

// UpdateNeighbors will create a point to multipoint gre tunnel between
// all the CNCIs for this tenant.
func (cnci *Cnci) UpdateNeighbors(neighbors []Neighbor) error {
	var tun *GreTunEP
	var err error

	// this must be done first
	for _, n := range neighbors {
		if n.PhysicalIP == cnci.ComputeAddr[0].IPNet.IP.String() {
			tun, err = cnci.confirmTunnel(n)
			if err != nil {
				return err
			}
			break
		}
	}

	neighs, err := netlink.NeighList(tun.Link.Index, netlink.FAMILY_V4)
	if err != nil {
		return err
	}

	var updated []netlink.Neigh
	for _, n := range neighbors {
		if n.PhysicalIP == cnci.ComputeAddr[0].IPNet.IP.String() {
			continue
		}

		neigh, err := cnci.confirmNeighbors(tun, n, neighs)
		if err != nil {
			return err
		}

		updated = append(updated, neigh)
	}

	// clean up any routes neighbors that need removing.
	return cnci.confirmRoutes(tun, updated, neighs)
}

//AddRemoteSubnet attaches a remote subnet to a local bridge on the CNCI
//If the bridge and DHCP server does not exist it will be created.
//If the tunnel exists and the bridge does not exist the bridge is created
//The bridge name interface name is returned if the bridge is newly created
func (cnci *Cnci) AddRemoteSubnet(subnet net.IPNet, subnetKey int, cnIP net.IP) (string, error) {

	if err := checkInputParams(subnet, subnetKey, cnIP); err != nil {
		return "", err
	}

	bridge, err := NewBridge(genBridgeAlias(subnet))
	if err != nil {
		return "", err
	}

	gre, err := newGreTapEP(genGreAlias(subnet, cnIP), cnci.ComputeAddr[0].IPNet.IP, cnIP, uint32(subnetKey))
	if err != nil {
		return "", err
	}

	//Logically add the bridge and gre tunnel to the topology
	var brInfo *bridgeInfo
	brExists, greExists, bLink, gLink, err := cnci.addSubnetToTopology(bridge, gre, &brInfo)
	if err != nil {
		return "", err
	}
	if brExists && greExists {
		//The subnet already exists and is fully setup
		return bLink.name, nil
	}

	//Now create them. This is time consuming
	if !brExists {
		err = createCnciBridge(bridge, brInfo, cnci.Tenant, subnet)
		bLink.index = bridge.Link.Index
		close(bLink.ready)
		if err != nil {
			//Do not leave the GRE hanging
			close(gLink.ready)
			return "", err
		}
	}

	if !greExists {
		err = createCnciTunnel(gre)
		gLink.index = gre.Link.Index
		close(gLink.ready)
		if err != nil {
			return "", err
		}
	}

	bridge.LinkName, bridge.Link.Index, err = waitForDeviceReady(bLink, cnci.APITimeout)
	if err != nil {
		return "", err
	}
	gre.LinkName, gre.Link.Index, err = waitForDeviceReady(gLink, cnci.APITimeout)
	if err != nil {
		return "", err
	}

	err = gre.attach(bridge)
	if brExists {
		return "", err
	}
	return bridge.LinkName, err

}

//DelRemoteSubnet detaches a remote subnet from the local bridge
//The bridge and DHCP server is kept around as they impose minimal overhead
//and helps in the case where instances keep getting added and deleted constantly
func (cnci *Cnci) DelRemoteSubnet(subnet net.IPNet, subnetKey int, cnIP net.IP) error {

	if err := checkInputParams(subnet, subnetKey, cnIP); err != nil {
		return err
	}

	bridgeID := genBridgeAlias(subnet)

	gre, err := newGreTapEP(genGreAlias(subnet, cnIP),
		cnci.ComputeAddr[0].IPNet.IP,
		cnIP, uint32(subnetKey))

	if err != nil {
		return err
	}

	// CS Start
	cnci.topology.Lock()
	defer cnci.topology.Unlock()

	gLink, present := cnci.topology.linkMap[gre.GlobalID]

	if !present {
		//TODO: Log this and continue
		//fmt.Println("Deleting non existent tunnel ", gre.GlobalID)
		return nil
	}

	if brInfo, present := cnci.topology.bridgeMap[bridgeID]; !present {
		//TODO: Log this and continue
		fmt.Println("internal error bridge does not exist ", bridgeID)
	} else {
		brInfo.tunnels--
	}

	gre.LinkName, gre.Link.Index, err = waitForDeviceReady(gLink, cnci.APITimeout)
	if err != nil {
		return fmt.Errorf("AddRemoteSubnet %s %v", gre.GlobalID, err)
	}

	delete(cnci.topology.nameMap, gre.GlobalID)
	delete(cnci.topology.linkMap, gre.GlobalID)
	err = gre.destroy()

	return err
}

//Shutdown stops all DHCP Servers. Tears down all links and tunnels
//It will continue even on encountering an error and perform as much
//cleanup as possible
func (cnci *Cnci) Shutdown() error {
	var lasterr error

	cnci.topology.Lock()
	defer cnci.topology.Unlock()

	for id, b := range cnci.topology.bridgeMap {
		if b.Dnsmasq != nil {
			if err := b.Dnsmasq.stop(); err != nil {
				lasterr = err
				continue
			}
		} else {
			lasterr = fmt.Errorf("invalid dnsmasq %v", b)
			continue
		}
		delete(cnci.topology.bridgeMap, id)
	}

	for alias, linfo := range cnci.topology.linkMap {
		if linfo != nil {
			//HACKING: Better to create the right type
			vnic, err := NewVnic(alias)
			if err != nil {
				lasterr = err
				continue
			}
			vnic.LinkName, vnic.Link.Attrs().Index, err = waitForDeviceReady(linfo, cnci.APITimeout)
			if err != nil {
				lasterr = err
				continue
			}
			if err := vnic.Destroy(); err != nil {
				lasterr = err
				continue
			}
			delete(cnci.topology.linkMap, alias)
			delete(cnci.topology.nameMap, alias)
		}
	}

	return lasterr
}
