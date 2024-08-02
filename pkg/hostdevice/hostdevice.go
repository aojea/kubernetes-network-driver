package hostdevice

import (
	"fmt"
	"net"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
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

func MoveLinkIn(hostIfName string, containerNs ns.NetNS, ifName string) (netlink.Link, error) {
	hostDev, err := netlink.LinkByName(hostIfName)
	if err != nil {
		return nil, err
	}
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

func MoveLinkOut(containerNs ns.NetNS, ifName string) error {
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
