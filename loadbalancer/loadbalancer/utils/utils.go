package utils

import (
	"bytes"
	"fmt"
	"os/exec"
	"reflect"
	"sort"
	"strings"

	"github.com/golang/glog"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/util/sets"
)

type diff struct {
	key  string
	a, b string
}

type orderedDiffs []diff

func (d orderedDiffs) Len() int      { return len(d) }
func (d orderedDiffs) Swap(i, j int) { d[i], d[j] = d[j], d[i] }
func (d orderedDiffs) Less(i, j int) bool {
	a, b := d[i].key, d[j].key
	if a < b {
		return true
	}
	return false
}

// GetConfigMapGroups gets all the groups in the keys
// The loadbalancer Configmap has the form of
// k -> groupName.configKey
// v -> configValue
func GetConfigMapGroups(cm map[string]string) sets.String {
	keys := MapKeys(cm)
	configMapGroups := sets.NewString()
	for _, k := range keys {
		configMapGroups.Insert(getGroupName(k))
	}
	return configMapGroups
}

func getGroupName(key string) string {
	return strings.Split(key, ".")[0]
}

func GetUpdatedConfigMapGroups(m1, m2 map[string]string) sets.String {
	updatedConfigMapGroups := sets.NewString()
	diff := getConfigMapDiff(m1, m2)
	for _, d := range diff {
		updatedConfigMapGroups.Insert(getGroupName(d.key))
	}
	return updatedConfigMapGroups
}

func getConfigMapDiff(oldCM, newCM map[string]string) []diff {
	if reflect.DeepEqual(oldCM, newCM) {
		return nil
	}
	oldKeys := make(map[string]string)
	for _, key := range MapKeys(oldCM) {
		oldKeys[key] = oldCM[key]
	}
	var missing []diff
	for _, key := range MapKeys(newCM) {
		if _, ok := oldKeys[key]; ok {
			delete(oldKeys, key)
			if oldCM[key] == newCM[key] {
				continue
			}
			missing = append(missing, diff{key: key, a: oldCM[key], b: newCM[key]})
			continue
		}
		missing = append(missing, diff{key: key, a: "", b: newCM[key]})
	}
	for key, value := range oldKeys {
		missing = append(missing, diff{key: key, a: value, b: ""})
	}
	sort.Sort(orderedDiffs(missing))
	return missing

}

// MapKeys return keys of a map
func MapKeys(m map[string]string) []string {
	keys := make([]string, len(m))

	i := 0
	for k := range m {
		keys[i] = k
		i++
	}
	return keys
}

// GetNodeHostIP returns the provided node's IP, based on the priority:
// 1. NodeExternalIP
// 2. NodeLegacyHostIP
// 3. NodeInternalIP
func GetNodeHostIP(node api.Node) (*string, error) {
	addresses := node.Status.Addresses
	addressMap := make(map[api.NodeAddressType][]api.NodeAddress)
	for i := range addresses {
		addressMap[addresses[i].Type] = append(addressMap[addresses[i].Type], addresses[i])
	}
	if addresses, ok := addressMap[api.NodeExternalIP]; ok {
		return &addresses[0].Address, nil
	}
	if addresses, ok := addressMap[api.NodeLegacyHostIP]; ok {
		return &addresses[0].Address, nil
	}
	if addresses, ok := addressMap[api.NodeInternalIP]; ok {
		return &addresses[0].Address, nil
	}
	return nil, fmt.Errorf("Host IP unknown; known addresses: %v", addresses)
}

func ShellOut(cmd string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	glog.Infof("executing %s", cmd)

	command := exec.Command("sh", "-c", cmd)
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Start()
	if err != nil {
		glog.Fatalf("Failed to execute %v, err: %v", cmd, err)
	}

	err = command.Wait()
	if err != nil {
		glog.Errorf("Command %v stdout: %q", cmd, stdout.String())
		glog.Errorf("Command %v stderr: %q", cmd, stderr.String())
		glog.Fatalf("Command %v finished with error: %v", cmd, err)
	}
}
