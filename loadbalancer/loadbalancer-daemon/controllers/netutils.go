package controllers

import (
	"bytes"
	"errors"
	"net"
	"os/exec"
	"regexp"

	"github.com/golang/glog"
)

var (
	invalidIfaces = []string{"lo", "docker0", "flannel.1", "cbr0"}
	vethRegex     = regexp.MustCompile(`^veth.*`)
)

// interfaceByIP returns the local network interface name that is using the
// specified IP address. If no interface is found returns an empty string.
func interfaceByIP(ip string) string {
	for _, iface := range netInterfaces() {
		ifaceIP, err := ipByInterface(iface.Name)
		if err == nil && ip == ifaceIP {
			return iface.Name
		}
	}
	return ""
}

func ipByInterface(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}

	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ip := ipnet.IP.String()
				return ip, nil
			}
		}
	}

	return "", errors.New("Found no IPv4 addresses.")
}

// netInterfaces returns a slice containing the local network interfaces
// excluding lo, docker0, flannel.1 and veth interfaces.
func netInterfaces() []net.Interface {
	validIfaces := []net.Interface{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return validIfaces
	}

	for _, iface := range ifaces {
		if !vethRegex.MatchString(iface.Name) && stringSlice(invalidIfaces).pos(iface.Name) == -1 {
			validIfaces = append(validIfaces, iface)
		}
	}

	return validIfaces
}

type stringSlice []string

// pos returns the position of a string in a slice.
// If it does not exists in the slice returns -1.
func (slice stringSlice) pos(value string) int {
	for p, v := range slice {
		if v == value {
			return p
		}
	}

	return -1
}

func shellOut(cmd string) {
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
