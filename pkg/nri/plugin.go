package nri

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
)

const (
	// Prefix of the key used for network device annotations.
	netdeviceKey = "netdevices.nri.io"
)

// Based on existing host-device CNI plugin
// https://github.com/containernetworking/plugins/blob/main/plugins/main/host-device/host-device.go

// setTempName sets a temporary name for netdevice to avoid collisions with interfaces names.
func setTempName(dev netlink.Link) (netlink.Link, error) {
	tempName := fmt.Sprintf("%s%d", "temp_", dev.Attrs().Index)

	// rename to tempName
	if err := netlink.LinkSetName(dev, tempName); err != nil {
		return nil, fmt.Errorf("failed to rename device %q to %q: %v", dev.Attrs().Name, tempName, err)
	}

	// Get updated Link obj
	tempDev, err := netlink.LinkByName(tempName)
	if err != nil {
		return nil, fmt.Errorf("failed to find %q after rename to %q: %v", dev.Attrs().Name, tempName, err)
	}

	return tempDev, nil
}

func moveLinkIn(hostDev netlink.Link, containerNs ns.NetNS, ifName string) (netlink.Link, error) {
	origLinkFlags := hostDev.Attrs().Flags
	hostDevName := hostDev.Attrs().Name
	defaultNs, err := ns.GetCurrentNS()
	if err != nil {
		return nil, fmt.Errorf("failed to get host namespace: %v", err)
	}

	// Devices can be renamed only when down
	if err = netlink.LinkSetDown(hostDev); err != nil {
		return nil, fmt.Errorf("failed to set %q down: %v", hostDev.Attrs().Name, err)
	}

	// restore original link state in case of error
	defer func() {
		if err != nil {
			if origLinkFlags&net.FlagUp == net.FlagUp && hostDev != nil {
				_ = netlink.LinkSetUp(hostDev)
			}
		}
	}()

	hostDev, err = setTempName(hostDev)
	if err != nil {
		return nil, fmt.Errorf("failed to rename device %q to temporary name: %v", hostDevName, err)
	}

	// restore original netdev name in case of error
	defer func() {
		if err != nil && hostDev != nil {
			_ = netlink.LinkSetName(hostDev, hostDevName)
		}
	}()

	if err = netlink.LinkSetNsFd(hostDev, int(containerNs.Fd())); err != nil {
		return nil, fmt.Errorf("failed to move %q to container ns: %v", hostDev.Attrs().Name, err)
	}

	var contDev netlink.Link
	tempDevName := hostDev.Attrs().Name
	if err = containerNs.Do(func(_ ns.NetNS) error {
		var err error
		contDev, err = netlink.LinkByName(tempDevName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", tempDevName, err)
		}

		// move netdev back to host namespace in case of error
		defer func() {
			if err != nil {
				_ = netlink.LinkSetNsFd(contDev, int(defaultNs.Fd()))
				// we need to get updated link object as link was moved back to host namepsace
				_ = defaultNs.Do(func(_ ns.NetNS) error {
					hostDev, _ = netlink.LinkByName(tempDevName)
					return nil
				})
			}
		}()

		// Save host device name into the container device's alias property
		if err = netlink.LinkSetAlias(contDev, hostDevName); err != nil {
			return fmt.Errorf("failed to set alias to %q: %v", tempDevName, err)
		}
		// Rename container device to respect args.IfName
		if err = netlink.LinkSetName(contDev, ifName); err != nil {
			return fmt.Errorf("failed to rename device %q to %q: %v", tempDevName, ifName, err)
		}

		// restore tempDevName in case of error
		defer func() {
			if err != nil {
				_ = netlink.LinkSetName(contDev, tempDevName)
			}
		}()

		// Bring container device up
		if err = netlink.LinkSetUp(contDev); err != nil {
			return fmt.Errorf("failed to set %q up: %v", ifName, err)
		}

		// bring device down in case of error
		defer func() {
			if err != nil {
				_ = netlink.LinkSetDown(contDev)
			}
		}()

		// Retrieve link again to get up-to-date name and attributes
		contDev, err = netlink.LinkByName(ifName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", ifName, err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return contDev, nil
}

func moveLinkOut(containerNs ns.NetNS, ifName string) error {
	defaultNs, err := ns.GetCurrentNS()
	if err != nil {
		return err
	}
	defer defaultNs.Close()

	var tempName string
	var origDev netlink.Link
	err = containerNs.Do(func(_ ns.NetNS) error {
		dev, err := netlink.LinkByName(ifName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", ifName, err)
		}
		origDev = dev

		// Devices can be renamed only when down
		if err = netlink.LinkSetDown(dev); err != nil {
			return fmt.Errorf("failed to set %q down: %v", ifName, err)
		}

		defer func() {
			// If moving the device to the host namespace fails, set its name back to ifName so that this
			// function can be retried. Also bring the device back up, unless it was already down before.
			if err != nil {
				_ = netlink.LinkSetName(dev, ifName)
				if dev.Attrs().Flags&net.FlagUp == net.FlagUp {
					_ = netlink.LinkSetUp(dev)
				}
			}
		}()

		newLink, err := setTempName(dev)
		if err != nil {
			return fmt.Errorf("failed to rename device %q to temporary name: %v", ifName, err)
		}
		dev = newLink
		tempName = dev.Attrs().Name

		if err = netlink.LinkSetNsFd(dev, int(defaultNs.Fd())); err != nil {
			return fmt.Errorf("failed to move %q to host netns: %v", tempName, err)
		}
		return nil
	})

	if err != nil {
		return err
	}

	// Rename the device to its original name from the host namespace
	tempDev, err := netlink.LinkByName(tempName)
	if err != nil {
		return fmt.Errorf("failed to find %q in host namespace: %v", tempName, err)
	}

	if err = netlink.LinkSetName(tempDev, tempDev.Attrs().Alias); err != nil {
		// move device back to container ns so it may be retired
		defer func() {
			_ = netlink.LinkSetNsFd(tempDev, int(containerNs.Fd()))
			_ = containerNs.Do(func(_ ns.NetNS) error {
				lnk, err := netlink.LinkByName(tempName)
				if err != nil {
					return err
				}
				_ = netlink.LinkSetName(lnk, ifName)
				if origDev.Attrs().Flags&net.FlagUp == net.FlagUp {
					_ = netlink.LinkSetUp(lnk)
				}
				return nil
			})
		}()
		return fmt.Errorf("failed to restore %q to original name %q: %v", tempName, tempDev.Attrs().Alias, err)
	}

	return nil
}

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

	hostDev, err := netlink.LinkByName(n.Name)
	if err != nil {
		return err
	}

	_, err = moveLinkIn(hostDev, containerNs, n.NewName)
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

	err = moveLinkOut(containerNs, n.NewName)
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
