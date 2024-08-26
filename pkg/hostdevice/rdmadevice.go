package hostdevice

import (
	"fmt"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// Based on existing RDMA CNI plugin
// https://github.com/k8snetworkplumbingwg/rdma-cni

func MoveRDMALinkIn(hostIfName string, containerNsPAth string) error {
	containerNs, err := ns.GetNS(containerNsPAth)
	if err != nil {
		return err
	}
	hostDev, err := netlink.RdmaLinkByName(hostIfName)
	if err != nil {
		return err
	}

	if err = netlink.RdmaLinkSetNsFd(hostDev, uint32(containerNs.Fd())); err != nil {
		return fmt.Errorf("failed to move %q to container ns: %v", hostDev.Attrs.Name, err)
	}

	return nil
}

func MoveRDMALinkOut(containerNsPAth string, ifName string) error {
	containerNs, err := ns.GetNS(containerNsPAth)
	if err != nil {
		return err
	}
	defaultNs, err := ns.GetCurrentNS()
	if err != nil {
		return err
	}
	defer defaultNs.Close()

	err = containerNs.Do(func(_ ns.NetNS) error {
		dev, err := netlink.RdmaLinkByName(ifName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", ifName, err)
		}

		if err = netlink.RdmaLinkSetNsFd(dev, uint32(defaultNs.Fd())); err != nil {
			return fmt.Errorf("failed to move %q to host netns: %v", dev.Attrs.Name, err)
		}
		return nil
	})

	if err != nil {
		return err
	}

	return nil
}
