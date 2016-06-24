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
	"bytes"
	"errors"
	"net"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned"
)

var ErrIPRangeExhausted = errors.New("Exhausted given Virtual IP range")

const ipConfigMapName = "ip-manager-configmap"

type IPManager struct {
	configMapName string
	ipRange       ipRange
	namespace     string
	kubeClient    *unversioned.Client
	ipStatusMap   map[string]bool
}

type ipRange struct {
	startIP string
	endIP   string
}

func NewIPManager(startIP string, endIP string, namespace string, kubeClient *unversioned.Client) *IPManager {

	ipRange := ipRange{
		startIP: startIP,
		endIP:   endIP,
	}
	ipManager := IPManager{
		configMapName: ipConfigMapName,
		ipRange:       ipRange,
		namespace:     namespace,
		kubeClient:    kubeClient,
	}
	return &ipManager
}

func (ipManager *IPManager) GenerateVirtualIP(configMap *api.ConfigMap) (string, error) {
	virtualIP, err := ipManager.getFreeVirtualIP()
	if err != nil {
		return "", err
	}

	//update ipConfigMap to add new configMap entry
	ipConfigMap := ipManager.getConfigMap()
	ipConfigMapData := ipConfigMap.Data
	name := configMap.Namespace + "-" + configMap.Name
	ipConfigMapData[virtualIP] = name

	_, err = ipManager.kubeClient.ConfigMaps(ipManager.namespace).Update(ipConfigMap)
	if err != nil {
		glog.Infof("Error updating ip configmap %v: %v", ipConfigMap.Name, err)
		return "", err
	}

	return virtualIP, nil
}

func (ipManager *IPManager) DeleteVirtualIP(name string) error {
	ipConfigMap := ipManager.getConfigMap()
	ipConfigMapData := ipConfigMap.Data

	//delete the configMap entry
	for k, v := range ipConfigMapData {
		if v == name {
			delete(ipConfigMapData, k)
			break
		}
	}

	_, err := ipManager.kubeClient.ConfigMaps(ipManager.namespace).Update(ipConfigMap)
	if err != nil {
		glog.Infof("Error updating ip configmap %v: %v", ipConfigMap.Name, err)
		return err
	}
	return nil
}

//gets the ip configmap or creates if it doesn't exist
func (ipManager *IPManager) getConfigMap() *api.ConfigMap {
	cmClient := ipManager.kubeClient.ConfigMaps(ipManager.namespace)
	cm, err := cmClient.Get(ipManager.configMapName)
	if err != nil {
		glog.Infof("ConfigMap %v does not exist. Creating...", ipManager.configMapName)
		configMapRequest := &api.ConfigMap{
			ObjectMeta: api.ObjectMeta{
				Name:      ipManager.configMapName,
				Namespace: ipManager.namespace,
			},
		}
		cm, err = cmClient.Create(configMapRequest)
		if err != nil {
			glog.Infof("Error creating configmap %v", err)
		}
	}
	return cm
}

//generate virtual IP in the given range
func (ipManager *IPManager) getFreeVirtualIP() (string, error) {
	startIPV4 := net.ParseIP(ipManager.ipRange.startIP).To4()
	endIPV4 := net.ParseIP(ipManager.ipRange.endIP).To4()
	temp := startIPV4
	ipConfigMap := ipManager.getConfigMap()
	ipConfigMapData := ipConfigMap.Data

	//check if the start IP is allocated
	if _, ok := ipConfigMapData[ipManager.ipRange.startIP]; !ok {
		return ipManager.ipRange.startIP, nil
	}

	for bytes.Compare(startIPV4, endIPV4) != 0 {
		for i := 3; i >= 0; i-- {
			if temp[i] == 255 {
				temp[i-1]++
			}
		}
		startIPV4[3]++

		if _, ok := ipConfigMapData[temp.String()]; !ok {
			return temp.String(), nil
		}
	}
	return "", ErrIPRangeExhausted
}
