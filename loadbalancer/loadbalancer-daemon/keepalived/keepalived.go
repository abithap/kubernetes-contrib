/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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

package keepalived

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"text/template"
	"time"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/util/dbus"
	k8sexec "k8s.io/kubernetes/pkg/util/exec"
	"k8s.io/kubernetes/pkg/util/iptables"
	"k8s.io/kubernetes/pkg/util/sets"
	"k8s.io/kubernetes/pkg/util/sysctl"
)

const (
	iptablesChain = "LOADBALANCER-DAEMON"
	keepalivedCfg = "/etc/keepalived/keepalived.conf"
	reloadQPS     = 10.0
	resyncPeriod  = 10 * time.Second
)

var (
	keepalivedTmpl = "keepalived.tmpl"

	// sysctl changes required by keepalived
	sysctlAdjustments = map[string]int{
		// allows processes to bind() to non-local IP addresses
		"net/ipv4/ip_nonlocal_bind": 1,
		// enable connection tracking for LVS connections
		"net/ipv4/vs/conntrack": 1,
	}
)

type KeepalivedController struct {
	keepalived *Keepalived
	ipt        iptables.Interface
	command    *exec.Cmd
}

type Keepalived struct {
	Interface     string
	Vips          sets.String
	IptablesChain string
}

// NewKeepalivedController creates a new keepalived controller
func NewKeepalivedController(nodeInterface string) KeepalivedController {

	// System init
	// loadIPVModule()
	changeSysctl()
	// resetIPVS()

	k := Keepalived{
		Interface:     nodeInterface,
		Vips:          sets.NewString(),
		IptablesChain: iptablesChain,
	}

	execer := k8sexec.New()
	dbus := dbus.New()
	iptInterface := iptables.New(execer, dbus, iptables.ProtocolIpv4)

	kaControl := KeepalivedController{
		keepalived: &k,
		ipt:        iptInterface,
	}

	return kaControl
}

// Start starts a keepalived process in foreground.
// In case of any error it will terminate the execution with a fatal error
func (k *KeepalivedController) Start() {
	// ae, err := k.ipt.EnsureChain(iptables.TableFilter, iptables.Chain(iptablesChain))
	// if err != nil {
	// 	glog.Fatalf("unexpected error: %v", err)
	// }
	// if ae {
	// 	glog.V(2).Infof("chain %v already existed", iptablesChain)
	// }

	// k.command = exec.Command("keepalived",
	// 	"--dont-fork",
	// 	"--log-console",
	// 	"--release-vips",
	// 	"--pid", "/keepalived.pid")

	// k.command.Stdout = os.Stdout
	// k.command.Stderr = os.Stderr

	// // in case the pod is terminated we need to check that the vips are removed
	// c := make(chan os.Signal, 2)
	// signal.Notify(c, syscall.SIGTERM)
	// go func() {
	// 	for range c {
	// 		glog.Warning("TERM signal received. freeing vips")
	// 		for vip := range k.keepalived.Vips {
	// 			k.freeVIP(vip)
	// 		}

	// 		err := k.ipt.FlushChain(iptables.TableFilter, iptables.Chain(iptablesChain))
	// 		if err != nil {
	// 			glog.V(2).Infof("unexpected error flushing iptables chain %v: %v", err, iptablesChain)
	// 		}
	// 	}
	// }()

	// if err := k.command.Start(); err != nil {
	// 	glog.Errorf("keepalived error: %v", err)
	// }

	// if err := k.command.Wait(); err != nil {
	// 	glog.Fatalf("keepalived error: %v", err)
	// }
	shellOut("service keepalived start")
}

// AddVIP adds a new VIP to the keepalived config and reload keepalived process
func (k *KeepalivedController) AddVIP(vip string) {
	glog.Infof("Adding VIP %v", vip)
	if k.keepalived.Vips.Has(vip) {
		glog.Errorf("VIP %v has already been added", vip)
		return
	}
	k.keepalived.Vips.Insert(vip)
	k.writeCfg()
	k.reload()
}

// DeleteVIP removes a VIP from the keepalived config and reload keepalived process
func (k *KeepalivedController) DeleteVIP(vip string) {
	glog.Infof("Deleing VIP %v", vip)
	if !k.keepalived.Vips.Has(vip) {
		glog.Errorf("VIP %v had not been added.", vip)
		return
	}
	k.keepalived.Vips.Delete(vip)
	k.writeCfg()
	k.reload()
}

// DeleteAllVIPs Delete all VIPs from the keepalived config and reload keepalived process
func (k *KeepalivedController) DeleteAllVIPs() {
	glog.Infof("Deleing all VIPs")
	k.keepalived.Vips.Delete(k.keepalived.Vips.List()...)
	k.writeCfg()
	k.reload()
}

// writeCfg creates a new keepalived configuration file.
// In case of an error with the generation it returns the error
func (k *KeepalivedController) writeCfg() {
	tmpl, err := template.New(keepalivedTmpl).ParseFiles(keepalivedTmpl)
	w, err := os.Create(keepalivedCfg)
	if err != nil {
		glog.Fatalf("Failed to open %v: %v", keepalivedCfg, err)
	}
	defer w.Close()

	if err := tmpl.Execute(w, *k.keepalived); err != nil {
		glog.Fatalf("Failed to write template %v", err)
	}
}

// reload sends SIGHUP to keepalived to reload the configuration.
func (k *KeepalivedController) reload() {
	glog.Info("reloading keepalived")
	// err := syscall.Kill(k.command.Process.Pid, syscall.SIGHUP)
	// if err != nil {
	// 	glog.Fatalf("Could not reload keepalived: %v", err)
	// }
	shellOut("service keepalived reload")
}

func (k *KeepalivedController) freeVIP(vip string) error {
	glog.Infof("removing configured VIP %v", vip)
	out, err := k8sexec.New().Command("ip", "addr", "del", vip+"/32", "dev", k.keepalived.Interface).CombinedOutput()
	if err != nil {
		return fmt.Errorf("error reloading keepalived: %v\n%s", err, out)
	}
	return nil
}

// loadIPVModule load module require to use keepalived
func loadIPVModule() error {
	out, err := k8sexec.New().Command("modprobe", "ip_vs").CombinedOutput()
	if err != nil {
		glog.V(2).Infof("Error loading ip_vip: %s, %v", string(out), err)
		return err
	}

	_, err = os.Stat("/proc/net/ip_vs")
	return err
}

// changeSysctl changes the required network setting in /proc to get
// keepalived working in the local system.
func changeSysctl() error {
	for k, v := range sysctlAdjustments {
		if err := sysctl.SetSysctl(k, v); err != nil {
			return err
		}
	}
	return nil
}

func resetIPVS() error {
	glog.Info("cleaning ipvs configuration")
	_, err := k8sexec.New().Command("ipvsadm", "-C").CombinedOutput()
	if err != nil {
		return fmt.Errorf("error removing ipvs configuration: %v", err)
	}

	return nil
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
