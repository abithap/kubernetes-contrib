package lbaasV2

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rackspace/gophercloud"
	openstack_lib "github.com/rackspace/gophercloud/openstack"
	"github.com/rackspace/gophercloud/openstack/networking/v2/extensions/lbaas_v2/listeners"
	"github.com/rackspace/gophercloud/openstack/networking/v2/extensions/lbaas_v2/loadbalancers"
	"github.com/rackspace/gophercloud/openstack/networking/v2/extensions/lbaas_v2/monitors"
	"github.com/rackspace/gophercloud/openstack/networking/v2/extensions/lbaas_v2/pools"
	"github.com/rackspace/gophercloud/pagination"
	"k8s.io/contrib/loadbalancer/loadbalancer/backend"

	"github.com/golang/glog"
)

const (
	LOADBALANCER = "loadbalancer"
	LISTENER     = "listener"
	POOL         = "pool"
	MONITOR      = "monitor"
)

type pollUntil func(string) (bool, error)

// OctaviaController Controller to manage octavia resources
type OctaviaController struct {
	compute  *gophercloud.ServiceClient
	network  *gophercloud.ServiceClient
	subnetID string
}

func init() {
	backend.Register("openstack-lbaasv2", NewOctaviaController)
}

// NewOctaviaController creates a Octavia controller
func NewOctaviaController(conf map[string]string) (backend.BackendController, error) {

	authOptions := gophercloud.AuthOptions{
		IdentityEndpoint: os.Getenv("OS_AUTH_URL"),
		Username:         os.Getenv("OS_USERNAME"),
		Password:         os.Getenv("OS_PASSWORD"),
		TenantName:       os.Getenv("OS_TENANT_NAME"),
		// Persistent service, so we need to be able to renew tokens.
		AllowReauth: true,
	}

	openstackClient, err := openstack_lib.AuthenticatedClient(authOptions)
	if err != nil {
		glog.Fatalf("Failed to retrieve openstack client. %v", err)
	}

	compute, err := openstack_lib.NewComputeV2(openstackClient, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		glog.Fatalf("Failed to find compute endpoint: %v", err)
	}

	network, err := openstack_lib.NewNetworkV2(openstackClient, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		glog.Fatalf("Failed to find network endpoint: %v", err)
	}

	octControl := OctaviaController{
		compute:  compute,
		network:  network,
		subnetID: os.Getenv("OS_SUBNET_ID"),
	}

	return &octControl, nil
}

// Name returns the name of the backend controller
func (octavia *OctaviaController) Name() string {
	return "OctaviaController"
}

// Create a new lbaas loadbalancer resource
func (octavia *OctaviaController) Create(name string, config backend.BackendConfig) {
	if config.NodePort == 0 {
		glog.Errorf("Nodeport is needed for loadbalancer")
		return
	}

	// Delete current load balancer for this service if it exist
	octavia.Delete(name)

	lbName := getResourceName(LOADBALANCER, name)
	lb, err := loadbalancers.Create(octavia.network, loadbalancers.CreateOpts{
		Name:         lbName,
		AdminStateUp: loadbalancers.Up,
		VipSubnetID:  octavia.subnetID,
	}).Extract()
	if err != nil {
		glog.Errorf("Could not create loadbalancer %v. %v", lbName, err)
		return
	}
	glog.Infof("Created loadbalancer %v. ID: %v", lbName, lb.ID)

	// Wait for load balancer resource to be ACTIVE state
	octavia.waitLoadbalancerReady(lb.ID)

	// Create a listener resouce for the loadbalancer
	listenerName := getResourceName(LISTENER, name)
	listener, err := listeners.Create(octavia.network, listeners.CreateOpts{
		Protocol:       listeners.Protocol(config.Protocol),
		Name:           listenerName,
		LoadbalancerID: lb.ID,
		AdminStateUp:   listeners.Up,
		ProtocolPort:   config.BindPort,
	}).Extract()
	if err != nil {
		glog.Errorf("Could not create listener %v. %v", listenerName, err)
		defer octavia.deleteOctaviaResource(lb.ID, LOADBALANCER, lb.ID)
		return
	}
	glog.Infof("Created listener %v. ID: %v", listenerName, listener.ID)

	// Wait for load balancer resource to be ACTIVE state
	octavia.waitLoadbalancerReady(lb.ID)

	// Create a pool resouce for the listener
	poolName := getResourceName(POOL, name)
	pool, err := pools.Create(octavia.network, pools.CreateOpts{
		LBMethod:   pools.LBMethodRoundRobin,
		Protocol:   pools.Protocol(config.Protocol),
		Name:       poolName,
		ListenerID: listener.ID,
	}).Extract()
	if err != nil {
		glog.Errorf("Could not create pool %v. %v", poolName, err)
		defer octavia.deleteOctaviaResource(lb.ID, LOADBALANCER, lb.ID)
		defer octavia.deleteOctaviaResource(lb.ID, LISTENER, listener.ID)
		return
	}
	glog.Infof("Created pool %v. ID: %v", poolName, pool.ID)
	// Wait for load balancer resource to be ACTIVE state
	octavia.waitLoadbalancerReady(lb.ID)

	// Associate servers to the pool
	for _, ip := range config.BindIPs {
		member, err := pools.CreateAssociateMember(octavia.network, pool.ID, pools.MemberCreateOpts{
			SubnetID:     octavia.subnetID,
			Address:      ip,
			ProtocolPort: config.NodePort,
		}).ExtractMember()
		if err != nil {
			glog.Errorf("Could not create member for %v. %v", ip, err)
			defer octavia.deleteOctaviaResource(lb.ID, LOADBALANCER, lb.ID)
			defer octavia.deleteOctaviaResource(lb.ID, LISTENER, listener.ID)
			defer octavia.deleteOctaviaResource(lb.ID, POOL, pool.ID)
			return
		}
		glog.Infof("Created member for %v. ID: %v", ip, member.ID)
		// Wait for load balancer resource to be ACTIVE state
		octavia.waitLoadbalancerReady(lb.ID)
	}

	// Create health monitor for the pool
	monitor, err := monitors.Create(octavia.network, monitors.CreateOpts{
		Type:       string(config.Protocol),
		PoolID:     pool.ID,
		Delay:      20,
		Timeout:    10,
		MaxRetries: 5,
	}).Extract()
	if err != nil {
		glog.Errorf("Could not create health monitor for pool %v. %v", poolName, err)
		defer octavia.deleteOctaviaResource(lb.ID, LOADBALANCER, lb.ID)
		defer octavia.deleteOctaviaResource(lb.ID, LISTENER, listener.ID)
		defer octavia.deleteOctaviaResource(lb.ID, POOL, pool.ID)
		return
	}
	glog.Infof("Created health monitor for pool %v. ID: %v", poolName, monitor.ID)
	// Wait for load balancer resource to be ACTIVE state
	octavia.waitLoadbalancerReady(lb.ID)
}

// Delete the lbaas loadbalancer resource
func (octavia *OctaviaController) Delete(name string) {

	// Find loadbalancer by name
	lbName := getResourceName(LOADBALANCER, name)
	opts := loadbalancers.ListOpts{Name: lbName}
	pager := loadbalancers.List(octavia.network, opts)
	lbErr := pager.EachPage(func(page pagination.Page) (bool, error) {
		loadbalancerList, err := loadbalancers.ExtractLoadbalancers(page)
		if err != nil {
			return false, err
		}

		if len(loadbalancerList) == 0 {
			err = fmt.Errorf("Load balancer with name %v not found.", lbName)
			return false, err
		}

		if len(loadbalancerList) > 1 {
			err = fmt.Errorf("More than one loadbalancer with name %v found.", lbName)
			return false, err
		}

		lbID := loadbalancerList[0].ID // assuming there is only one loadbalancer with this name
		listenerList := loadbalancerList[0].Listeners
		if len(listenerList) != 0 {
			listener, _ := listeners.Get(octavia.network, listenerList[0].ID).Extract() // assuming there is only one listener
			poolID := listener.DefaultPoolID
			if poolID != "" {
				pool, _ := pools.Get(octavia.network, poolID).Extract()
				if pool.MonitorID != "" {
					octavia.deleteOctaviaResource(lbID, MONITOR, pool.MonitorID)
				}
				octavia.deleteOctaviaResource(lbID, POOL, poolID)
			}
			octavia.deleteOctaviaResource(lbID, LISTENER, listener.ID)
		}
		octavia.deleteOctaviaResource(lbID, LOADBALANCER, lbID)
		return true, nil
	})

	if lbErr != nil {
		glog.Errorf("Could not get list of loadbalancer. %v.", lbErr)
	}

}

// AddNodeHandler creates new member for this node in every loadbalancer pool
func (octavia *OctaviaController) AddNodeHandler(ip string, configMapNodePortMap map[string]int) {
	for configmapName, nodePort := range configMapNodePortMap {
		poolName := getResourceName(POOL, configmapName)
		poolID, err := octavia.getPoolIDFromName(poolName)
		if err != nil {
			glog.Errorf("Could not get pool %v. %v", poolName, err)
			continue
		}
		memberID, err := octavia.createMemberResource(poolID, ip, nodePort)
		if err != nil {
			glog.Errorf("Could not create member for pool %v. IP: %v. Port: %v", poolName, ip, nodePort)
			continue
		}
		glog.Infof("Created member for %v. ID: %v", ip, memberID)
	}
}

// DeleteNodeHandler deletes member for this node
func (octavia *OctaviaController) DeleteNodeHandler(ip string, configMapNodePortMap map[string]int) {
	for configmapName := range configMapNodePortMap {
		poolName := getResourceName(POOL, configmapName)
		poolID, err := octavia.getPoolIDFromName(poolName)
		if err != nil {
			glog.Errorf("Could not get pool for %v. %v", poolName, err)
			continue
		}
		memberID, err := octavia.getMemberIDFromIP(poolID, ip)
		if err != nil {
			glog.Errorf("Could not get member for pool %v. IP: %v.", poolName, ip)
			continue
		}
		err = pools.DeleteMember(octavia.network, poolID, memberID).ExtractErr()
		if err != nil {
			glog.Errorf("Could not get member for pool %v. memberID: %v", poolName, memberID)
			continue
		}
		glog.Infof("Deleted member for pool %v with IP: %v. ID: %v", poolName, ip, memberID)
	}
}

// UpdateNodeHandler update IP of the member for this node if it exists. If it doesnt, it will create a new member
func (octavia *OctaviaController) UpdateNodeHandler(oldIP string, newIP string, configMapNodePortMap map[string]int) {
	for configmapName, nodePort := range configMapNodePortMap {
		poolName := getResourceName(POOL, configmapName)
		poolID, err := octavia.getPoolIDFromName(poolName)
		if err != nil {
			glog.Errorf("Could not get pool for %v. %v", poolName, err)
			continue
		}
		memberID, err := octavia.getMemberIDFromIP(poolID, oldIP)
		if err != nil {
			glog.Warningf("Could not get member for pool %v. IP: %v. Creating...", poolName, oldIP)
			memberID, err := octavia.createMemberResource(poolID, newIP, nodePort)
			if err != nil {
				glog.Errorf("Could not create member for pool %v. IP: %v. Port: %v", poolName, newIP, nodePort)
				continue
			}
			glog.Infof("Created member for %v. ID: %v", newIP, memberID)
			continue
		}

		// Update member. Change IP
		res, err := pools.UpdateAssociateMember(octavia.network, poolID, memberID, pools.MemberUpdateOpts{
			Address:      newIP,
			ProtocolPort: nodePort,
		}).ExtractMember()
		if err != nil {
			glog.Errorf("Could not update member %v. %v", memberID, err)
		}
		glog.Infof("Updated member %v. Changed IP from %v to %v.", res.ID, oldIP, newIP)
	}
}

func getResourceName(resourceType string, names ...string) string {
	return strings.Join(names, "-") + "-" + resourceType
}

func (octavia *OctaviaController) getPoolIDFromName(poolName string) (string, error) {
	pager := pools.List(octavia.network, pools.ListOpts{Name: poolName})
	var poolID string
	poolErr := pager.EachPage(func(page pagination.Page) (bool, error) {
		poolList, err := pools.ExtractPools(page)
		if err != nil {
			return false, err
		}

		if len(poolList) == 0 {
			err = fmt.Errorf("Pool with name %v not found.", poolName)
			return false, err
		}

		if len(poolList) > 1 {
			err = fmt.Errorf("More than one pool with name %v found. %v", poolName, poolList)
			return false, err
		}
		poolID = poolList[0].ID
		return true, nil
	})

	return poolID, poolErr
}

func (octavia *OctaviaController) createMemberResource(poolID string, ip string, nodePort int) (string, error) {
	member, err := pools.CreateAssociateMember(octavia.network, poolID, pools.MemberCreateOpts{
		SubnetID:     octavia.subnetID,
		Address:      ip,
		ProtocolPort: nodePort,
	}).ExtractMember()
	if err != nil {
		return "", fmt.Errorf("Could not create member for %v. %v", ip, err)
	}
	return member.ID, nil
}

func (octavia *OctaviaController) getMemberIDFromIP(poolID string, ip string) (string, error) {
	var memberID string
	pager := pools.ListAssociateMembers(octavia.network, poolID, pools.MemberListOpts{Address: ip})
	memberErr := pager.EachPage(func(page pagination.Page) (bool, error) {
		membersList, err := pools.ExtractMembers(page)
		// There should only be one member that has this IP.
		if err != nil {
			return false, err
		}

		if len(membersList) == 0 {
			err = fmt.Errorf("Member with IP %v not found.", ip)
			return false, err
		}

		if len(membersList) > 1 {
			err = fmt.Errorf("More than one member with IP %v found.", ip)
			return false, err
		}
		memberID = membersList[0].ID
		return true, nil
	})
	return memberID, memberErr
}

func (octavia *OctaviaController) waitLoadbalancerReady(lbID string) {
	// Wait for load balancer resource to be ACTIVE state
	_, err := resourceReady(octavia.checkLoadbalancerReady, lbID, 2, 300)
	if err != nil {
		glog.Errorf("Loadbalancer %v resource did not go ACTIVE. %v", lbID, err)
		return
	}
}

func resourceReady(f pollUntil, resourceID string, interval int64, timeout int64) (bool, error) {
	to := time.After(time.Duration(timeout) * time.Second)
	tick := time.Tick(time.Duration(interval) * time.Second)
	for {
		select {
		case <-to:
			return false, errors.New("Timed out")
		case <-tick:
			ok, err := f(resourceID)
			if err != nil {
				return false, err
			} else if ok {
				return true, nil
			}
		}
	}
}

func (octavia *OctaviaController) checkLoadbalancerReady(lbID string) (bool, error) {
	lb, err := loadbalancers.Get(octavia.network, lbID).Extract()
	if err != nil {
		return true, err
	}

	if lb.ProvisioningStatus == "ACTIVE" || lb.ProvisioningStatus == "ERROR" {
		return true, nil
	}
	return false, nil
}

func (octavia *OctaviaController) deleteOctaviaResource(lbID string, resourceType string, resourceID string) {
	glog.Errorf("Deleting %v %v.", resourceType, resourceID)
	var err error
	switch {
	case resourceType == LOADBALANCER:
		err = loadbalancers.Delete(octavia.network, resourceID).Err
	case resourceType == LISTENER:
		err = listeners.Delete(octavia.network, resourceID).Err
	case resourceType == POOL:
		err = pools.Delete(octavia.network, resourceID).Err
	case resourceType == MONITOR:
		err = monitors.Delete(octavia.network, resourceID).Err
	}
	if err != nil {
		glog.Errorf("Could not delete %v %v. %v", resourceType, resourceID, err)
		return
	}
	if resourceType != LOADBALANCER {
		// Wait for load balancer resource to be ACTIVE state
		octavia.waitLoadbalancerReady(lbID)
	}
	glog.Errorf("%v %v Deleted", resourceType, resourceID)
}
