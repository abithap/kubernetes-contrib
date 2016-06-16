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
	"time"

	factory "k8s.io/contrib/loadbalancer/loadbalancer-daemon/backend"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/cache"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/controller/framework"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/watch"
)

// ConfigMapController watches Kubernetes API for ConfigMap changes
// and reconfigures keepalived and backend when needed
type ConfigMapController struct {
	client              *client.Client
	configMapController *framework.Controller
	configMapLister     StoreToConfigMapLister
	stopCh              chan struct{}
	backendController   factory.BackendController
}

// StoreToConfigMapLister makes a Store that lists ConfigMap.
type StoreToConfigMapLister struct {
	cache.Store
}

// Values to verify the configmap object is a loadbalancer config
const (
	configLabelKey   = "loadbalancer"
	configLabelValue = "configmap"
)

var keyFunc = framework.DeletionHandlingMetaNamespaceKeyFunc

// NewConfigMapController creates a controller
func NewConfigMapController(kubeClient *client.Client, resyncPeriod time.Duration, namespace string, controller factory.BackendController) (*ConfigMapController, error) {
	configMapController := ConfigMapController{
		client:            kubeClient,
		stopCh:            make(chan struct{}),
		backendController: controller,
	}

	// Configmap has the form of
	// k -> configGroupName.configKey
	// v -> configValue
	configMapHandlers := framework.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cm := obj.(*api.ConfigMap)
			cmData := cm.Data
			groups := getConfigMapGroups(cmData)
			for group := range groups {
				backendConfig := createBackendConfig(cmData, group)
				go configMapController.backendController.AddConfig(group, backendConfig)
			}
		},
		DeleteFunc: func(obj interface{}) {
			cm := obj.(*api.ConfigMap)
			cmData := cm.Data
			groups := getConfigMapGroups(cmData)
			for group := range groups {
				go configMapController.backendController.DeleteConfig(group)
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				oldCM := old.(*api.ConfigMap).Data
				curCM := cur.(*api.ConfigMap).Data
				groups := getConfigMapGroups(curCM)
				updatedGroups := getUpdatedConfigMapGroups(oldCM, curCM)
				for group := range updatedGroups {
					if !groups.Has(group) {
						go configMapController.backendController.DeleteConfig(group)
					} else {
						backendConfig := createBackendConfig(curCM, group)
						go configMapController.backendController.AddConfig(group, backendConfig)
					}
				}
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
	<-configMapController.stopCh
}

func configMapListFunc(c *client.Client, ns string) func(api.ListOptions) (runtime.Object, error) {
	return func(opts api.ListOptions) (runtime.Object, error) {
		opts.LabelSelector = labels.Set{configLabelKey: configLabelValue}.AsSelector()
		return c.ConfigMaps(ns).List(opts)
	}
}

func configMapWatchFunc(c *client.Client, ns string) func(options api.ListOptions) (watch.Interface, error) {
	return func(options api.ListOptions) (watch.Interface, error) {
		options.LabelSelector = labels.Set{configLabelKey: configLabelValue}.AsSelector()
		return c.ConfigMaps(ns).Watch(options)
	}
}

func createBackendConfig(cm map[string]string, group string) factory.BackendConfig {
	bindPort, _ := strconv.Atoi(cm[group+".bind-port"])
	targetPort, _ := strconv.Atoi(cm[group+".target-port"])
	ssl, _ := strconv.ParseBool(cm[group+".SSL"])
	sslPort, _ := strconv.Atoi(cm[group+".ssl-port"])
	backendConfig := factory.BackendConfig{
		Host:              cm[group+".host"],
		Namespace:         cm[group+".namespace"],
		BindIp:            cm[group+".bind-ip"],
		BindPort:          bindPort,
		TargetServiceName: cm[group+".target-service-name"],
		TargetServiceId:   cm[group+".target-service-id"],
		TargetPort:        targetPort,
		SSL:               ssl,
		SSLPort:           sslPort,
		Path:              cm[group+".path"],
		TlsCert:           "some cert", //TODO get certs from secret
		TlsKey:            "some key",  //TODO get certs from secret
	}
	return backendConfig
}
