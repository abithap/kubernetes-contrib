/*
Copyright 2016 The Kubernetes Authors All rights reserved.

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

package controllers

import (
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"

	"k8s.io/contrib/loadbalancer/configmap-loadbalancer/backends"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/cache"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/controller/framework"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/watch"
)

// ConfigMapController watches Kubernetes API for ConfigMap changes
// and reconfigures backend when needed
type ConfigMapController struct {
	client              *client.Client
	configMapController *framework.Controller
	configMapLister     StoreToConfigMapLister
	configMapQueue      *taskQueue
	stopCh              chan struct{}
	backendController   backends.BackendController
}

// Values to verify the configmap object is a loadbalancer config
const (
	configLabelKey   = "loadbalancer"
	configLabelValue = "configmap"
)

var keyFunc = framework.DeletionHandlingMetaNamespaceKeyFunc

// NewConfigMapController creates a controller
func NewConfigMapController(kubeClient *client.Client, resyncPeriod time.Duration, namespace string, controller backends.BackendController) (*ConfigMapController, error) {
	configMapController := ConfigMapController{
		client:            kubeClient,
		stopCh:            make(chan struct{}),
		backendController: controller,
	}
	configMapController.configMapQueue = NewTaskQueue(configMapController.syncConfigMap)

	configMapHandlers := framework.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			configMapController.configMapQueue.enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			configMapController.configMapQueue.enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				configMapController.configMapQueue.enqueue(cur)
			}
		},
	}
	configMapController.configMapLister.Store, configMapController.configMapController = framework.NewInformer(
		&cache.ListWatch{
			ListFunc:  configMapListFunc(kubeClient, namespace),
			WatchFunc: configMapWatchFunc(kubeClient, namespace),
		},
		&api.ConfigMap{}, resyncPeriod, configMapHandlers)

	return &configMapController, nil
}

// Run starts the configmap controller
func (configMapController *ConfigMapController) Run() {
	go configMapController.configMapController.Run(configMapController.stopCh)
	go configMapController.configMapQueue.run(time.Second, configMapController.stopCh)
	<-configMapController.stopCh
}

func configMapListFunc(c *client.Client, ns string) func(api.ListOptions) (runtime.Object, error) {
	return func(opts api.ListOptions) (runtime.Object, error) {
		return c.ConfigMaps(ns).List(opts)
	}
}

func configMapWatchFunc(c *client.Client, ns string) func(options api.ListOptions) (watch.Interface, error) {
	return func(options api.ListOptions) (watch.Interface, error) {
		return c.ConfigMaps(ns).Watch(options)
	}
}

func (configMapController *ConfigMapController) syncConfigMap(key string) {
	glog.Infof("Syncing %v", key)

	// defaut/some-configmap -> default-some-configmap
	name := strings.Replace(key, "/", "-", -1)

	obj, configMapExists, err := configMapController.configMapLister.Store.GetByKey(key)
	if err != nil {
		configMapController.configMapQueue.requeue(key, err)
		return
	}

	if !configMapExists {
		configMapController.backendController.DeleteConfig(name)
	} else {
		configMap := obj.(*api.ConfigMap)
		configMapData := configMap.Data
		configMapMetaData := configMap.ObjectMeta

		// Check if the configmap event is of type app=loadbalancer.
		if val, ok := configMapMetaData.Labels[configLabelKey]; !ok || val != configLabelValue {
			return
		}

		glog.Infof("Adding ConfigMap: %v", key)

		bindPort, _ := strconv.Atoi(configMapData["bind-port"])
		targetPort, _ := strconv.Atoi(configMapData["target-port"])
		ssl, _ := strconv.ParseBool(configMapData["SSL"])
		sslPort, _ := strconv.Atoi(configMapData["ssl-port"])
		backendConfig := backends.BackendConfig{
			Host:              configMapData["host"],
			Namespace:         configMapData["namespace"],
			BindIp:            configMapData["bind-ip"],
			BindPort:          bindPort,
			TargetServiceName: configMapData["target-service-name"],
			TargetServiceId:   configMapData["target-service-id"],
			TargetPort:        targetPort,
			SSL:               ssl,
			SSLPort:           sslPort,
			Path:              configMapData["path"],
			TlsCert:           "some cert", //TODO get certs from secret
			TlsKey:            "some key",  //TODO get certs from secret
		}
		configMapController.backendController.AddConfig(name, backendConfig)
	}
}
