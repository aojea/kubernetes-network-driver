package nri

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"

	"github.com/aojea/kubernetes-network-driver/pkg/hostdevice"
	"github.com/containernetworking/plugins/pkg/ns"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
)

const (
	// Prefix of the key used for network device annotations.
	netdeviceKey = "netdevices.nri.io"
)

// an annotated netdevice
// https://man7.org/linux/man-pages/man7/netdevice.7.html
type netdevice struct {
	Name    string `json:"name"`     // name in the runtime namespace
	NewName string `json:"new_name"` // name inside the pod namespace
	Address string `json:"address"`
	Prefix  int    `json:"prefix"`
	MTU     int    `json:"mtu"`
}

func (n *netdevice) inject(nsPath string) error {
	// Lock the OS Thread so we don't accidentally switch namespaces
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	containerNs, err := ns.GetNS(nsPath)
	if err != nil {
		return err
	}
	defer containerNs.Close()

	_, err = hostdevice.MoveLinkIn(n.Name, containerNs, n.NewName)
	if err != nil {
		return fmt.Errorf("failed to move link %v", err)
	}
	return nil
}

// remove the network device from the Pod namespace and recover its name
// Leaves the interface in down state to avoid issues with the root network.
func (n *netdevice) release(nsPath string) error {
	// Lock the OS Thread so we don't accidentally switch namespaces
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	containerNs, err := ns.GetNS(nsPath)
	if err != nil {
		return err
	}
	defer containerNs.Close()

	err = hostdevice.MoveLinkOut(containerNs, n.NewName)
	if err != nil {
		return err
	}

	return nil
}

// our injector plugin
type Plugin struct {
	Stub stub.Stub
}

func (p *Plugin) RunPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	// inject associated devices of the netdevice to the container
	netdevices, err := parseNetdevices(pod.Annotations)
	if err != nil {
		return err
	}

	if len(netdevices) == 0 {
		return nil
	}

	// get the pod network namespace
	var ns string
	for _, namespace := range pod.Linux.GetNamespaces() {
		if namespace.Type == "network" {
			ns = namespace.Path
			break
		}
	}
	// TODO check host network namespace
	if ns == "" {
		return nil
	}

	// attach the network devices to the pod namespace
	for _, n := range netdevices {
		err = n.inject(ns)
		if err != nil {
			return nil
		}
	}
	return nil
}

func (p *Plugin) StopPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	// release associated devices of the netdevice to the Pod
	netdevices, err := parseNetdevices(pod.Annotations)
	if err != nil {
		return err
	}

	if len(netdevices) == 0 {
		return nil
	}

	// get the pod network namespace
	var ns string
	for _, namespace := range pod.Linux.GetNamespaces() {
		if namespace.Type == "network" {
			ns = namespace.Path
			break
		}
	}
	// TODO check host network namespace
	if ns == "" {
		return nil
	}

	// release the network devices from the pod namespace
	for _, n := range netdevices {
		err = n.release(ns)
		if err != nil {
			return nil
		}
	}

	return nil
}

func parseNetdevices(annotations map[string]string) ([]netdevice, error) {
	var (
		key        string
		annotation []byte
		netdevices []netdevice
	)

	// look up effective device annotation and unmarshal devices
	for _, key = range []string{
		netdeviceKey + "/pod",
		netdeviceKey,
	} {
		if value, ok := annotations[key]; ok {
			annotation = []byte(value)
			break
		}
	}

	if annotation == nil {
		return nil, nil
	}

	if err := yaml.Unmarshal(annotation, &netdevices); err != nil {
		return nil, fmt.Errorf("invalid device annotation %q: %w", key, err)
	}

	// validate and default
	for _, n := range netdevices {
		if n.NewName == "" {
			n.NewName = n.Name
		}
		if n.Address != "" {
			ip := net.ParseIP(n.Address)
			if ip == nil {
				return nil, fmt.Errorf("error parsing address %s", n.Address)
			}

			if n.Prefix == 0 {
				if ip.To4() == nil {
					n.Prefix = 128
				} else {
					n.Prefix = 32
				}
			}
		}

	}
	return netdevices, nil
}

// Dump one or more objects, with an optional global prefix and per-object tags.
func dump(args ...interface{}) {
	var (
		prefix string
		idx    int
	)

	if len(args)&0x1 == 1 {
		prefix = args[0].(string)
		idx++
	}

	for ; idx < len(args)-1; idx += 2 {
		tag, obj := args[idx], args[idx+1]
		msg, err := yaml.Marshal(obj)
		if err != nil {
			klog.Infof("%s: %s: failed to dump object: %v", prefix, tag, err)
			continue
		}

		if prefix != "" {
			klog.Infof("%s: %s:", prefix, tag)
			for _, line := range strings.Split(strings.TrimSpace(string(msg)), "\n") {
				klog.Infof("%s:    %s", prefix, line)
			}
		} else {
			klog.Infof("%s:", tag)
			for _, line := range strings.Split(strings.TrimSpace(string(msg)), "\n") {
				klog.Infof("  %s", line)
			}
		}
	}
}
