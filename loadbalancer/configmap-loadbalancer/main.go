package main

import (
	"flag"
	"time"
    "os"

	"github.com/golang/glog"
    "github.com/spf13/pflag"

	"k8s.io/contrib/loadbalancer/configmap-loadbalancer/backends"
	"k8s.io/contrib/loadbalancer/configmap-loadbalancer/backends/nginx"
	"k8s.io/contrib/loadbalancer/configmap-loadbalancer/controllers"
	"k8s.io/kubernetes/pkg/api"
	client "k8s.io/kubernetes/pkg/client/unversioned"
    kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

var (
    flags = pflag.NewFlagSet("", pflag.ExitOnError)
	inCluster = flags.Bool("running-in-cluster", true,
		`Optional, if this controller is running in a kubernetes cluster, use the
		 pod secrets for creating a Kubernetes client.`)

	watchNamespace = flag.String("watch-namespace", api.NamespaceAll,
		`Namespace to watch for Configmap/Services/Endpoints. By default the controller
		watches acrosss all namespaces`)
)

func init() {
	backends.Register("nginx", nginx.NewNGINXController)
}

func main() {
	flags.AddGoFlagSet(flag.CommandLine)
	flags.Parse(os.Args)
    clientConfig := kubectl_util.DefaultClientConfig(flags)

	var kubeClient *client.Client

    var err error
	if *inCluster {
		kubeClient, err = client.NewInCluster()
	} else {
		config, connErr := clientConfig.ClientConfig()
		if connErr != nil {
			glog.Fatalf("error connecting to the client: %v", err)
		}
		kubeClient, err = client.New(config)
	}

    if err != nil {
		glog.Fatalf("failed to create client: %v", err)
	}
	
	backendController, err := backends.CreateBackendController(map[string]string{
		"BACKEND": "nginx",
		"CONFIG_PATH": "/etc/nginx",
	})
	configController, _ := controllers.NewConfigMapController(kubeClient, 30*time.Second, *watchNamespace, backendController)
	configController.Run()
}