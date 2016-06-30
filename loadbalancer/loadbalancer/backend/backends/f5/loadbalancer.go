package f5

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/scottdware/go-bigip"
	"k8s.io/contrib/loadbalancer/loadbalancer/backend"
	"k8s.io/contrib/loadbalancer/loadbalancer/controllers"
	"k8s.io/contrib/loadbalancer/loadbalancer/utils"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned"
)

const (
	VIRTUAL_SERVER   = "virtualserver"
	POOL             = "pool"
	MONITOR          = "monitor"
	MONITOR_PROTOCOL = "tcp"
)

// F5Controller Controller to communicate with F5
type F5Controller struct {
	f5                  *bigip.BigIP
	kubeClient          *unversioned.Client
	watchNamespace      string
	configMapLabelKey   string
	configMapLabelValue string
	ipManager           *controllers.IPManager
}

func init() {
	backend.Register("f5", NewF5Controller)
}

// NewF5Controller creates a F5 controller
func NewF5Controller(kubeClient *unversioned.Client, watchNamespace string, conf map[string]string, configLabelKey, configLabelValue string) (backend.BackendController, error) {
	f5Session := bigip.NewSession(os.Getenv("F5_HOST"), os.Getenv("F5_USER"), os.Getenv("F5_PASSWORD"))

	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		ns = api.NamespaceDefault
	}

	ipMgr := controllers.NewIPManager(kubeClient, ns, watchNamespace, configLabelKey, configLabelValue)
	if ipMgr == nil {
		glog.Fatalln("NewIPManager returned nil")
	}

	lbControl := F5Controller{
		f5:                  f5Session,
		kubeClient:          kubeClient,
		watchNamespace:      watchNamespace,
		configMapLabelKey:   configLabelKey,
		configMapLabelValue: configLabelValue,
		ipManager:           ipMgr,
	}
	return &lbControl, nil
}

// Name returns the name of the backend controller
func (ctr *F5Controller) Name() string {
	return "F5Controller"
}

// GetBindIP returns the IP used by users to access their apps
func (ctr *F5Controller) GetBindIP(name string) string {
	virtualServerName := getResourceName(VIRTUAL_SERVER, name)
	virtualServer, err := ctr.f5.GetVirtualServer(virtualServerName)
	if err != nil {
		glog.Errorf("Error getting virtual server %v. %v", virtualServerName, err)
		return ""
	}
	if virtualServer == nil {
		return ""
	}
	return formatVirtualServerDestination(virtualServer.Destination)
}

// HandleConfigMapCreate creates a new F5 pool, nodes, monitor and virtual server to provide loadbalancing to the app defined in the configmap
func (ctr *F5Controller) HandleConfigMapCreate(configMap *api.ConfigMap) {
	name := configMap.Namespace + "-" + configMap.Name

	config := configMap.Data
	serviceName := config["target-service-name"]
	namespace := config["namespace"]
	serviceObj, err := ctr.kubeClient.Services(namespace).Get(serviceName)
	if err != nil {
		glog.Errorf("Error getting service object %v/%v. %v", namespace, serviceName, err)
		return
	}
	servicePort, err := utils.GetServicePort(serviceObj, config["target-port-name"])
	if err != nil {
		glog.Errorf("Error while getting the service port %v", err)
		return
	}
	if servicePort.NodePort == 0 {
		glog.Errorf("NodePort is needed for loadbalancer")
		return
	}

	//generate Virtual IP
	bindIP, err := ctr.ipManager.GenerateVirtualIP(configMap)
	if err != nil {
		glog.Errorf("Error generating Virtual IP - %v", err)
		return
	}

	poolName := getResourceName(POOL, name)
	pool, err := ctr.f5.GetPool(poolName)
	if err != nil {
		glog.Errorf("Error getting pool %v. %v", poolName, err)
		return
	}
	if pool == nil {
		err = ctr.f5.CreatePool(poolName)
		if err != nil {
			glog.Errorf("Error creating pool %v. %v", poolName, err)
			return
		}
		glog.Infof("Pool %v created.", poolName)
	}

	// Add nodes to pool
	nodes, err := ctr.kubeClient.Nodes().List(api.ListOptions{})
	if err != nil {
		glog.Errorf("Error listing nodes %v", err)
		defer ctr.deleteF5Resource(poolName, POOL)
	}
	for _, n := range nodes.Items {
		if utils.NodeReady(n) {
			node, err := ctr.f5.GetNode(n.Name)
			if err != nil {
				glog.Errorf("Error getting Node %v. %v", n.Name, err)
				continue
			}
			member := node.Name + ":" + strconv.Itoa(int(servicePort.NodePort))
			ctr.f5.AddPoolMember(poolName, member)
			// Not checking for error since there is a F5 bug that returns error even if the request was successful
			// https://devcentral.f5.com/questions/icontrol-rest-404-despite-success-when-adding-pool-member
			glog.Infof("Member %v added to pool %v.", member, poolName)
		}
	}

	monitorName := getResourceName(MONITOR, name)
	monExist, err := ctr.monitorExist(monitorName)
	if err != nil {
		glog.Errorf("Error accessing monitors. %v", err)
		defer ctr.deleteF5Resource(poolName, POOL)
		return
	}
	if !monExist {
		err = ctr.f5.CreateMonitor(monitorName, MONITOR_PROTOCOL, 5, 16, "", "")
		if err != nil {
			glog.Errorf("Could not create monitor %v. %v", monitorName, err)
			defer ctr.deleteF5Resource(poolName, POOL)
			return
		}
		glog.Infof("Monitor %v created.", monitorName)
	}

	err = ctr.f5.AddMonitorToPool(monitorName, poolName)
	if err != nil {
		glog.Errorf("Could not add monitor %v to pool %v. %v", monitorName, poolName, err)
		defer ctr.deleteF5Resource(monitorName, MONITOR)
		defer ctr.deleteF5Resource(poolName, POOL)
		return
	}
	glog.Infof("Monitor %v added to pool %v.", monitorName, poolName)

	virtualServerName := getResourceName(VIRTUAL_SERVER, name)
	vs, err := ctr.f5.GetVirtualServer(virtualServerName)
	if err != nil {
		glog.Errorf("Error getting virtual server %v. %v", virtualServerName, err)
		return
	}
	bindPort, _ := strconv.Atoi(config["bind-port"])
	if vs == nil {
		err = ctr.f5.CreateVirtualServer(virtualServerName, bindIP, "32", poolName, bindPort)
		if err != nil {
			glog.Errorf("Could not create virtual server %v for IP %v in pool %v. %v", virtualServerName, bindIP, poolName, err)
			defer ctr.deleteF5Resource(monitorName, MONITOR)
			defer ctr.deleteF5Resource(poolName, POOL)
			return
		}
		glog.Infof("Virtual server %v created.", virtualServerName)
	} else {
		destination := fmt.Sprintf("%s:%d", bindIP, bindPort)
		if destination != formatVirtualServerDestination(vs.Destination) {
			vs.Destination = destination
			err = ctr.f5.ModifyVirtualServer(virtualServerName, vs)
			if err != nil {
				glog.Errorf("Error updating virtual server %v destination %v: %v", virtualServerName, destination, err)
			}
			glog.Infof("Virtual server %v has updated its destination to %v.", virtualServerName, destination)
		}
	}
}

// HandleConfigMapDelete delete all the resources created in F5 for load balancing an app
func (ctr *F5Controller) HandleConfigMapDelete(name string) {
	virtualServerName := getResourceName(VIRTUAL_SERVER, name)
	ctr.deleteF5Resource(virtualServerName, VIRTUAL_SERVER)

	poolName := getResourceName(POOL, name)
	ctr.deleteF5Resource(poolName, POOL)

	monitorName := getResourceName(MONITOR, name)
	ctr.deleteF5Resource(monitorName, MONITOR)

	err := ctr.ipManager.DeleteVirtualIP(name)
	if err != nil {
		glog.Errorf("Error deleting Virtual IP - %v", err)
	}
}

// HandleNodeCreate creates new member for this node in every pool
func (ctr *F5Controller) HandleNodeCreate(node *api.Node) {

	n, err := ctr.f5.GetNode(node.Name)
	if err != nil {
		glog.Errorf("Error getting Node %v. %v", node.Name, err)
	}
	ip, err := utils.GetNodeHostIP(*node)
	if err != nil {
		glog.Errorf("Error getting IP for node %v. %v", node.Name, err)
		return
	}
	if n == nil {
		ctr.f5.CreateNode(node.Name, *ip)
		if err != nil {
			glog.Errorf("Error creating node %v and IP %v. %v", n.Name, *ip, err)
			return
		}
	} else {
		if n.Address != *ip {
			n.Address = *ip
			err := ctr.f5.ModifyNode(n.Name, n)
			if err != nil {
				glog.Errorf("Error updating node %v and IP %v. %v", n.Name, *ip, err)
			}
			glog.Infof("Node %v has updated its IP to %v.", n.Name, *ip)
		}
	}

	configMapNodePortMap := utils.GetLBConfigMapNodePortMap(ctr.kubeClient, ctr.watchNamespace, ctr.configMapLabelKey, ctr.configMapLabelValue)
	for configmapName, nodePort := range configMapNodePortMap {
		poolName := getResourceName(POOL, configmapName)
		member := node.Name + ":" + strconv.Itoa(int(nodePort))
		err = ctr.f5.AddPoolMember(poolName, member)
		glog.Infof("Created member %v in pool %v", member, poolName)
	}
}

// HandleNodeDelete deletes member for this node
func (ctr *F5Controller) HandleNodeDelete(node *api.Node) {
	configMapNodePortMap := utils.GetLBConfigMapNodePortMap(ctr.kubeClient, ctr.watchNamespace, ctr.configMapLabelKey, ctr.configMapLabelValue)
	for configmapName, nodePort := range configMapNodePortMap {
		poolName := getResourceName(POOL, configmapName)
		member := node.Name + ":" + strconv.Itoa(int(nodePort))
		err := ctr.f5.DeletePoolMember(poolName, member)
		if err != nil {
			glog.Errorf("Could not delete member %v from pool %v. %v", member, poolName, err)
			continue
		}
		glog.Infof("Deleted member %v for pool %v", member, poolName)
	}
}

// HandleNodeUpdate updates IP of the member for this node if it exists. If it doesnt, it will create a new member
func (ctr *F5Controller) HandleNodeUpdate(oldNode *api.Node, curNode *api.Node) {

	// Update the IP of the old node to match the updated current node
	oldN, err := ctr.f5.GetNode(oldNode.Name)
	if err != nil {
		glog.Errorf("Error getting Node %v. %v", oldNode.Name, err)
	}

	if oldN == nil {
		ctr.HandleNodeCreate(curNode)
	} else {
		ip, err := utils.GetNodeHostIP(*curNode)
		if err != nil {
			glog.Errorf("Error getting IP for node %v. %v", curNode.Name, err)
		}
		if oldN.Address != *ip {
			oldN.Address = *ip
			ctr.f5.ModifyNode(oldN.Name, oldN)
		}
	}
}

// monitorExist checks whether the monitor exists in F5. The big-ip library does not have support for TCP monitors lookup.
// Therefore i am making my own.
func (ctr *F5Controller) monitorExist(name string) (bool, error) {
	var m bigip.Monitors
	req := &bigip.APIRequest{
		Method:      "get",
		URL:         "ltm/monitor/" + MONITOR_PROTOCOL,
		ContentType: "application/json",
	}

	resp, err := ctr.f5.APICall(req)
	if err != nil {
		return false, err
	}
	err = json.Unmarshal(resp, &m)
	if err != nil {
		return false, err
	}

	for _, mon := range m.Monitors {
		if mon.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func formatVirtualServerDestination(destination string) string {
	// /Commmon/<ip>::<port> -> <ip>:<port>
	res := strings.Split(destination, "/")
	return res[len(res)-1]
}

func getResourceName(resourceType string, names ...string) string {
	return strings.Join(names, "-") + "-" + resourceType
}

func (ctr *F5Controller) deleteF5Resource(resourceName string, resourceType string) {
	glog.Errorf("Deleting %v %v.", resourceType, resourceName)
	var err error
	switch {
	case resourceType == VIRTUAL_SERVER:
		err = ctr.f5.DeleteVirtualServer(resourceName)
	case resourceType == POOL:
		err = ctr.f5.DeletePool(resourceName)
	case resourceType == MONITOR:
		err = ctr.f5.DeleteMonitor(resourceName, MONITOR_PROTOCOL)
	}
	if err != nil {
		glog.Errorf("Could not delete %v %v. %v", resourceType, resourceName, err)
		return
	}
	glog.Infof("%v %v Deleted", resourceType, resourceName)
}
