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
	namespace           string
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
		namespace:           ns,
	}
	return &lbControl, nil
}

// Name returns the name of the backend controller
func (ctr *F5Controller) Name() string {
	return "F5Controller"
}

// GetBindIP returns the IP used by users to access their apps
func (ctr *F5Controller) GetBindIP(name string) (string, error) {
	cmClient := ctr.kubeClient.ConfigMaps(ctr.namespace)
	ipCm, err := cmClient.Get(ctr.ipManager.ConfigMapName)
	if err != nil {
		err = fmt.Errorf("ConfigMap %v does not exist.", ctr.ipManager.ConfigMapName)
		return "", err
	}
	ipCmData := ipCm.Data
	for k, v := range ipCmData {
		if v == name {
			return k, nil
		}
	}
	return "", nil
}

// HandleConfigMapCreate creates a new F5 pool, nodes, monitor and virtual server to provide loadbalancing to the app defined in the configmap
func (ctr *F5Controller) HandleConfigMapCreate(configMap *api.ConfigMap) error {
	name := configMap.Namespace + "-" + configMap.Name

	config := configMap.Data
	serviceName := config["target-service-name"]
	namespace := config["namespace"]
	serviceObj, err := ctr.kubeClient.Services(namespace).Get(serviceName)
	if err != nil {
		err = fmt.Errorf("Error getting service object %v/%v. %v", namespace, serviceName, err)
		return err
	}

	//generate Virtual IP
	bindIP, err := ctr.ipManager.GenerateVirtualIP(configMap)
	if err != nil {
		err = fmt.Errorf("Error generating Virtual IP - %v", err)
		return err
	}

	if len(serviceObj.Spec.Ports) == 0 {
		return fmt.Errorf("Could not find any port from service %v.", serviceObj.Name)
	}

	for _, p := range serviceObj.Spec.Ports {
		servicePortName := p.Name
		if p.NodePort == 0 {
			err = fmt.Errorf("NodePort is needed for loadbalancer")
			return err
		}

		monitorName := utils.GetResourceName(MONITOR, name, servicePortName)
		monExist, err := ctr.monitorExist(monitorName)
		if err != nil {
			err = fmt.Errorf("Error accessing monitors. %v", err)
			return err
		}
		if !monExist {
			err = ctr.f5.CreateMonitor(monitorName, MONITOR_PROTOCOL, 5, 16, "", "")
			if err != nil {
				err = fmt.Errorf("Could not create monitor %v. %v", monitorName, err)
				return err
			}
			glog.Infof("Monitor %v created.", monitorName)
		}

		poolName := utils.GetResourceName(POOL, name, servicePortName)
		pool, err := ctr.f5.GetPool(poolName)
		if err != nil {
			err = fmt.Errorf("Error getting pool %v. %v", poolName, err)
			defer ctr.deleteF5Resource(monitorName, MONITOR)
			return err
		}
		if pool == nil {
			err = ctr.createPool(poolName, monitorName)
			if err != nil {
				err = fmt.Errorf("Error creating pool %v. %v", poolName, err)
				defer ctr.deleteF5Resource(monitorName, MONITOR)
				return err
			}
			glog.Infof("Pool %v created.", poolName)
		}

		// Add nodes to pool
		nodes, err := ctr.kubeClient.Nodes().List(api.ListOptions{})
		if err != nil {
			glog.Errorf("Error listing nodes %v", err)
			defer ctr.deleteF5Resource(monitorName, MONITOR)
			defer ctr.deleteF5Resource(poolName, POOL)
		}
		for _, n := range nodes.Items {
			if utils.NodeReady(n) {
				node, err := ctr.f5.GetNode(n.Name)
				if err != nil {
					glog.Errorf("Error getting Node %v. %v", n.Name, err)
					continue
				}
				member := node.Name + ":" + strconv.Itoa(int(p.NodePort))
				ctr.f5.AddPoolMember(poolName, member)
				// Not checking for error since there is a F5 bug that returns error even if the request was successful
				// https://devcentral.f5.com/questions/icontrol-rest-404-despite-success-when-adding-pool-member
				glog.Infof("Member %v added to pool %v.", member, poolName)
			}
		}

		virtualServerName := utils.GetResourceName(VIRTUAL_SERVER, name, servicePortName)
		vs, err := ctr.f5.GetVirtualServer(virtualServerName)
		if err != nil {
			err = fmt.Errorf("Error getting virtual server %v. %v", virtualServerName, err)
			defer ctr.deleteF5Resource(monitorName, MONITOR)
			defer ctr.deleteF5Resource(poolName, POOL)
			return err
		}

		bindPort := p.Port
		dest := fmt.Sprintf("%s:%d", bindIP, bindPort)
		if vs == nil {
			err := ctr.createVirtualServer(virtualServerName, poolName, dest)
			if err != nil {
				err = fmt.Errorf("Error creating virtual server %v. %v", virtualServerName, err)
				defer ctr.deleteF5Resource(monitorName, MONITOR)
				defer ctr.deleteF5Resource(poolName, POOL)
				return err
			}
			glog.Infof("Virtual server %v created.", virtualServerName)
		} else {
			if dest != formatVirtualServerDestination(vs.Destination) {
				vs.Destination = dest
				err = ctr.f5.ModifyVirtualServer(virtualServerName, vs)
				if err != nil {
					glog.Errorf("Error updating virtual server %v destination %v: %v", virtualServerName, dest, err)
				}
				glog.Infof("Virtual server %v has updated its destination to %v.", virtualServerName, dest)
			}
		}
	}

	return nil
}

// HandleConfigMapDelete delete all the resources created in F5 for load balancing an app
func (ctr *F5Controller) HandleConfigMapDelete(configMap *api.ConfigMap) {
	name := configMap.Namespace + "-" + configMap.Name
	cmData := configMap.Data
	serviceName := cmData["target-service-name"]
	namespace := cmData["namespace"]
	serviceObj, err := ctr.kubeClient.Services(namespace).Get(serviceName)
	if err != nil {
		err = fmt.Errorf("Error getting service object %v/%v. %v", namespace, serviceName, err)
	}
	for _, p := range serviceObj.Spec.Ports {
		servicePortName := p.Name

		virtualServerName := utils.GetResourceName(VIRTUAL_SERVER, name, servicePortName)
		ctr.deleteF5Resource(virtualServerName, VIRTUAL_SERVER)

		poolName := utils.GetResourceName(POOL, name, servicePortName)
		ctr.deleteF5Resource(poolName, POOL)

		monitorName := utils.GetResourceName(MONITOR, name, servicePortName)
		ctr.deleteF5Resource(monitorName, MONITOR)

		err = ctr.ipManager.DeleteVirtualIP(name)
		if err != nil {
			glog.Errorf("Error deleting Virtual IP - %v", err)
		}
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

	configMapNodePortMap := utils.GetPoolNodePortMap(ctr.kubeClient, ctr.watchNamespace, ctr.configMapLabelKey, ctr.configMapLabelValue)
	for configmapPoolName, nodePort := range configMapNodePortMap {
		member := node.Name + ":" + strconv.Itoa(nodePort)
		err = ctr.f5.AddPoolMember(configmapPoolName, member)
		glog.Infof("Created member %v in pool %v", member, configmapPoolName)
	}
}

// HandleNodeDelete deletes member for this node
func (ctr *F5Controller) HandleNodeDelete(node *api.Node) {

	configMapNodePortMap := utils.GetPoolNodePortMap(ctr.kubeClient, ctr.watchNamespace, ctr.configMapLabelKey, ctr.configMapLabelValue)
	for configmapPoolName, nodePort := range configMapNodePortMap {
		member := node.Name + ":" + strconv.Itoa(nodePort)
		err := ctr.f5.DeletePoolMember(configmapPoolName, member)
		if err != nil {
			glog.Errorf("Could not delete member %v from pool %v. %v", member, configmapPoolName, err)
			continue
		}
		glog.Infof("Deleted member %v for pool %v", member, configmapPoolName)
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

// createPool creates a F5 pool. go-bigip.CreatePool does not allow to set other params except for the name
func (ctr *F5Controller) createPool(name string, monitor string) error {
	pool := bigip.Pool{
		Name:      name,
		Monitor:   monitor,
		AllowSNAT: true,
		AllowNAT:  true,
	}
	marshalJSON, _ := pool.MarshalJSON()
	return ctr.f5ApiCall(string(marshalJSON), "ltm/pool")
}

func (ctr *F5Controller) createVirtualServer(name, pool string, destination string) error {
	virtualServer := bigip.VirtualServer{
		Name:        name,
		Mask:        "255.255.255.255",
		Pool:        pool,
		Destination: destination,
		SourceAddressTranslation: struct {
			Type string `json:"type,omitempty"`
		}{
			Type: "automap",
		},
	}
	marshalJSON, _ := json.Marshal(virtualServer)
	return ctr.f5ApiCall(string(marshalJSON), "ltm/virtual")
}

func (ctr *F5Controller) f5ApiCall(marshalJSON string, url string) error {
	req := &bigip.APIRequest{
		Method:      "post",
		URL:         url,
		Body:        marshalJSON,
		ContentType: "application/json",
	}

	_, callErr := ctr.f5.APICall(req)
	return callErr
}

func formatVirtualServerDestination(destination string) string {
	// /Commmon/<ip>::<port> -> <ip>:<port>
	res := strings.Split(destination, "/")
	return res[len(res)-1]
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
