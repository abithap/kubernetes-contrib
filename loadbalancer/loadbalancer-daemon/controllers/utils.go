package controllers

import (
	"reflect"
	"sort"
	"strings"

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

func getConfigMapGroups(cm map[string]string) sets.String {
	keys := mapKeys(cm)
	configMapGroups := sets.NewString()
	for _, k := range keys {
		configMapGroups.Insert(getGroupName(k))
	}
	return configMapGroups
}

func getGroupName(key string) string {
	return strings.Split(key, ".")[0]
}

func getUpdatedConfigMapGroups(m1, m2 map[string]string) sets.String {
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
	for _, key := range mapKeys(oldCM) {
		oldKeys[key] = oldCM[key]
	}
	var missing []diff
	for _, key := range mapKeys(newCM) {
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

func mapKeys(m map[string]string) []string {
	keys := make([]string, len(m))

	i := 0
	for k := range m {
		keys[i] = k
		i++
	}
	return keys
}
